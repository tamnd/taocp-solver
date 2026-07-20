package evaluation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMapWinnerCorrectsForOrder(t *testing.T) {
	t.Parallel()
	if got := mapWinner("A", false); got != "new" {
		t.Fatalf("got %q", got)
	}
	if got := mapWinner("B", true); got != "new" {
		t.Fatalf("got %q", got)
	}
}

func TestParseWinner(t *testing.T) {
	t.Parallel()
	if got := parseWinner("WINNER: B"); got != "B" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadBrainSolution(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "vol1", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	value := "---\ntitle: Test\n---\n\nExercise metadata.\n\n---\n\nActual solution.\n"
	if err := os.WriteFile(filepath.Join(dir, "01.md"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := LoadBrainSolution(root, 1, "1.1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Actual solution." {
		t.Fatalf("got %q", got)
	}
}
