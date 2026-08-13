package gendocs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"microbe/internal/cmd"
	"microbe/internal/gendocs"
)

func TestWriteMarkdownGeneratesTree(t *testing.T) {
	dir := t.TempDir()
	root := cmd.NewRootCmd()
	if err := gendocs.WriteMarkdown(root, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"microbe.md", "microbe_up.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "microbe.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), root.Short) {
		t.Errorf("microbe.md missing root Short text %q", root.Short)
	}
}

func TestWriteManGeneratesTree(t *testing.T) {
	dir := t.TempDir()
	root := cmd.NewRootCmd()
	if err := gendocs.WriteMan(root, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"microbe.1", "microbe-up.1"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
		if !strings.Contains(string(b), ".TH") {
			t.Errorf("%s missing .TH header", name)
		}
	}
}
