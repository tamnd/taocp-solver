package publish

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/taocp-solver/exercise"
)

// The goldens are copies of pages that are live in brain. A renderer that
// differs from them by one byte would rewrite the whole corpus on its first run,
// so these tests compare raw bytes and never trim: the two trailing spaces on
// the verified and solve time lines are part of the format.
func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fixture(t *testing.T, name string, into any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func TestSolutionMatchesThePublishedPage(t *testing.T) {
	t.Parallel()
	var stored struct {
		SectionID    string `json:"section_id"`
		Number       int    `json:"exercise_num"`
		SectionTitle string `json:"section_title"`
		Chapter      int    `json:"chapter"`
		ChapterTitle string `json:"chapter_title"`
		Volume       int    `json:"volume"`
		BookPages    string `json:"book_pages"`
		Rating       string `json:"rating"`
		Category     string `json:"category"`
		Recommended  bool   `json:"recommended"`
		ExerciseMD   string `json:"exercise_md"`
		SolutionMD   string `json:"solution_md"`
		Verified     bool   `json:"verified"`
		SolveTime    int    `json:"solve_time_s"`
		Date         string `json:"date"`
	}
	fixture(t, "solution.json", &stored)
	date, err := time.Parse(DateLayout, stored.Date)
	if err != nil {
		t.Fatal(err)
	}

	page := RenderSolution(Solution{
		Exercise: exercise.Exercise{
			SectionID:    stored.SectionID,
			Number:       stored.Number,
			SectionTitle: stored.SectionTitle,
			Chapter:      stored.Chapter,
			ChapterTitle: stored.ChapterTitle,
			Volume:       stored.Volume,
			BookPages:    stored.BookPages,
			Rating:       stored.Rating,
			Category:     stored.Category,
			Recommended:  stored.Recommended,
			Body:         stored.ExerciseMD,
		},
		Body:      stored.SolutionMD,
		Verified:  stored.Verified,
		SolveTime: time.Duration(stored.SolveTime) * time.Second,
		Date:      date,
	})
	if want := golden(t, "solution.golden.md"); page != want {
		t.Errorf("solution page differs from the published one:\ngot:\n%s\nwant:\n%s", page, want)
	}
}

func TestSectionIndexMatchesThePublishedPage(t *testing.T) {
	t.Parallel()
	var stored struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Chapter      int    `json:"chapter"`
		ChapterTitle string `json:"chapter_title"`
		Volume       int    `json:"volume"`
		Total        int    `json:"total"`
		Entries      []struct {
			Number      int    `json:"number"`
			Rating      string `json:"rating"`
			Category    string `json:"category"`
			Recommended bool   `json:"recommended"`
			Published   bool   `json:"published"`
			Verified    bool   `json:"verified"`
			SolveTime   int    `json:"solve_time_s"`
		} `json:"entries"`
	}
	fixture(t, "section.json", &stored)

	page := Section{
		ID: stored.ID, Title: stored.Title, Chapter: stored.Chapter,
		ChapterTitle: stored.ChapterTitle, Volume: stored.Volume, Total: stored.Total,
	}
	unsolved := 0
	for _, entry := range stored.Entries {
		if !entry.Published {
			unsolved++
		}
		page.Entries = append(page.Entries, Entry{
			Number: entry.Number, Rating: entry.Rating, Category: entry.Category,
			Recommended: entry.Recommended, Published: entry.Published, Verified: entry.Verified,
			SolveTime: time.Duration(entry.SolveTime) * time.Second,
		})
	}
	// The fixture is a section with something still to do, so the bare number and
	// dash row is covered by the golden rather than only by a unit test.
	if unsolved == 0 {
		t.Fatal("the section fixture has no unsolved exercise")
	}
	if got := RenderSection(page); got != golden(t, "section.golden.md") {
		t.Errorf("section index differs from the published one:\n%s", got)
	}
}

func TestVolumeIndexMatchesThePublishedPage(t *testing.T) {
	t.Parallel()
	var stored struct {
		Number   int `json:"number"`
		Sections []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Solved   int    `json:"solved"`
			Verified int    `json:"verified"`
			Total    int    `json:"total"`
		} `json:"sections"`
	}
	fixture(t, "volume.json", &stored)

	page := Volume{Number: stored.Number}
	for _, row := range stored.Sections {
		page.Sections = append(page.Sections, SectionRow{
			ID: row.ID, Title: row.Title, Solved: row.Solved, Verified: row.Verified, Total: row.Total,
		})
	}
	if got := RenderVolume(page); got != golden(t, "volume.golden.md") {
		t.Errorf("volume index differs from the published one:\n%s", got)
	}
}

func TestWeightArithmetic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		section  string
		number   int
		section2 int
		weight   int
	}{
		{"1.1", 1, 1010000, 1010000001},
		{"1.2.10", 8, 1021000, 1021000008},
		{"6.5", 2, 6050000, 6050000002},
		{"7.2.1.1", 1, 7020101, 7020101001},
		{"7.2.2.2", 525, 7020202, 7020202525},
		// A part above 99 overflows its two digit field and lands in the next one.
		// No TAOCP section is numbered that way, and pinning the arithmetic says
		// so out loud rather than leaving it to be discovered.
		{"1.2.100", 1, 1030000, 1030000001},
	}
	for _, test := range cases {
		if got := SectionWeight(test.section); got != test.section2 {
			t.Errorf("SectionWeight(%q) = %d, want %d", test.section, got, test.section2)
		}
		if got := Weight(test.section, test.number); got != test.weight {
			t.Errorf("Weight(%q, %d) = %d, want %d", test.section, test.number, got, test.weight)
		}
	}
}

func TestDurationFormat(t *testing.T) {
	t.Parallel()
	cases := map[int]string{0: "-", 59: "59s", 60: "1m", 74: "1m14s", 194: "3m14s", 3600: "1h00m", 3900: "1h05m"}
	for seconds, want := range cases {
		if got := Duration(time.Duration(seconds) * time.Second); got != want {
			t.Errorf("Duration(%ds) = %q, want %q", seconds, got, want)
		}
	}
}

func TestDescriptionReadsPastMathAndHeadings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// A description that opened with a formula would tell a reader nothing,
			// so display math is skipped and the prose after it is used.
			name: "opens with display math",
			body: "$$\\sum_{k} k = n(n+1)/2$$\n\nBoth sides count the same pairs. The rest follows.",
			want: "Both sides count the same pairs.",
		},
		{
			name: "opens with a heading",
			body: "# Proof\n\nInduct on $n$. The base case is immediate.",
			want: "Induct on $n$.",
		},
		{
			// A full stop inside inline math is not the end of a sentence.
			name: "full stop inside math",
			body: "Let $f(x) = 0.5x$ be the map. That is enough.",
			want: "Let $f(x) = 0.5x$ be the map.",
		},
		{
			name: "empty",
			body: "   \n\n  ",
			want: "Solution to TAOCP 1.1 Exercise 3.",
		},
		{
			name: "em dash becomes a comma",
			body: "The bound is tight—in fact optimal. More follows.",
			want: "The bound is tight, in fact optimal.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Description(test.body, "1.1", 3); got != test.want {
				t.Errorf("Description = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDescriptionTruncatesALongOpening(t *testing.T) {
	t.Parallel()
	long := ""
	for range 30 {
		long += "abcdefg "
	}
	got := Description(long, "1.1", 3)
	if size := len([]rune(got)); size > descriptionLimit+3 {
		t.Fatalf("description is %d characters: %q", size, got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated description must say so: %q", got)
	}
}

func TestExercisePrefixIsStrippedOnce(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"▶ **8.** [*25*] Show that the sum": "Show that the sum",
		"**8.** [*M25*] Show that the sum":  "Show that the sum",
		"Show that the sum":                 "Show that the sum",
		// Only the leading label goes. A second one is part of the text.
		"**8.** [*25*] **9.** [*25*] Show that": "**9.** [*25*] Show that",
	}
	for body, want := range cases {
		page := RenderSolution(Solution{
			Exercise: exercise.Exercise{SectionID: "1.1", Number: 8, Rating: "25", Body: body},
			Body:     "A solution.",
		})
		if want := "**Exercise 8.** [*25*] " + want + "\n"; !strings.Contains(page, want) {
			t.Errorf("body %q produced a page without %q", body, want)
		}
	}
}

func TestRecommendedExerciseCarriesTheMarker(t *testing.T) {
	t.Parallel()
	page := RenderSolution(Solution{
		Exercise: exercise.Exercise{SectionID: "1.1", Number: 8, Rating: "25", Recommended: true, Body: "Show that"},
		Body:     "A solution.",
	})
	if !strings.Contains(page, "**Exercise 8.** &#9654; [*25*] Show that") {
		t.Errorf("page is missing the recommended marker:\n%s", page)
	}
}

func TestVolumeNumberFollowsTheBookNotTheFascicles(t *testing.T) {
	t.Parallel()
	cases := map[string]int{"1.1": 1, "2.3.4.6": 1, "3.5": 2, "4.6.4": 2, "5.2.4": 3, "6.5": 3, "7.2.2.2": 4}
	for section, want := range cases {
		if got := VolumeNumber(section); got != want {
			t.Errorf("VolumeNumber(%q) = %d, want %d", section, got, want)
		}
	}
	// The source repo splits volume 4 by fascicle, and brain does not. Both
	// mappings have to stay, so this pins the pair that differ.
	if dir := exercise.VolumeDir("7.2.2.2"); dir != "vol4f6" {
		t.Errorf("source directory for 7.2.2.2 = %q", dir)
	}
}

func TestEmptyIndexesSayThereIsNothingYet(t *testing.T) {
	t.Parallel()
	if !strings.Contains(RenderVolume(Volume{Number: 2}), "| (none yet) | | | | |\n") {
		t.Error("an empty volume index needs a placeholder row or the table renders broken")
	}
	if !strings.Contains(RenderTop(nil), "| (none yet) | | | | |\n") {
		t.Error("an empty top index needs a placeholder row")
	}
}
