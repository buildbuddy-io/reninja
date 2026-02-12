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
