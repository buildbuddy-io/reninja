package build_metadata

import (
	"os"
	"os/exec"
	"strings"
)

var isGitRepo bool

func init() {
	if skip := os.Getenv("NINJA_SKIP_METADATA"); skip != "" {
		return
	}
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	isGitRepo = cmd.Run() == nil
}

// GetMetadata looks in the environment and attempts to set the following
// fields: GIT_BRANCH, GIT_TREE_STATUS, COMMIT_SHA, REPO_URL.
func GetMetadata() map[string]string {
	r := make(map[string]string, 0)

	// GIT BRANCH
	if gitBranch := os.Getenv("GIT_BRANCH"); gitBranch == "" {
		if isGitRepo {
			cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
			if stdout, err := cmd.CombinedOutput(); err == nil {
				gitBranch = string(stdout)
			}
		}
		if gitBranch != "" {
			r["GIT_BRANCH"] = strings.TrimSpace(gitBranch)
		}
	}

	// GIT_TREE_STATUS
	if gitTreeStatus := os.Getenv("GIT_TREE_STATUS"); gitTreeStatus == "" {
		if isGitRepo {
			cmd := exec.Command("git", "diff-index", "--quiet", "HEAD")
			if _, err := cmd.CombinedOutput(); err == nil {
				gitTreeStatus = "Clean"
			} else {
				gitTreeStatus = "Modified"
			}
		}
		if gitTreeStatus != "" {
			r["GIT_TREE_STATUS"] = gitTreeStatus
		}
	}

	// COMMIT_SHA
	if commitSHA := os.Getenv("COMMIT_SHA"); commitSHA == "" {
		if isGitRepo {
			cmd := exec.Command("git", "rev-parse", "HEAD")
			if stdout, err := cmd.CombinedOutput(); err == nil {
				commitSHA = string(stdout)
			}
		}
		if commitSHA != "" {
			r["COMMIT_SHA"] = commitSHA
		}
	}

	if repoURL := os.Getenv("REPO_URL"); repoURL == "" {
		if isGitRepo {
			cmd := exec.Command("git", "config", "--get", "remote.origin.url")
			if stdout, err := cmd.CombinedOutput(); err == nil {
				repoURL = string(stdout)
			}
		}
		if repoURL != "" {
			r["REPO_URL"] = repoURL
		}
	}
	return r
}
