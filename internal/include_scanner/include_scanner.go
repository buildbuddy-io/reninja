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
	"sync"

	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/google/shlex"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
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

var includeRegex = regexp.MustCompile(`^\s*#\s*(?:include|include_next|import)\s*(["<])([^">]+)[">]`)

// Inclusion represents a single #include directive.
type Inclusion struct {
	Path   string // the path from the #include directive
	Quoted bool   // true for "...", false for <...>
}

// Scanner parses C/C++ files for #include directives and resolves them
// to discover transitively included project files.
type Scanner struct {
	parseGroup singleflight.Group // map of filepath (string) to inclusions ([]Inclusion)
	mu         sync.Mutex         // PROTECTS(parseCache)
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
	splitCommand, err := shlex.Split(command)
	if err != nil {
		return nil, err
	}
	searchPaths := extractSearchPaths(splitCommand)
	forceIncludes := extractForceIncludes(splitCommand)

	inputSet := make(map[string]bool, len(inputFiles))
	for _, f := range inputFiles {
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		// Use realpath so that inputSet and visited use the same key space.
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		inputSet[abs] = true
	}

	var g errgroup.Group
	var mu sync.Mutex
	visited := make(map[string]bool)

	const maxScanDepth = 100

	var scanFile func(filePath string, depth int) error
	scanFile = func(filePath string, depth int) error {
		if depth >= maxScanDepth {
			return statuserr.ResourceExhaustedErrorf("include scan depth exceeded %d at %s", maxScanDepth, filePath)
		}

		// Use the real path (resolving symlinks) as the visited key to prevent
		// infinite traversal through symlink cycles. For example, if a/link -> ../b
		// and b/link -> ../a, following a/link/foo.h and b/link/foo.h would
		// resolve to the same real files.
		realPath, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			realPath = filePath
		}
		mu.Lock()
		if visited[realPath] {
			mu.Unlock()
			return nil
		}
		visited[realPath] = true
		mu.Unlock()

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
			g.Go(func() error {
				return scanFile(resolved, depth+1)
			})
		}
		return nil
	}

	for _, f := range inputFiles {
		if !isScannable(f) {
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		g.Go(func() error {
			return scanFile(abs, 0)
		})
	}

	// Scan force-included files (-include flag) as additional starting
	// points. These are implicitly included before each source file.
	for _, f := range forceIncludes {
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		g.Go(func() error {
			return scanFile(abs, 0)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
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

func (s *Scanner) parseIncludes(filePath string) ([]Inclusion, error) {
	s.mu.Lock()
	cached, ok := s.parseCache[filePath]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}

	v, err, _ := s.parseGroup.Do(filePath, func() (interface{}, error) {
		return s.doParseIncludes(filePath)
	})
	if err != nil {
		return nil, err
	}
	inclusions := v.([]Inclusion)

	s.mu.Lock()
	s.parseCache[filePath] = inclusions
	s.mu.Unlock()

	return inclusions, nil
}

func (s *Scanner) doParseIncludes(filePath string) ([]Inclusion, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var inclusions []Inclusion
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		// Handle backslash-newline continuations: join lines ending
		// with '\' before applying the include regex.
		for strings.HasSuffix(line, "\\") && scanner.Scan() {
			line = line[:len(line)-1] + scanner.Text()
		}

		m := includeRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		inclusions = append(inclusions, Inclusion{
			Path:   m[2],
			Quoted: m[1] == `"`,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return inclusions, nil
}

// resolveInclude resolves an Inclusion to an absolute file path, or returns ""
// if the file is not found (e.g. system header).
func resolveInclude(inc Inclusion, includingFileDir string, searchPaths []string) string {
	// Handle absolute paths directly (e.g. CMake unity builds: #include </abs/path/file.cpp>).
	if filepath.IsAbs(inc.Path) {
		if info, err := os.Stat(inc.Path); err == nil && !info.IsDir() {
			return inc.Path
		}
		return ""
	}
	if inc.Quoted {
		// For quoted includes, check relative to the including file's directory first.
		if resolved := statIncludeCandidate(includingFileDir, inc.Path); resolved != "" {
			return resolved
		}
	}
	// Check search paths (both quoted and angle bracket includes).
	for _, dir := range searchPaths {
		if resolved := statIncludeCandidate(dir, inc.Path); resolved != "" {
			return resolved
		}
	}
	return ""
}

// statIncludeCandidate constructs a candidate path from dir and relPath,
// stats it, and returns the resolved absolute path if it's a regular file.
// It avoids filepath.Join to preserve ".." components so that the OS can
// correctly resolve them through symlinks.
func statIncludeCandidate(dir, relPath string) string {
	// Use path concatenation instead of filepath.Join when ".." is present,
	// because filepath.Join calls filepath.Clean which resolves ".."
	// textually. The OS needs to see the ".." to resolve symlinks correctly:
	// e.g. "symlink/../foo.h" must follow the symlink first, then go up.
	var candidate string
	if strings.Contains(relPath, "..") {
		candidate = dir + string(filepath.Separator) + relPath
	} else {
		candidate = filepath.Join(dir, relPath)
	}

	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}

	// Resolve to a clean absolute path via EvalSymlinks.
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved
	}
	return abs
}

// extractSearchPaths parses -I, -iquote, -isystem, and -L flags from a
// compiler/linker command and returns the directory paths they reference.
func extractSearchPaths(args []string) []string {
	var paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-I" || arg == "-iquote" || arg == "-isystem" || arg == "-L":
			if i+1 < len(args) {
				i++
				paths = append(paths, args[i])
			}
		case strings.HasPrefix(arg, "-I"):
			paths = append(paths, strings.TrimPrefix(arg, "-I"))
		case strings.HasPrefix(arg, "-L"):
			paths = append(paths, strings.TrimPrefix(arg, "-L"))
		}
	}
	return paths
}

// extractForceIncludes parses -include flags from a compiler command.
// These specify files to be force-included before each source file.
func extractForceIncludes(args []string) []string {
	var paths []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-include" && i+1 < len(args) {
			i++
			paths = append(paths, args[i])
		}
	}
	return paths
}

func isScannable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return scannableExtensions[ext]
}

// expandPaths stats each candidate path and expands it:
//   - Regular files: adds the file + sibling files in the same directory
//   - Directories: if walkDirs is true, recursively collects all files;
//     otherwise adds the directory path as-is
//
// Results are deduplicated.
func expandPaths(candidates []string, walkDirs bool) ([]string, error) {
	var mu sync.Mutex
	var paths []string
	seen := make(map[string]struct{})
	siblingDirsSeen := make(map[string]struct{})

	addPath := func(path string) {
		mu.Lock()
		defer mu.Unlock()

		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	addSiblingDir := func(dir string) bool {
		mu.Lock()
		defer mu.Unlock()

		_, alreadyExpanded := siblingDirsSeen[dir]
		if !alreadyExpanded {
			siblingDirsSeen[dir] = struct{}{}
		}
		return alreadyExpanded
	}

	var g errgroup.Group
	for _, candidate := range candidates {
		g.Go(func() error {
			info, err := os.Stat(candidate)
			if err != nil {
				return nil
			}

			if info.IsDir() {
				if walkDirs {
					var walked []string
					err = filepath.WalkDir(candidate, func(p string, d fs.DirEntry, err error) error {
						if err != nil || d.IsDir() {
							return nil
						}
						walked = append(walked, p)
						return nil
					})
					if err != nil {
						return err
					}
					for _, p := range walked {
						addPath(p)
					}
				} else {
					addPath(candidate)
				}
			} else {
				var siblings []string
				dir := filepath.Dir(candidate)

				alreadyExpanded := addSiblingDir(dir)

				if !alreadyExpanded {
					entries, err := os.ReadDir(dir)
					if err == nil {
						for _, entry := range entries {
							if !entry.IsDir() {
								siblings = append(siblings, filepath.Join(dir, entry.Name()))
							}
						}
					}
				}

				addPath(candidate)
				for _, s := range siblings {
					addPath(s)
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return paths, err
	}

	return paths, nil
}

// walkDirectoryFiles stats each candidate to confirm it's a directory,
// then recursively walks it collecting all non-directory entries.
// Results are deduplicated.
func walkDirectoryFiles(candidates []string) ([]string, error) {
	var mu sync.Mutex
	var files []string
	seen := make(map[string]struct{})

	var g errgroup.Group
	for _, dir := range candidates {
		g.Go(func() error {
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return nil
			}
			var walked []string
			err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				walked = append(walked, path)
				return nil
			})
			if err != nil {
				return err
			}
			mu.Lock()
			for _, p := range walked {
				if _, ok := seen[p]; !ok {
					seen[p] = struct{}{}
					files = append(files, p)
				}
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return files, err
	}

	return files, nil
}

// extractCommandCandidatePaths extracts absolute paths from the command string
// that reference locations under the given root. For each whitespace-separated
// token, it finds substrings matching root and trims trailing quotes.
// Results are deduplicated. No filesystem access is performed.
func extractCommandCandidatePaths(command, root string) []string {
	var paths []string
	seen := make(map[string]struct{})

	for _, token := range strings.Fields(command) {
		idx := strings.Index(token, root)
		if idx < 0 {
			continue
		}
		path := strings.TrimRight(token[idx:], `"'`)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	return paths
}

// ExtractCommandReferencedPaths finds absolute paths in the command string
// that reference existing files or directories under the given root. These
// paths may not be declared as edge inputs but need to exist on the remote
// executor for the command to succeed (e.g. cmake scripts, data files).
//
// For regular files, sibling files in the same directory are also included
// since commands often reference files that depend on neighbors (e.g. cmake
// scripts that include() other modules from the same directory).
func ExtractCommandReferencedPaths(command, root string) ([]string, error) {
	candidates := extractCommandCandidatePaths(command, root)
	return expandPaths(candidates, false)
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

// extractSearchDirectoryCandidates parses -I, -L, -isystem, and -iquote flags
// from the command string, resolves relative paths to absolute, and filters to
// under root. Results are deduplicated. No filesystem access is performed.
func extractSearchDirectoryCandidates(command, root string) []string {
	args, err := shlex.Split(command)
	if err != nil {
		return nil
	}

	raw := extractSearchPaths(args)

	seen := make(map[string]struct{})
	var dirs []string
	for _, dir := range raw {
		if !filepath.IsAbs(dir) {
			abs, err := filepath.Abs(dir)
			if err != nil {
				continue
			}
			dir = abs
		}
		if !strings.HasPrefix(dir, root+"/") && dir != root {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}

	return dirs
}

// ExtractSearchDirectoryContents finds -I, -isystem, -iquote, and -L
// directory flags in the command string and returns all files found
// recursively within those directories that are under the given root.
// This allows remote execution of commands that search these directories
// at runtime (e.g. tablegen processing -I paths for .td includes).
func ExtractSearchDirectoryContents(command, root string) ([]string, error) {
	dirs := extractSearchDirectoryCandidates(command, root)
	return walkDirectoryFiles(dirs)
}

// extractRelativeDotDotCandidates finds non-absolute tokens containing ".."
// components, strips flag prefixes and shell operators, resolves via filepath.Abs,
// and filters to under root. Results are deduplicated. No filesystem access is performed.
func extractRelativeDotDotCandidates(command, root string) []string {
	var paths []string
	seen := make(map[string]struct{})

	for _, token := range strings.Fields(command) {
		if filepath.IsAbs(token) || !strings.Contains(token, "..") {
			continue
		}

		path := token
		for _, prefix := range []string{"-I", "-L"} {
			if strings.HasPrefix(token, prefix) && len(token) > len(prefix) {
				path = token[len(prefix):]
				break
			}
		}
		path = strings.TrimRight(path, ";&|\"'")

		if !strings.Contains(path, "..") {
			continue
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}

		if !strings.HasPrefix(abs, root+"/") && abs != root {
			continue
		}

		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		paths = append(paths, abs)
	}

	return paths
}

// ExtractRelativeDotDotContents finds relative paths containing ".." components
// in a command string, resolves them to absolute paths, and returns all files
// within resolved directories (or the resolved file and its siblings for
// regular files). This handles commands like "cd ../../tools/icu" that
// reference directories outside the build output tree but still within the
// project root. Sibling files are included for regular files because scripts
// commonly import neighboring modules (e.g. Python's import of pdl.py from
// check_protocol_compatibility.py).
func ExtractRelativeDotDotContents(command, root string) ([]string, error) {
	candidates := extractRelativeDotDotCandidates(command, root)
	return expandPaths(candidates, true)
}

// extractCdRelativeCandidates finds "cd <target>" in the command, then for
// each non-flag/non-operator/non-absolute token, resolves it against the cd
// target directory. Filters to under root and deduplicates.
// No filesystem access is performed.
func extractCdRelativeCandidates(command, root string) []string {
	fields, err := shlex.Split(command)
	if err != nil {
		return nil
	}
	var cdTarget string
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "cd" {
			cdTarget = strings.TrimRight(fields[i+1], ";&|")
			break
		}
	}
	if cdTarget == "" || !filepath.IsAbs(cdTarget) {
		return nil
	}
	if !strings.HasPrefix(cdTarget, root+"/") && cdTarget != root {
		return nil
	}

	var paths []string
	seen := make(map[string]struct{})

	for _, token := range fields {
		if filepath.IsAbs(token) {
			continue
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		if token == "&&" || token == "||" || token == "|" || token == ";" || token == "cd" {
			continue
		}
		path := strings.TrimRight(token, ";&|")
		resolved := filepath.Clean(filepath.Join(cdTarget, path))

		if !strings.HasPrefix(resolved, root+"/") && resolved != root {
			continue
		}

		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		paths = append(paths, resolved)
	}

	return paths
}

// ExtractCdRelativePaths handles commands that start with "cd <dir> &&" by
// resolving relative path-like tokens against the cd target directory. This
// covers commands like "cd /project/root && python3 scripts/foo.py args..."
// where the script path is relative to the cd target, not to the build
// directory. Sibling files are included for regular files because scripts
// commonly import neighboring modules (e.g. Python imports).
func ExtractCdRelativePaths(command, root string) ([]string, error) {
	candidates := extractCdRelativeCandidates(command, root)
	return expandPaths(candidates, true)
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
