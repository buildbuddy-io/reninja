package project_root_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/project_root"
)

func TestDetectFrom_NoMarkers(t *testing.T) {
	dir := t.TempDir()
	got := project_root.WalkUpDirsToFindRoot(dir)
	if got != dir {
		t.Errorf("project_root.WalkUpDirsToFindRoot(%q) = %q, want %q (fallback to startDir)", dir, got, dir)
	}
}

func TestDetectFrom_GitAtCWD(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := project_root.WalkUpDirsToFindRoot(dir)
	if got != dir {
		t.Errorf("project_root.WalkUpDirsToFindRoot(%q) = %q, want %q", dir, got, dir)
	}
}

func TestDetectFrom_GitAboveCWD(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := project_root.WalkUpDirsToFindRoot(sub)
	if got != root {
		t.Errorf("project_root.WalkUpDirsToFindRoot(%q) = %q, want %q", sub, got, root)
	}
}

func TestDetectFrom_OutermostWins(t *testing.T) {
	// Create structure:
	//   root/.gclient
	//   root/inner/.git
	//   root/inner/deep/
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gclient"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(inner, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got := project_root.WalkUpDirsToFindRoot(deep)
	if got != root {
		t.Errorf("project_root.WalkUpDirsToFindRoot(%q) = %q, want %q (outermost marker)", deep, got, root)
	}
}

func TestDetectFrom_GclientMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gclient"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got := project_root.WalkUpDirsToFindRoot(sub)
	if got != root {
		t.Errorf("project_root.WalkUpDirsToFindRoot(%q) = %q, want %q", sub, got, root)
	}
}
