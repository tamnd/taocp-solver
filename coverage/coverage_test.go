package coverage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/result"
)

// tree builds a source repository, a result store, and a brain checkout in a
// temporary directory, and returns their roots in that order.
func tree(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "solutions")
	brain := filepath.Join(root, "brain")
	return source, output, brain
}

func writeExercise(t *testing.T, source, section, name string) {
	t.Helper()
	dir := filepath.Join(source, "content", exercise.VolumeDir(section), "exercises", section)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := "---\ntitle: \"Exercise\"\nrating: 20\n---\n\nProve it.\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeResult(t *testing.T, output, section string, number int, solution string) {
	t.Helper()
	store := result.Store{Root: output}
	path := store.JSONPath(section, number)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Result{
		Exercise: exercise.Exercise{SectionID: section, Number: number},
		Solution: solution,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePublished(t *testing.T, brain, section string, number int, verified bool) {
	t.Helper()
	volume := fmt.Sprintf("vol%d", volumeOf(section))
	dir := filepath.Join(brain, "content", "en", "practice", "maths", "taocp", volume, section)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf("---\ntitle: \"Exercise %d\"\nverified: %t\n---\n\nA proof.\n", number, verified)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.md", number)), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

func volumeOf(section string) int {
	switch strings.SplitN(section, ".", 2)[0] {
	case "1", "2":
		return 1
	case "3", "4":
		return 2
	case "5", "6":
		return 3
	default:
		return 4
	}
}

func TestRepeatOccurrencesAreOneExercise(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for _, name := range []string{"01.md", "02.md", "02_01.md", "02_02.md", "03_01.md"} {
		writeExercise(t, source, "1.1", name)
	}
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 3 {
		t.Fatalf("total = %d, want 3 exercises from five files", report.Total)
	}
	if report.Missing != 3 || report.Solved != 0 {
		t.Fatalf("missing = %d, solved = %d", report.Missing, report.Solved)
	}
}

func TestAnEmptySolutionIsNotSolved(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	writeExercise(t, source, "1.1", "02.md")
	writeExercise(t, source, "1.1", "03.md")
	writeResult(t, output, "1.1", 1, "A real proof.")
	writeResult(t, output, "1.1", 2, "   \n\t\n")
	// A result file the store cannot decode is not a solve either.
	store := result.Store{Root: output}
	if err := os.WriteFile(store.JSONPath("1.1", 3), []byte("{truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Solved != 1 || report.Missing != 2 {
		t.Fatalf("solved = %d, missing = %d, want 1 and 2", report.Solved, report.Missing)
	}
	if queue := report.Queue(); len(queue) != 2 || queue[0].Number != 2 || queue[1].Number != 3 {
		t.Fatalf("queue = %v", queue)
	}
}

func TestPublishedCountingReadsVerifiedAndToleratesNoFrontmatter(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for number := 1; number <= 3; number++ {
		writeExercise(t, source, "1.1", fmt.Sprintf("%02d.md", number))
	}
	writePublished(t, brain, "1.1", 1, true)
	writePublished(t, brain, "1.1", 2, false)
	bare := filepath.Join(brain, "content", "en", "practice", "maths", "taocp", "vol1", "1.1", "03.md")
	if err := os.WriteFile(bare, []byte("Just a proof, no frontmatter.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Published != 3 || report.Verified != 1 {
		t.Fatalf("published = %d, verified = %d, want 3 and 1", report.Published, report.Verified)
	}
}

func TestAPublishedPageWithNoResultIsImportedNotMissing(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for number := 1; number <= 3; number++ {
		writeExercise(t, source, "1.1", fmt.Sprintf("%02d.md", number))
	}
	// One solved and published, one published by an earlier tool that kept no
	// result, one nowhere.
	writeResult(t, output, "1.1", 1, "A proof.")
	writePublished(t, brain, "1.1", 1, true)
	writePublished(t, brain, "1.1", 2, true)
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Solved != 1 || report.Imported != 1 || report.Missing != 1 {
		t.Fatalf("solved = %d, imported = %d, missing = %d, want 1 each",
			report.Solved, report.Imported, report.Missing)
	}
	// The queue must not offer to solve a page that already exists, because doing
	// so would spend a model on work that is already published.
	queue := report.Queue()
	if len(queue) != 1 || queue[0].Number != 3 {
		t.Fatalf("queue = %v, want only exercise 3", queue)
	}
}

func TestVolumeMappingCoversEveryFascicleAndBook(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	sections := map[string]struct {
		dir  string
		book int
	}{
		"1.1":     {"vol1", 1},
		"3.4.1":   {"vol2", 2},
		"5.2.4":   {"vol3", 3},
		"7.2.1.2": {"vol4a", 4},
		"7.2.2.1": {"vol4b", 4},
		"7.2.2.2": {"vol4f6", 4},
	}
	for section := range sections {
		writeExercise(t, source, section, "01.md")
	}
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Volumes) != 6 {
		t.Fatalf("volumes = %d, want all six fascicles", len(report.Volumes))
	}
	// Reading order, not string order: vol4f6 sorts before vol4a.
	var order []string
	for _, volume := range report.Volumes {
		order = append(order, volume.Dir)
	}
	want := "vol1 vol2 vol3 vol4a vol4b vol4f6"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("volume order = %q, want %q", got, want)
	}
	for _, volume := range report.Volumes {
		for _, section := range volume.Sections {
			expected := sections[section.ID]
			if volume.Dir != expected.dir || section.VolumeNumber != expected.book {
				t.Errorf("%s = %s/vol%d, want %s/vol%d",
					section.ID, volume.Dir, section.VolumeNumber, expected.dir, expected.book)
			}
		}
	}
}

func TestMissingOutputIsSortedForAStableQueue(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	// Deliberately out of order, and with sections that string sort wrongly.
	for _, section := range []string{"1.2.10", "1.2.9", "1.2.1"} {
		for _, number := range []int{11, 2, 1} {
			writeExercise(t, source, section, fmt.Sprintf("%02d.md", number))
		}
	}
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := report.WriteMissing(&out); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"1.2.1 1", "1.2.1 2", "1.2.1 11",
		"1.2.9 1", "1.2.9 2", "1.2.9 11",
		"1.2.10 1", "1.2.10 2", "1.2.10 11",
	}, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("queue =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestOrphansFindAPublishedExerciseTheRepoDoesNotHave(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for number := 1; number <= 12; number++ {
		writeExercise(t, source, "1.1", fmt.Sprintf("%02d.md", number))
		writePublished(t, brain, "1.1", number, true)
	}
	writePublished(t, brain, "1.1", 99, true)
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Orphans != 1 {
		t.Fatalf("orphans = %d, want 1", report.Orphans)
	}
	if got := report.Volumes[0].Sections[0].Orphans; len(got) != 1 || got[0] != 99 {
		t.Fatalf("orphan numbers = %v", got)
	}
	// Published exceeds total, which is exactly the anomaly this flag explains.
	if report.Published != 13 || report.Total != 12 {
		t.Fatalf("published = %d against total = %d", report.Published, report.Total)
	}
	var out bytes.Buffer
	if err := report.WriteOrphans(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "vol1/1.1/99.md is published but 1.1 99 is not in vol1") ||
		!strings.Contains(out.String(), "1 orphans") {
		t.Fatalf("orphan report = %q", out.String())
	}
}

func TestGapsAreWorstFirstThenInSectionOrder(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	counts := map[string]int{"1.2.1": 3, "1.2.2": 1, "1.2.3": 3, "1.3.1": 2}
	for section, count := range counts {
		for number := 1; number <= count; number++ {
			writeExercise(t, source, section, fmt.Sprintf("%02d.md", number))
		}
	}
	// One section is complete, so it must not appear in the gap list at all.
	writeExercise(t, source, "1.4.1", "01.md")
	writeResult(t, output, "1.4.1", 1, "Solved.")
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, section := range report.Volumes[0].Gaps() {
		order = append(order, section.ID)
	}
	want := "1.2.1 1.2.3 1.3.1 1.2.2"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("gap order = %q, want %q", got, want)
	}
}

func TestAVolumeFilterAcceptsBothSpellingsAndRejectsNonsense(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	writeExercise(t, source, "7.2.2.2", "01.md")
	scanner := New(source, output, brain)
	for _, spelling := range []string{"4f6", "vol4f6", "VOL4F6"} {
		report, err := scanner.Run(Filter{Volume: spelling})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Volumes) != 1 || report.Volumes[0].Dir != "vol4f6" {
			t.Fatalf("%q selected %+v", spelling, report.Volumes)
		}
	}
	if _, err := scanner.Run(Filter{Volume: "5"}); err == nil {
		t.Fatal("volume 5 should be rejected, there are only four books")
	}
}

func TestAnAbsentBrainReportsNothingPublishedRatherThanFailing(t *testing.T) {
	t.Parallel()
	source, output, _ := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	writeResult(t, output, "1.1", 1, "Solved.")
	report, err := New(source, output, filepath.Join(t.TempDir(), "nowhere")).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Solved != 1 || report.Published != 0 {
		t.Fatalf("solved = %d, published = %d", report.Solved, report.Published)
	}
	// So does no brain at all, for a machine that only has the solver.
	report, err = New(source, output, "").Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Published != 0 {
		t.Fatalf("published = %d with no brain configured", report.Published)
	}
}

func TestHumanOutputTotalsTheVolumes(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	writeExercise(t, source, "5.1.1", "01.md")
	writeExercise(t, source, "5.1.1", "02.md")
	writeResult(t, output, "5.1.1", 1, "Solved.")
	writePublished(t, brain, "5.1.1", 1, true)
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := report.Write(&out, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"volume    total  solved  imported  published  verified  missing",
		"vol1          1       0         0          0         0        1",
		"vol3          2       1         0          1         1        1",
		"total         3       1         0          1         1        2",
		"vol3 sections with gaps, worst first",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q, got\n%s", want, text)
		}
	}
}

func TestASingleVolumeSkipsTheTotalRow(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	report, err := New(source, output, brain).Run(Filter{Volume: "1"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := report.Write(&out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "total  ") && strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("one volume should not repeat itself as a total row, got\n%s", out.String())
	}
}

func TestNoOrphansSaysSoRatherThanPrintingNothing(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", "01.md")
	writePublished(t, brain, "1.1", 1, true)
	report, err := New(source, output, brain).Run(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := report.WriteOrphans(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no orphans") {
		t.Fatalf("orphan report = %q", out.String())
	}
}
