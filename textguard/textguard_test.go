package textguard

import "testing"

func TestCleanSolution(t *testing.T) {
	t.Parallel()
	got, err := CleanSolution("Answer \u2014 checked.\n\n---\n\n$1+1=2$\f")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Answer , checked.\n\n$1+1=2$" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanSolutionRejectsBrandLeak(t *testing.T) {
	t.Parallel()
	if _, err := CleanSolution("As an AI language model, I cannot solve this."); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	if got := Verdict("## Verdict\nVERDICT: PASS"); got != "PASS" {
		t.Fatalf("got %q", got)
	}
	if got := Verdict("VERDICT: PARTIAL"); got != "UNKNOWN" {
		t.Fatalf("got %q", got)
	}
}

func TestScore(t *testing.T) {
	t.Parallel()
	if got := Score("SCORE: 6/7"); got != 6 {
		t.Fatalf("got %d", got)
	}
	if got := Score("no score"); got != -1 {
		t.Fatalf("got %d", got)
	}
}
