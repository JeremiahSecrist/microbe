package flakegen

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteStackWritesAllArtifacts(t *testing.T) {
	dir := t.TempDir()
	st := mustStack(t, fixtureConfig())

	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{
		"flake.nix",
		"generated.json",
		"modules/renderer.nix",
		"modules/guest-base.nix",
		"modules/db.nix",
		"modules/jump.nix",
		"modules/web.nix",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing artifact %s: %v", f, err)
		}
	}

	gen, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "generated.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != gen {
		t.Errorf("generated.json differs from RenderGenerated()")
	}
}

// TestWriteStackPreservesMtimeOnUnchangedContent is the whole point of the
// write-if-changed render: a file whose rendered content didn't change
// between two WriteStack calls must keep its original mtime, so nix's flake
// eval cache (keyed by content/tree hash) can hit on it instead of treating
// every `up`/`build` as a fresh input.
func TestWriteStackPreservesMtimeOnUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	st := mustStack(t, fixtureConfig())

	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}
	unchanged := filepath.Join(dir, "modules", "guest-base.nix")
	before, err := os.Stat(unchanged)
	if err != nil {
		t.Fatal(err)
	}

	// Force the mtime backward so a spurious rewrite would be detectable
	// even on filesystems with coarse mtime resolution.
	older := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(unchanged, older, older); err != nil {
		t.Fatal(err)
	}

	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(older) {
		t.Errorf("mtime changed for unchanged content: got %v, want %v", after.ModTime(), older)
	}
}
