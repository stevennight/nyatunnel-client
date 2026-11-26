//go:build darwin

package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFindAppBundle(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "NyaTunnel.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findAppBundle(dir, "NyaTunnel.app")
	if err != nil {
		t.Fatalf("findAppBundle preferred: %v", err)
	}
	if got != app {
		t.Fatalf("got %q, want %q", got, app)
	}

	got, err = findAppBundle(dir, "")
	if err != nil {
		t.Fatalf("findAppBundle fallback: %v", err)
	}
	if got != app {
		t.Fatalf("got %q, want %q", got, app)
	}
}

func TestReplaceAppBundle(t *testing.T) {
	root := t.TempDir()
	oldApp := filepath.Join(root, "NyaTunnel.app")
	newApp := filepath.Join(root, "New.app")

	for _, p := range []struct {
		dir  string
		body string
	}{
		{oldApp, "old"},
		{newApp, "new"},
	} {
		binDir := filepath.Join(p.dir, "Contents", "MacOS")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "marker"), []byte(p.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := replaceAppBundle(context.Background(), newApp, oldApp); err != nil {
		t.Fatalf("replaceAppBundle: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(oldApp, "Contents", "MacOS", "marker"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Fatalf("got %q, want %q", string(b), "new")
	}
}
