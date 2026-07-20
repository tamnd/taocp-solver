package result

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/exercise"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	store := Store{Root: t.TempDir()}
	value := Result{
		ID: "1.1-1", Exercise: exercise.Exercise{SectionID: "1.1", Number: 1},
		Solution: "Proof.", Verdict: "PASS", Verified: true,
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("1.1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Solution != "Proof." || !loaded.Verified {
		t.Fatalf("loaded = %+v", loaded)
	}
	markdown, err := os.ReadFile(filepath.Join(store.Root, "1.1", "01.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "# TAOCP 1.1 Exercise 1\n\nProof.") {
		t.Fatalf("markdown = %q", markdown)
	}
}
