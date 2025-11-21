package ninjarc

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/buildbuddy-io/gin/internal/util"
	"github.com/google/shlex"
)

const (
	workspacePrefix = `%workspace%/`
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

// RcRule is a rule parsed from a bazelrc file.
type RcRule struct {
	Phase   string
	Config  string
	Options []string
}

func (r *RcRule) ApplyToFlags() {
	for _, opt := range r.Options {
		key, val, ok := strings.Cut(strings.TrimLeft(opt, "-"), "=")
		if ok {
			flag.Set(key, val)
		} else {
			util.Infof("Skipping unknown opt %q\n", opt)
		}
	}
}

func appendRcRulesFromImport(workspaceDir, path string, opts []*RcRule, optional bool, importStack []string) ([]*RcRule, error) {
	if strings.HasPrefix(path, workspacePrefix) {
		path = filepath.Join(workspaceDir, path[len(workspacePrefix):])
	}

	file, err := os.Open(path)
	if err != nil {
		if optional {
			return opts, nil
		}
		return nil, err
	}
	defer file.Close()
	return appendRcRulesFromFile(workspaceDir, file, opts, importStack)
}

func appendRcRulesFromFile(workspaceDir string, f *os.File, opts []*RcRule, importStack []string) ([]*RcRule, error) {
	rpath, err := realpath(f.Name())
	if err != nil {
		return nil, fmt.Errorf("could not determine real path of bazelrc file: %s", err)
	}
	for _, path := range importStack {
		if path == rpath {
			return nil, fmt.Errorf("circular import detected: %s -> %s", strings.Join(importStack, " -> "), rpath)
		}
	}
	importStack = append(importStack, rpath)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Handle line continuations (lines can end with "\" to effectively
		// continue the same line)
		for strings.HasSuffix(line, `\`) && scanner.Scan() {
			line = line[:len(line)-1] + scanner.Text()
		}

		line = stripCommentsAndWhitespace(line)

		tokens := strings.Fields(line)
		if len(tokens) == 0 {
			// blank line
			continue
		}
		if tokens[0] == "import" || tokens[0] == "try-import" {
			isOptional := tokens[0] == "try-import"
			path := strings.TrimSpace(strings.TrimPrefix(line, tokens[0]))
			opts, err = appendRcRulesFromImport(workspaceDir, path, opts, isOptional, importStack)
			if err != nil {
				return nil, err
			}
			continue
		}

		opt, err := parseRcRule(line)
		if err != nil {
			util.Infof("Error parsing bazelrc option: %s", err.Error())
			continue
		}
		opts = append(opts, opt)
	}
	return opts, scanner.Err()
}

func realpath(path string) (string, error) {
	directPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(directPath)
}

func stripCommentsAndWhitespace(line string) string {
	index := strings.Index(line, "#")
	if index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}

func parseRcRule(line string) (*RcRule, error) {
	tokens, err := shlex.Split(line)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("unexpected empty line")
	}
	if strings.HasPrefix(tokens[0], "-") {
		return &RcRule{
			Phase:   "common",
			Options: tokens,
		}, nil
	}
	if !strings.Contains(tokens[0], ":") {
		return &RcRule{
			Phase:   tokens[0],
			Options: tokens[1:],
		}, nil
	}
	phaseConfig := strings.Split(tokens[0], ":")
	if len(phaseConfig) != 2 {
		return nil, fmt.Errorf("invalid ninjarc syntax: %s", phaseConfig)
	}
	return &RcRule{
		Phase:   phaseConfig[0],
		Config:  phaseConfig[1],
		Options: tokens[1:],
	}, nil
}

func ParseRCFiles(workspaceDir string, filePaths ...string) ([]*RcRule, error) {
	options := make([]*RcRule, 0)
	for _, filePath := range filePaths {
		if strings.HasPrefix(filePath, "~") {
			currentUser, err := user.Current()
			if err != nil {
				return nil, err
			}
			filePath = strings.Replace(filePath, "~", currentUser.HomeDir, 1)
		}
		file, err := os.Open(filePath)
		if err != nil {
			continue
		}
		defer file.Close()
		options, err = appendRcRulesFromFile(workspaceDir, file, options, nil /*=importStack*/)
		if err != nil {
			return nil, err
		}
	}
	return options, nil
}
