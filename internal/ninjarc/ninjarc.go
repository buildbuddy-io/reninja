package ninjarc

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/project_root"
	"github.com/buildbuddy-io/reninja/internal/util"
	"github.com/google/shlex"
)

const (
	workspacePrefix = `%workspace%/`
)

var (
	ignoreConfig = flag.Bool("norc", false, "ignore all RC files (including ones in default locations)")
	ninjaRCFile  = flag.String("ninjarc", "", "path to a ninjarc file to parse")
	configFlag   = flag.String("config", "", "ninjarc configuration to apply")

	rcFileLocations = []string{".ninjarc", "%workspace%/.ninjarc", "~/.ninjarc", "/etc/.ninjarc"}
)

// Package ninjarc defines a simple RC format to configure ninja flags without
// having to specify them on the command line for every invocation.
//
// The format is very simple. Here is an example:
//
// build:local --bes_backend="grpc://localhost:1985"
// build:local --remote_cache="grpc://localhost:1985"
// build:local --results_url="http://localhost:8080"
//
// This config specifies that for "build" commands, when the --config flag value
// is "local", the `--bes_backend`, `--remote_cache` and `--results_url` flags
// will be set.
//
// Here's another example:
//
// clean --bes_backend="grpc://localhost:1985"
// clean --remote_cache="grpc://localhost:1985"
// clean --results_url="http://localhost:8080"
//
// This config specifies that for "clean" commands, regardless of config flag
// value, the `--bes_backend`, `--remote_cache` and `--results_url` flags will
// be set.

// RcRule is a rule parsed from a ninjarc file.
type RcRule struct {
	Phase string

	// Make Config a pointer to a string so we can distinguish default configs
	// from configs with blank names.
	Config *string

	// Tokens contains the raw (non-canonicalized) tokens in the rule.
	Tokens []string
}

// resolvePath resolves a ninjarc path by expanding %workspace%/ and ~
// prefixes, cleaning the path, and stripping the leading "/" so it is
// suitable for use with fs.FS (which requires unrooted paths).
func resolvePath(workspaceDir, path string) string {
	if strings.HasPrefix(path, workspacePrefix) {
		path = filepath.Join(workspaceDir, path[len(workspacePrefix):])
	}

	if strings.HasPrefix(path, "~") {
		currentUser, err := user.Current()
		if err == nil {
			path = strings.Replace(path, "~", currentUser.HomeDir, 1)
		}
	}

	path = filepath.Clean(path)
	// fs.FS paths must be unrooted — strip leading "/".
	path = strings.TrimPrefix(path, "/")
	return path
}

// AppendRcRulesFromFile reads and lexes the provided rc file and appends the
// args to the provided configs based on the detected phase and name.
//
// configs is a map keyed by config name where the values are maps keyed by
// phase name where the values are lists containing all the rules for that
// config in the order they are encountered.
func AppendRcRulesFromFile(fsys fs.FS, workspaceDir string, path string, namedConfigs map[string]map[string][]string, defaultConfig map[string][]string, importStack []string, optional bool) error {
	if slices.Contains(importStack, path) {
		return fmt.Errorf("circular import detected: %s -> %s", strings.Join(importStack, " -> "), path)
	}
	importStack = append(importStack, path)
	file, err := fsys.Open(path)
	if err != nil {
		if optional {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// Handle line continuations (lines can end with "\" to effectively
		// continue the same line)
		for strings.HasSuffix(line, `\`) && scanner.Scan() {
			line = line[:len(line)-1] + scanner.Text()
		}
		lexer := shlex.NewLexer(strings.NewReader(line))
		tokens := []string{}
		for {
			token, err := lexer.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("Error parsing ninjarc: %s\nFailed to lex '%s'.", err, line)
			}
			tokens = append(tokens, token)
		}
		if len(tokens) == 0 {
			// blank line
			continue
		}
		if tokens[0] == "import" || tokens[0] == "try-import" {
			isOptional := tokens[0] == "try-import"
			importPath := strings.TrimSpace(strings.TrimPrefix(line, tokens[0]))
			if err := AppendRcRulesFromFile(fsys, workspaceDir, resolvePath(workspaceDir, importPath), namedConfigs, defaultConfig, importStack, isOptional); err != nil {
				return err
			}
			continue
		}

		rule, err := parseRcRule(line)
		if err != nil {
			util.Errorf("Error parsing ninjarc option: %s", err.Error())
			continue
		}
		if rule == nil {
			continue
		}
		if rule.Config == nil {
			defaultConfig[rule.Phase] = append(defaultConfig[rule.Phase], rule.Tokens...)
			continue
		}

		config, ok := namedConfigs[*rule.Config]
		if !ok {
			config = make(map[string][]string)
			namedConfigs[*rule.Config] = config
		}
		config[rule.Phase] = append(config[rule.Phase], rule.Tokens...)
	}
	return scanner.Err()
}

func parseRcRule(line string) (*RcRule, error) {
	tokens, err := shlex.Split(line)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("unexpected empty line")
	}
	if len(tokens) == 1 {
		// ninja ignores .ninjarc lines consisting of a single shlex token
		return nil, nil
	}
	phase := tokens[0]
	var configName *string
	if colonIndex := strings.Index(tokens[0], ":"); colonIndex != -1 {
		phase = tokens[0][:colonIndex]
		v := tokens[0][colonIndex+1:]
		configName = &v
	}

	return &RcRule{
		Phase:  phase,
		Config: configName,
		Tokens: tokens[1:],
	}, nil
}

type RCConfig struct {
	namedRcRules   map[string]map[string][]string
	defaultRcRules map[string][]string
}

func (c *RCConfig) Apply(toolName string, config string, flagSet *flag.FlagSet) {
	expandedValues := make([]string, 0)
	seenConfigs := make(map[string]struct{}, 0)
	tools := []string{"common", toolName}

	var expandRules func(config string)
	addOptionToExpanded := func(opt string) {
		key, val, ok := strings.Cut(strings.TrimLeft(opt, "-"), "=")
		// if we encounter a flag like "--config=dev", then
		// expand the rules from "dev".
		if ok && key == "config" {
			expandRules(val)
		}
		expandedValues = append(expandedValues, opt)
	}

	expandRules = func(config string) {
		if _, ok := seenConfigs[config]; ok {
			return
		}
		seenConfigs[config] = struct{}{}
		optsByTool := c.namedRcRules[config]

		for _, toolName := range tools {
			for _, opt := range optsByTool[toolName] {
				addOptionToExpanded(opt)
			}
		}
		delete(seenConfigs, config)
	}

	for _, toolName := range tools {
		for _, opt := range c.defaultRcRules[toolName] {
			addOptionToExpanded(opt)
		}
	}

	expandRules(config)
	flagSet.Parse(expandedValues)
}

// ParseRCFiles parses the provided rc files in the given workspace into Configs
// and returns a map of the named configs as well as the default (unnamed)
// Config.
func ParseRCFiles(fsys fs.FS, workspaceDir string, filePaths ...string) (*RCConfig, error) {
	seen := make(map[string]struct{}, len(filePaths))
	namedRcRules := map[string]map[string][]string{}
	defaultRcRules := map[string][]string{}

	for _, filePath := range filePaths {
		r := resolvePath(workspaceDir, filePath)
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}

		err := AppendRcRulesFromFile(fsys, workspaceDir, r, namedRcRules, defaultRcRules, nil /*=importStack*/, true)
		if err != nil {
			return nil, err
		}
	}

	return &RCConfig{
		namedRcRules:   namedRcRules,
		defaultRcRules: defaultRcRules,
	}, nil

}

func ParseAndApplyRCFilesToFlags(toolName string) error {
	flag.Parse() // Parse flags once to ensure we have --config, --ninjarc, and --norc

	if *ignoreConfig {
		return nil
	}

	filePaths := rcFileLocations
	if *ninjaRCFile != "" {
		if !disk.FileExists(*ninjaRCFile) {
			util.Fatalf("ninjarc file '%s' not found", *ninjaRCFile)
		}
		filePaths = append(filePaths, *ninjaRCFile)
	}

	workspaceDir := project_root.Root()
	rcConfig, err := ParseRCFiles(os.DirFS("/"), workspaceDir, rcFileLocations...)
	if err != nil {
		return err
	}

	rcConfig.Apply(toolName, *configFlag, flag.CommandLine)
	return nil
}
