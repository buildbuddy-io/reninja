package include_scanner

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/shlex"
)

// scannableExtensions lists file extensions worth scanning for #include directives.
// This includes C/C++ sources/headers and assembly files that are preprocessed.
var scannableExtensions = map[string]bool{
	".c":   true,
	".cc":  true,
	".cpp": true,
	".cxx": true,
	".h":   true,
	".hh":  true,
	".hpp": true,
	".hxx": true,
	".inc": true,
	".s":   true, // assembly (preprocessed via cpp)
}

var includeRegex = regexp.MustCompile(`^\s*#\s*include\s*["<]([^">]+)[">]`)
var includeKindRegex = regexp.MustCompile(`^\s*#\s*include\s*(["<])`)

// Inclusion represents a single #include directive.
type Inclusion struct {
	Path   string // the path from the #include directive
	Quoted bool   // true for "...", false for <...>
}

// Scanner parses C/C++ files for #include directives and resolves them
// to discover transitively included project files.
type Scanner struct {
	parseCache map[string][]Inclusion
}

// New creates a new Scanner with an empty parse cache.
func New() *Scanner {
	return &Scanner{
		parseCache: make(map[string][]Inclusion),
	}
}

// ScanEdge scans all input files for transitive #include dependencies
// and returns the list of additional files not already in inputFiles.
func (s *Scanner) ScanEdge(inputFiles []string, command string) ([]string, error) {
	searchPaths := extractSearchPaths(command)

	inputSet := make(map[string]bool, len(inputFiles))
	for _, f := range inputFiles {
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		inputSet[abs] = true
	}

	visited := make(map[string]bool)
	for _, f := range inputFiles {
		if !isScannable(f) {
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		if err := s.scanFile(abs, searchPaths, visited); err != nil {
			// Don't fail the build if we can't scan a file; just skip it.
			continue
		}
	}

	var extra []string
	for path := range visited {
		if !inputSet[path] {
			// Convert back to a relative path if possible.
			rel, err := filepath.Rel(".", path)
			if err != nil {
				rel = path
			}
			extra = append(extra, rel)
		}
	}
	return extra, nil
}

func (s *Scanner) scanFile(filePath string, searchPaths []string, visited map[string]bool) error {
	if visited[filePath] {
		return nil
	}
	visited[filePath] = true

	inclusions, err := s.parseIncludes(filePath)
	if err != nil {
		return err
	}

	dir := filepath.Dir(filePath)
	for _, inc := range inclusions {
		resolved := resolveInclude(inc, dir, searchPaths)
		if resolved == "" {
			continue
		}
		if err := s.scanFile(resolved, searchPaths, visited); err != nil {
			continue
		}
	}
	return nil
}

func (s *Scanner) parseIncludes(filePath string) ([]Inclusion, error) {
	if cached, ok := s.parseCache[filePath]; ok {
		return cached, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var inclusions []Inclusion
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		m := includeRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		includePath := m[1]
		km := includeKindRegex.FindStringSubmatch(line)
		quoted := km != nil && km[1] == `"`
		inclusions = append(inclusions, Inclusion{
			Path:   includePath,
			Quoted: quoted,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	s.parseCache[filePath] = inclusions
	return inclusions, nil
}

// resolveInclude resolves an Inclusion to an absolute file path, or returns ""
// if the file is not found (e.g. system header).
func resolveInclude(inc Inclusion, includingFileDir string, searchPaths []string) string {
	// Handle absolute paths directly (e.g. CMake unity builds: #include </abs/path/file.cpp>).
	if filepath.IsAbs(inc.Path) {
		if _, err := os.Stat(inc.Path); err == nil {
			return inc.Path
		}
		return ""
	}
	if inc.Quoted {
		// For quoted includes, check relative to the including file's directory first.
		candidate := filepath.Join(includingFileDir, inc.Path)
		if _, err := os.Stat(candidate); err == nil {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs
			}
			return candidate
		}
	}
	// Check search paths (both quoted and angle bracket includes).
	for _, dir := range searchPaths {
		candidate := filepath.Join(dir, inc.Path)
		if _, err := os.Stat(candidate); err == nil {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs
			}
			return candidate
		}
	}
	return ""
}

// extractSearchPaths parses -I, -iquote, and -isystem flags from a compiler command.
func extractSearchPaths(command string) []string {
	args, err := shlex.Split(command)
	if err != nil {
		return nil
	}

	var paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-I" || arg == "-iquote" || arg == "-isystem":
			if i+1 < len(args) {
				i++
				paths = append(paths, args[i])
			}
		case strings.HasPrefix(arg, "-I"):
			paths = append(paths, strings.TrimPrefix(arg, "-I"))
		}
	}
	return paths
}

func isScannable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return scannableExtensions[ext]
}

// ExtractCommandReferencedPaths finds absolute paths in the command string
// that reference existing files or directories under the given root. These
// paths may not be declared as edge inputs but need to exist on the remote
// executor for the command to succeed (e.g. cmake scripts, data files).
//
// For regular files, sibling files in the same directory are also included
// since commands often reference files that depend on neighbors (e.g. cmake
// scripts that include() other modules from the same directory).
func ExtractCommandReferencedPaths(command, root string) []string {
	var paths []string
	seen := make(map[string]struct{})

	addPath := func(path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	siblingDirsSeen := make(map[string]struct{})

	for _, token := range strings.Fields(command) {
		// Find the project root anywhere in the token to handle flags
		// like -DFOO=/project/root/path or -P /project/root/path.
		idx := strings.Index(token, root)
		if idx < 0 {
			continue
		}
		path := strings.TrimRight(token[idx:], `"'`)

		if _, ok := seen[path]; ok {
			continue
		}

		// Only include paths that exist on disk to avoid adding output
		// paths that haven't been created yet.
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		addPath(path)

		// For regular files, also include sibling files in the same
		// directory. Commands often depend on neighboring files that
		// aren't listed in the command (e.g. cmake include() modules).
		if info.Mode().IsRegular() {
			dir := filepath.Dir(path)
			if _, ok := siblingDirsSeen[dir]; ok {
				continue
			}
			siblingDirsSeen[dir] = struct{}{}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".ninja_") {
					addPath(filepath.Join(dir, entry.Name()))
				}
			}
		}
	}

	return paths
}

// ExtractIntermediateDirsFromCommand finds absolute paths containing ".."
// in a command string and returns the directory prefixes that the kernel
// would need to traverse to resolve the ".." components. These directories
// must exist in the remote input tree for path resolution to succeed.
func ExtractIntermediateDirsFromCommand(command string) []string {
	var dirs []string
	seen := make(map[string]struct{})

	for _, token := range strings.Fields(command) {
		path := token
		// Strip common compiler flag prefixes to extract the path.
		for _, prefix := range []string{"-I", "-L"} {
			if strings.HasPrefix(token, prefix) && len(token) > len(prefix) {
				path = token[len(prefix):]
				break
			}
		}

		if !filepath.IsAbs(path) || !strings.Contains(path, "..") {
			continue
		}

		// Walk path components, collecting directories before each ".."
		// that the kernel must enter during path resolution.
		parts := strings.Split(path, "/")
		var stack []string
		for _, part := range parts {
			if part == "" || part == "." {
				continue
			}
			if part == ".." {
				if len(stack) > 0 {
					dir := "/" + strings.Join(stack, "/")
					if _, ok := seen[dir]; !ok {
						seen[dir] = struct{}{}
						dirs = append(dirs, dir)
					}
					stack = stack[:len(stack)-1]
				}
				continue
			}
			stack = append(stack, part)
		}
	}

	return dirs
}

// ExtractSearchDirectoryContents finds -I, -isystem, -iquote, and -L
// directory flags in the command string and returns all files found
// recursively within those directories that are under the given root.
// This allows remote execution of commands that search these directories
// at runtime (e.g. tablegen processing -I paths for .td includes).
func ExtractSearchDirectoryContents(command, root string) []string {
	args, err := shlex.Split(command)
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	var dirs []string

	addDir := func(dir string) {
		if !filepath.IsAbs(dir) {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return
			}
			dir = abs
		}
		if !strings.HasPrefix(dir, root+"/") && dir != root {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		dirs = append(dirs, dir)
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-I" || arg == "-iquote" || arg == "-isystem" || arg == "-L":
			if i+1 < len(args) {
				i++
				addDir(args[i])
			}
		case strings.HasPrefix(arg, "-I"):
			addDir(strings.TrimPrefix(arg, "-I"))
		case strings.HasPrefix(arg, "-L"):
			addDir(strings.TrimPrefix(arg, "-L"))
		}
	}

	var files []string
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			files = append(files, path)
			return nil
		})
	}

	return files
}

// ExtractRelativeDotDotContents finds relative paths containing ".." components
// in a command string, resolves them to absolute paths, and returns all files
// within resolved directories (or the resolved file and its siblings for
// regular files). This handles commands like "cd ../../tools/icu" that
// reference directories outside the build output tree but still within the
// project root. Sibling files are included for regular files because scripts
// commonly import neighboring modules (e.g. Python's import of pdl.py from
// check_protocol_compatibility.py).
func ExtractRelativeDotDotContents(command, root string) []string {
	var files []string
	seen := make(map[string]struct{})
	siblingDirsSeen := make(map[string]struct{})

	addFile := func(path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}

	for _, token := range strings.Fields(command) {
		if filepath.IsAbs(token) || !strings.Contains(token, "..") {
			continue
		}

		// Strip common flag prefixes to extract the path.
		path := token
		for _, prefix := range []string{"-I", "-L"} {
			if strings.HasPrefix(token, prefix) && len(token) > len(prefix) {
				path = token[len(prefix):]
				break
			}
		}
		// Strip trailing shell operators and quotes.
		path = strings.TrimRight(path, ";&|\"'")

		if !strings.Contains(path, "..") {
			continue
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}

		// Must be under project root.
		if !strings.HasPrefix(abs, root+"/") && abs != root {
			continue
		}

		if _, ok := seen[abs]; ok {
			continue
		}

		info, err := os.Stat(abs)
		if err != nil {
			continue
		}

		if info.IsDir() {
			filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				addFile(p)
				return nil
			})
		} else {
			addFile(abs)
			// Include sibling files in the same directory — scripts often
			// depend on neighboring modules (e.g. Python imports).
			dir := filepath.Dir(abs)
			if _, ok := siblingDirsSeen[dir]; !ok {
				siblingDirsSeen[dir] = struct{}{}
				entries, err := os.ReadDir(dir)
				if err == nil {
					for _, entry := range entries {
						if !entry.IsDir() {
							addFile(filepath.Join(dir, entry.Name()))
						}
					}
				}
			}
		}
	}

	return files
}

// ExtractCdRelativePaths handles commands that start with "cd <dir> &&" by
// resolving relative path-like tokens against the cd target directory. This
// covers commands like "cd /project/root && python3 scripts/foo.py args..."
// where the script path is relative to the cd target, not to the build
// directory. Sibling files are included for regular files because scripts
// commonly import neighboring modules (e.g. Python imports).
func ExtractCdRelativePaths(command, root string) []string {
	// Find the cd target directory.
	fields := strings.Fields(command)
	var cdTarget string
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "cd" {
			cdTarget = strings.TrimRight(fields[i+1], ";&|\"'")
			break
		}
	}
	if cdTarget == "" || !filepath.IsAbs(cdTarget) {
		return nil
	}
	if !strings.HasPrefix(cdTarget, root+"/") && cdTarget != root {
		return nil
	}

	var files []string
	seen := make(map[string]struct{})
	siblingDirsSeen := make(map[string]struct{})

	addFile := func(path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}

	for _, token := range fields {
		// Skip absolute paths (handled by ExtractCommandReferencedPaths).
		if filepath.IsAbs(token) {
			continue
		}
		// Skip flags and shell operators.
		if strings.HasPrefix(token, "-") {
			continue
		}
		if token == "&&" || token == "||" || token == "|" || token == ";" || token == "cd" {
			continue
		}
		// Must look like a file path (contains a path separator or extension).
		if !strings.Contains(token, "/") && !strings.Contains(token, ".") {
			continue
		}

		path := strings.TrimRight(token, ";&|\"'")
		resolved := filepath.Clean(filepath.Join(cdTarget, path))

		if !strings.HasPrefix(resolved, root+"/") && resolved != root {
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}

		if info.IsDir() {
			filepath.WalkDir(resolved, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				addFile(p)
				return nil
			})
		} else {
			addFile(resolved)
			// Include sibling files — scripts often import neighboring modules.
			dir := filepath.Dir(resolved)
			if _, ok := siblingDirsSeen[dir]; !ok {
				siblingDirsSeen[dir] = struct{}{}
				entries, err := os.ReadDir(dir)
				if err == nil {
					for _, entry := range entries {
						if !entry.IsDir() {
							addFile(filepath.Join(dir, entry.Name()))
						}
					}
				}
			}
		}
	}

	return files
}

// ExtractThinArchiveMembers checks if the given file is a GNU thin archive
// and returns the resolved paths of its members. Thin archives store
// references to .o files by path rather than embedding them, so those files
// must be included as additional inputs for remote execution. Returns nil
// if the file is not a thin archive or cannot be parsed.
func ExtractThinArchiveMembers(archivePath string) []string {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Thin archives start with "!<thin>\n".
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil
	}
	if string(magic) != "!<thin>\n" {
		// Not a thin archive (regular archive or other file); no members to extract.
		return nil
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	archiveDir := filepath.Dir(archivePath)
	var extNames string
	var members []string

	pos := 0
	for pos+60 <= len(data) {
		hdr := data[pos : pos+60]
		name := strings.TrimRight(string(hdr[0:16]), " ")
		sizeStr := strings.TrimSpace(string(hdr[48:58]))
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			break
		}
		pos += 60

		// "/" is the symbol table, "//" is the extended filename table.
		// Both have actual data stored in the archive even for thin archives.
		if name == "/" || name == "//" {
			if name == "//" && pos+int(size) <= len(data) {
				extNames = string(data[pos : pos+int(size)])
			}
			pos += int(size)
			if pos%2 != 0 {
				pos++
			}
			continue
		}

		// Regular member: no data follows in a thin archive.
		var memberName string
		if strings.HasPrefix(name, "/") {
			// Extended name reference: /offset
			offset, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(name[1:], "/")))
			if err == nil && offset < len(extNames) {
				rest := extNames[offset:]
				if end := strings.Index(rest, "/\n"); end >= 0 {
					memberName = rest[:end]
				}
			}
		} else {
			memberName = strings.TrimSuffix(name, "/")
		}

		if memberName != "" {
			var resolved string
			if filepath.IsAbs(memberName) {
				resolved = memberName
			} else {
				resolved = filepath.Clean(filepath.Join(archiveDir, memberName))
			}
			members = append(members, resolved)
		}
	}

	return members
}
