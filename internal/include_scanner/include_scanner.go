package include_scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
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
		path := token[idx:]

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
				if !entry.IsDir() {
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
