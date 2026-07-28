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

func TestParseAssessment(t *testing.T) {
	t.Parallel()
	got := ParseAssessment("TRUTH: TRUE\nCOMPLETE: YES\nSELF_CONTAINED: YES\nHUMAN_READABLE: YES\nVERIFIABLE: NO")
	if !got.HasTruth || !got.HasQuality || !got.Truth || !got.Complete || !got.SelfContained || !got.HumanReadable || got.Verifiable {
		t.Fatalf("assessment = %+v", got)
	}
}

func TestSelectedCandidate(t *testing.T) {
	t.Parallel()
	if got := SelectedCandidate("reason\nSELECTED: 3"); got != 3 {
		t.Fatalf("selected = %d", got)
	}
	if got := SelectedCandidate("SELECTED: 0"); got != 0 {
		t.Fatalf("invalid selected = %d", got)
	}
}

// A weaker model bolds, bullets, or punctuates the line the prompt asked it to
// write bare. It still said what it decided, and a solve is too expensive to
// throw away over a pair of asterisks.
func TestADecoratedDecisionLineStillCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		review string
	}{
		{"bare", "TRUTH: TRUE\nCOMPLETE: YES\nSELF_CONTAINED: YES\nHUMAN_READABLE: YES\nVERIFIABLE: YES"},
		{"bold", "**TRUTH: TRUE**\n**COMPLETE: YES**\n**SELF_CONTAINED: YES**\n**HUMAN_READABLE: YES**\n**VERIFIABLE: YES**"},
		{"bulleted", "- TRUTH: TRUE\n- COMPLETE: YES\n- SELF_CONTAINED: YES\n- HUMAN_READABLE: YES\n- VERIFIABLE: YES"},
		{"full stops", "TRUTH: TRUE.\nCOMPLETE: YES.\nSELF_CONTAINED: YES.\nHUMAN_READABLE: YES.\nVERIFIABLE: YES."},
		{"bold value", "TRUTH: **TRUE**\nCOMPLETE: **YES**\nSELF_CONTAINED: **YES**\nHUMAN_READABLE: **YES**\nVERIFIABLE: **YES**"},
		{"quoted heading", "> TRUTH: TRUE\n#### COMPLETE: YES\n> SELF_CONTAINED: YES\n> HUMAN_READABLE: YES\n> VERIFIABLE: YES"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			got := ParseAssessment(item.review)
			if !got.HasTruth || !got.HasQuality {
				t.Fatalf("assessment = %+v, want every field read", got)
			}
			if !got.Truth || !got.Complete || !got.SelfContained || !got.HumanReadable || !got.Verifiable {
				t.Fatalf("assessment = %+v, want every field affirmative", got)
			}
		})
	}
}

func TestADecoratedVerdictScoreAndChoiceStillCount(t *testing.T) {
	t.Parallel()
	if got := Verdict("**VERDICT: PASS**"); got != "PASS" {
		t.Errorf("Verdict = %q, want PASS", got)
	}
	if got := Verdict("- VERDICT: FAIL."); got != "FAIL" {
		t.Errorf("Verdict = %q, want FAIL", got)
	}
	if got := Score("**SCORE: 6/7**"); got != 6 {
		t.Errorf("Score = %d, want 6", got)
	}
	if got := Score("SCORE: 7 / 7."); got != 7 {
		t.Errorf("Score = %d, want 7", got)
	}
	if got := SelectedCandidate("**SELECTED: 2**"); got != 2 {
		t.Errorf("SelectedCandidate = %d, want 2", got)
	}
}

// The decision has to be the whole line. A verdict mentioned in passing is not
// a verdict, and a gate that read one would not be a gate.
func TestAVerdictMentionedInPassingIsNotAVerdict(t *testing.T) {
	t.Parallel()
	for _, review := range []string{
		"If the argument held I would write VERDICT: PASS here.",
		"The reviewer should end with VERDICT: PASS on its own line.",
	} {
		if got := Verdict(review); got != "UNKNOWN" {
			t.Errorf("Verdict(%q) = %q, want UNKNOWN", review, got)
		}
	}
	if got := ParseAssessment("I cannot say TRUTH: TRUE without more work."); got.HasTruth {
		t.Error("a truth decision was read out of a sentence")
	}
}
