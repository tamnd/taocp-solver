package publish

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/result"
)

// scratch builds a source repository, a brain repository and a result store in
// temporary directories, so the tests exercise the real file walk rather than a
// stubbed one.
func scratch(t *testing.T, numbers ...int) Publisher {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "taocp")
	brain := filepath.Join(root, "brain")
	store := result.Store{Root: filepath.Join(root, "results")}

	dir := filepath.Join(source, "content", "vol1", "exercises", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, number := range numbers {
		page := fmt.Sprintf(`---
section: "1.1"
section_title: "Algorithms"
chapter: 1
chapter_title: "Basic Concepts"
volume: 1
book_pages: "1–9"
exercise: %d
rating: "10"
category: "simple"
recommended: %t
---
**%d.** [*10*] Exercise %d asks a question.
`, number, number == 2, number, number)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.md", number)), []byte(page), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Publisher{
		Brain: brain, Source: source, Store: store,
		Now: func() time.Time { return time.Date(2026, 7, 25, 9, 30, 0, 0, time.UTC) },
	}
}

func store(t *testing.T, publisher Publisher, number int, solution string, verified bool) {
	t.Helper()
	repository := exercise.NewRepository(publisher.Source)
	target, _, err := repository.Load("1.1", number)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Store.Save(result.Result{
		ID: fmt.Sprintf("1.1.%d", number), Exercise: target, Solution: solution,
		Verified: verified, SolveTime: 78 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishWritesTheSolutionAndItsIndexes(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1, 2, 3)
	store(t, publisher, 1, "A first solution. It is short.", true)
	store(t, publisher, 2, "A second solution.", false)

	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 2 || report.Sections != 1 || report.Volumes != 1 || report.Top != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Solved != 2 || report.Verified != 1 || report.Total != 3 {
		t.Errorf("counts = %d solved, %d verified, %d total", report.Solved, report.Verified, report.Total)
	}

	index := read(t, filepath.Join(publisher.ContentDir(), "vol1", "1.1", "_index.md"))
	// Exercise 3 is in the source repository and has no solution, so the index is
	// still a list of what is left rather than a list of what is done.
	for _, want := range []string{
		"Section 1.1 exercises: 2/3 solved.",
		"| [1](01.md) |  [*10*] | simple | verified | 1m18s |",
		"| [2](02.md) | &#9654; [*10*] | simple | solved | 1m18s |",
		"| 3 |  [*10*] | simple | - | - |",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("section index is missing %q:\n%s", want, index)
		}
	}
	volume := read(t, filepath.Join(publisher.ContentDir(), "vol1", "_index.md"))
	if !strings.Contains(volume, "| [1.1](1.1/) | Algorithms | 2 | 1 | 3 |") {
		t.Errorf("volume index row is wrong:\n%s", volume)
	}
	top := read(t, filepath.Join(publisher.ContentDir(), "_index.md"))
	if !strings.Contains(top, "| [Vol 1](vol1/) | Fundamental Algorithms | 2 | 1 | 3 |") {
		t.Errorf("top index row is wrong:\n%s", top)
	}
}

// The whole point of the byte comparison is that a second run over an unchanged
// store leaves the worktree clean. Anything else buries the real change in
// thousands of files whose only difference is a fresh timestamp.
func TestPublishTwiceLeavesTheWorktreeClean(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1, 2)
	store(t, publisher, 1, "A first solution.", true)
	store(t, publisher, 2, "A second solution.", false)

	if _, err := publisher.Run(nil, false); err != nil {
		t.Fatal(err)
	}
	git(t, publisher.Brain, "init", "-q")
	git(t, publisher.Brain, "add", "-A")
	git(t, publisher.Brain, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "publish")

	// A day later, and with nothing solved in between.
	publisher.Now = func() time.Time { return time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC) }
	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 0 || report.Unchanged != 2 || report.Sections != 0 || report.Volumes != 0 || report.Top != 0 {
		t.Fatalf("second run = %+v, want nothing written", report)
	}
	if dirty := git(t, publisher.Brain, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree is dirty after a second publish:\n%s", dirty)
	}
}

// A stored page that differs only on the render timestamp is left alone, so its
// original date and mtime survive.
func TestADifferentDateIsNotAChange(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	store(t, publisher, 1, "A first solution.", true)
	if _, err := publisher.Run(nil, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(publisher.ContentDir(), "vol1", "1.1", "01.md")
	before := read(t, path)

	publisher.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 0 {
		t.Errorf("report = %+v, want nothing written", report)
	}
	if after := read(t, path); after != before {
		t.Errorf("the page was rewritten with a new date:\n%s", after)
	}
}

// Dates are stamped in local Vietnamese time, not a UTC clock wearing a +07:00
// label the way the old publisher wrote them.
func TestTheDateCarriesTheRealOffset(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	store(t, publisher, 1, "A first solution.", true)
	if _, err := publisher.Run(nil, false); err != nil {
		t.Fatal(err)
	}
	page := read(t, filepath.Join(publisher.ContentDir(), "vol1", "1.1", "01.md"))
	if !strings.Contains(page, `date: "2026-07-25T16:30:00+07:00"`) {
		t.Errorf("date line is wrong:\n%s", firstLines(page, 6))
	}
}

func TestTheLeakGateRefusesToPublishAndCleansUp(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	store(t, publisher, 1, "A first solution.", true)
	if _, err := publisher.Run(nil, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(publisher.ContentDir(), "vol1", "1.1", "01.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	// The stored solution now trips the gate, which is what a pre-guard leak looks
	// like on a later pass.
	store(t, publisher, 1, "I am ChatGPT and I cannot help with that.", true)
	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Deleted != 1 || report.Written != 0 {
		t.Fatalf("report = %+v, want one deletion", report)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the leaked page survived: %v", err)
	}
	// A deletion is reported separately from a write, because a publish run that
	// quietly removed live content would be alarming to find out about later.
	if len(report.Changes) == 0 || report.Changes[0] != path {
		t.Errorf("changes = %v", report.Changes)
	}
}

func TestALeakThatWasNeverPublishedIsSilent(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	store(t, publisher, 1, "As an AI language model I cannot help.", true)
	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 0 || report.Deleted != 0 || report.Unchanged != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestCheckWritesNothing(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1, 2)
	store(t, publisher, 1, "A first solution.", true)

	report, err := publisher.Run(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 1 || report.Sections != 1 || report.Volumes != 1 || report.Top != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(publisher.ContentDir()); !os.IsNotExist(err) {
		t.Fatalf("check created the content tree: %v", err)
	}
}

func TestOneExerciseAndOneSectionCanBeNamed(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1, 2)
	store(t, publisher, 1, "A first solution.", true)
	store(t, publisher, 2, "A second solution.", false)

	report, err := publisher.Run([]Target{{Section: "1.1", Number: 2}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 1 {
		t.Fatalf("report = %+v, want only the named exercise", report)
	}
	if _, err := os.Stat(filepath.Join(publisher.ContentDir(), "vol1", "1.1", "01.md")); !os.IsNotExist(err) {
		t.Error("naming one exercise published another")
	}

	report, err = publisher.Run([]Target{{Section: "1.1"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 1 || report.Unchanged != 1 {
		t.Fatalf("report = %+v, want the rest of the section", report)
	}
}

// A section the source repository knows nothing about used to render a table
// with a header and no rows, which is how 30 live pages ended up empty. The
// published files are enough to build the list.
func TestASectionMissingFromTheSourceStillListsItsSolutions(t *testing.T) {
	t.Parallel()
	publisher := scratch(t)
	publisher.Source = filepath.Join(t.TempDir(), "absent")
	if err := os.MkdirAll(filepath.Join(publisher.ContentDir(), "vol1", "1.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := RenderSolution(Solution{
		Exercise: exercise.Exercise{
			SectionID: "1.1", Number: 4, SectionTitle: "Algorithms", Chapter: 1,
			ChapterTitle: "Basic Concepts", Volume: 1, Rating: "10", Category: "simple",
			Body: "A question.",
		},
		Body: "A solution.", Verified: true, SolveTime: 52 * time.Second,
		Date: time.Now(),
	})
	path := filepath.Join(publisher.ContentDir(), "vol1", "1.1", "04.md")
	if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	index, err := publisher.renderSectionIndex(exercise.NewRepository(publisher.Source), "1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "| [4](04.md) |  [*10*] | simple | verified | 52s |") {
		t.Errorf("index has no row for the published solution:\n%s", index)
	}
	// With no source repository there is no total to divide by, so the count reads
	// as a plain number rather than a fraction of nothing.
	if !strings.Contains(index, "Section 1.1 exercises: 1 solved.") {
		t.Errorf("index count is wrong:\n%s", index)
	}
	if !strings.Contains(index, "# Section 1.1. Algorithms") {
		t.Errorf("index title is wrong:\n%s", index)
	}
}

func TestAMissingBrainDirectoryIsAnError(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	publisher.Brain = ""
	if _, err := publisher.Run(nil, false); err == nil {
		t.Fatal("publishing with no brain directory must be an error")
	}
}

// An exercise body links its figures the way the source repository is laid out.
// Those paths resolve to nothing once the page is published in another tree, so
// the figure has to travel with it.
func TestFiguresTravelWithTheSolution(t *testing.T) {
	t.Parallel()
	publisher := scratch(t, 1)
	image := filepath.Join(publisher.Source, "md", "vol1", "images", "page_0041.png")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(publisher.Source, "content", "vol1", "exercises", "1.1", "01.md")
	page := read(t, body) + "\n![Figure 5](../../../../md/vol1/images/page_0041.png)\n" +
		"![Remote](https://example.com/x.png)\n![Missing](../../../../md/vol1/images/absent.png)\n"
	if err := os.WriteFile(body, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	store(t, publisher, 1, "A solution.", true)

	report, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Images != 1 {
		t.Fatalf("report = %+v", report)
	}
	published := read(t, filepath.Join(publisher.ContentDir(), "vol1", "1.1", "01.md"))
	if !strings.Contains(published, "![Figure 5](page_0041.png)") {
		t.Errorf("the figure link was not localised:\n%s", published)
	}
	// A remote image needs no copy, and one the source cannot resolve is already
	// broken, so rewriting it would only hide that.
	if !strings.Contains(published, "![Remote](https://example.com/x.png)") {
		t.Error("a remote image must be left alone")
	}
	if !strings.Contains(published, "![Missing](../../../../md/vol1/images/absent.png)") {
		t.Error("an unresolvable link must be left alone")
	}
	if got := read(t, filepath.Join(publisher.ContentDir(), "vol1", "1.1", "page_0041.png")); got != "not really a png" {
		t.Errorf("copied image = %q", got)
	}

	// The second run has the same figure to place and must not copy it again, or
	// every run would show up as a change in the content repository.
	second, err := publisher.Run(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Images != 0 || second.Unchanged != 1 {
		t.Fatalf("second report = %+v", second)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func firstLines(text string, count int) string {
	lines := strings.SplitN(text, "\n", count+1)
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}
