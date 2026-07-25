// Package publish renders solved exercises into the brain content tree.
//
// The format is not a design decision. Thousands of pages are already live, so
// a renderer that differs by one byte produces a diff the size of the corpus
// and rewrites dates that carry meaning. Everything here reproduces what is on
// disk, and the golden tests are copies of real published files.
package publish

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tamnd/taocp-solver/exercise"
)

// descriptionLimit is the character budget for the page description. Search
// engines truncate around there anyway.
const descriptionLimit = 200

// DateLayout stamps the frontmatter date with a real offset rather than a fixed
// string, so the timestamp and the offset agree with each other.
const DateLayout = "2006-01-02T15:04:05-07:00"

var volumeTitles = map[int]string{
	1: "Fundamental Algorithms",
	2: "Seminumerical Algorithms",
	3: "Sorting and Searching",
	4: "Combinatorial Algorithms",
}

var (
	// exercisePrefix is the label the source exercise text carries. The page
	// prints its own header, so the copy in the body would be a duplicate.
	exercisePrefix = regexp.MustCompile(`^(?:[\x{25B6}\x{25BA}]\s*)?(?:\*\*\d+\.\*\*\s*)?(?:\[\*[0-9HM]+\*\]\s*)?`)
	displayMath    = regexp.MustCompile(`\$\$[\s\S]*?\$\$`)
	inlineMath     = regexp.MustCompile(`\$[^$\n]+?\$`)
	emDash         = regexp.MustCompile(`\s*\x{2014}\s*`)
	mathHole       = regexp.MustCompile("\x00M(\\d+)\x00")
)

// Solution is one published exercise page.
type Solution struct {
	Exercise  exercise.Exercise
	Body      string
	Verified  bool
	SolveTime time.Duration
	Date      time.Time
}

// Entry is one row of a section index: an exercise the source repo knows about,
// solved or not.
type Entry struct {
	Number      int
	Rating      string
	Category    string
	Recommended bool
	Published   bool
	Verified    bool
	SolveTime   time.Duration
}

// Section is one section index page. Total is the source repo's exercise count,
// which can be larger than len(Entries) only when the repo is unreadable, in
// which case it is zero and the count reads as a plain total.
type Section struct {
	ID           string
	Title        string
	Chapter      int
	ChapterTitle string
	Volume       int
	Entries      []Entry
	Total        int
}

// SectionRow is one row of a volume index.
type SectionRow struct {
	ID       string
	Title    string
	Solved   int
	Verified int
	Total    int
}

// Volume is one volume index page.
type Volume struct {
	Number   int
	Sections []SectionRow
}

// VolumeRow is one row of the top index.
type VolumeRow struct {
	Number   int
	Solved   int
	Verified int
	Total    int
}

// VolumeNumber is the book volume a section belongs to.
//
// This is deliberately not exercise.VolumeDir. The source repo splits volume 4
// by fascicle because that is how the exercises were published, while brain
// groups solutions by book volume because that is how a reader looks for them.
func VolumeNumber(sectionID string) int {
	chapter, err := strconv.Atoi(strings.SplitN(sectionID, ".", 2)[0])
	if err != nil {
		return 1
	}
	switch {
	case chapter <= 2:
		return 1
	case chapter <= 4:
		return 2
	case chapter <= 6:
		return 3
	default:
		return 4
	}
}

// VolumeTitle is the book's own title for a volume.
func VolumeTitle(number int) string {
	if title, ok := volumeTitles[number]; ok {
		return title
	}
	return fmt.Sprintf("Volume %d", number)
}

// SectionWeight orders sections within a volume. Each of the first four dotted
// parts gets a two digit field, which is what makes 1.2.10 sort after 1.2.9
// instead of before it the way strings would.
func SectionWeight(sectionID string) int {
	weight := 0
	for i, part := range strings.Split(sectionID, ".") {
		if i >= 4 {
			break
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		scale := 1
		for range 3 - i {
			scale *= 100
		}
		weight += value * scale
	}
	return weight
}

// Weight orders one solution within its volume, section first and exercise
// number second.
func Weight(sectionID string, number int) int {
	return SectionWeight(sectionID)*1000 + number
}

// Duration renders a solve time the way the published pages do. Zero renders as
// a dash, because a page claiming it took no time reads as a bug.
func Duration(value time.Duration) string {
	seconds := int(value.Round(time.Second) / time.Second)
	if seconds <= 0 {
		return "-"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes, seconds := seconds/60, seconds%60
	if minutes < 60 {
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

// Description is the opening sentence of a solution, for the page metadata.
func Description(body, sectionID string, number int) string {
	text := firstSentence(body, descriptionLimit)
	if text == "" {
		return fmt.Sprintf("Solution to TAOCP %s Exercise %d.", sectionID, number)
	}
	return cleanEmDash(text)
}

// firstSentence reads at most the first six lines of prose as one paragraph and
// returns its first sentence.
//
// Two details earn their keep. Display math and code fences are skipped, since
// a description that opens with a formula tells a reader nothing. And a full
// stop inside inline math does not end a sentence, because LaTeX is full of
// them.
func firstSentence(body string, limit int) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
		if len(lines) == 6 {
			break
		}
	}
	var prose []string
	for _, line := range lines {
		if strings.Contains(line, "$$") || strings.HasPrefix(line, "```") {
			continue
		}
		prose = append(prose, line)
	}
	paragraph := strings.Join(prose, " ")
	inMath := false
	for i, letter := range paragraph {
		if letter == '$' {
			inMath = !inMath
		}
		if inMath || i == 0 || !strings.ContainsRune(".!?", letter) {
			continue
		}
		sentence := strings.TrimSpace(paragraph[:i+1])
		if utf8.RuneCountInString(sentence) <= limit {
			return sentence
		}
		break
	}
	if letters := []rune(paragraph); len(letters) > limit {
		return strings.TrimRight(string(letters[:limit]), " \t\n\r") + "..."
	}
	return strings.TrimRight(paragraph, " \t\n\r")
}

// cleanEmDash replaces em dashes with a comma, leaving math spans alone so a
// LaTeX command is never rewritten.
func cleanEmDash(text string) string {
	var spans []string
	keep := func(match string) string {
		spans = append(spans, match)
		return fmt.Sprintf("\x00M%d\x00", len(spans)-1)
	}
	text = displayMath.ReplaceAllStringFunc(text, keep)
	text = inlineMath.ReplaceAllStringFunc(text, keep)
	text = emDash.ReplaceAllString(text, ", ")
	return mathHole.ReplaceAllStringFunc(text, func(match string) string {
		index, err := strconv.Atoi(mathHole.FindStringSubmatch(match)[1])
		if err != nil || index >= len(spans) {
			return match
		}
		return spans[index]
	})
}

// yamlString quotes a value for frontmatter.
func yamlString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

// RenderSolution renders one exercise page.
func RenderSolution(value Solution) string {
	target := value.Exercise
	tags := []string{"taocp", "mathematics", "algorithms", fmt.Sprintf("volume-%d", target.Volume)}
	if target.Category != "" && target.Category != "immediate" {
		tags = append(tags, target.Category)
	}
	quoted := make([]string, len(tags))
	for i, tag := range tags {
		quoted[i] = yamlString(tag)
	}

	body := strings.TrimSpace(exercisePrefix.ReplaceAllString(strings.TrimSpace(target.Body), ""))
	body = cleanEmDash(body)
	recommended := ""
	if target.Recommended {
		recommended = "&#9654; "
	}
	verified := "no"
	if value.Verified {
		verified = "yes"
	}

	var page strings.Builder
	page.WriteString("---\n")
	fmt.Fprintf(&page, "title: %s\n", yamlString(fmt.Sprintf("TAOCP %s Exercise %d", target.SectionID, target.Number)))
	fmt.Fprintf(&page, "description: %s\n", yamlString(Description(value.Body, target.SectionID, target.Number)))
	fmt.Fprintf(&page, "date: %s\n", yamlString(value.Date.Format(DateLayout)))
	fmt.Fprintf(&page, "tags: [%s]\n", strings.Join(quoted, ", "))
	page.WriteString("categories: [\"mathematics\"]\n")
	fmt.Fprintf(&page, "section: %s\n", yamlString(target.SectionID))
	fmt.Fprintf(&page, "section_title: %s\n", yamlString(target.SectionTitle))
	fmt.Fprintf(&page, "chapter: %d\n", target.Chapter)
	fmt.Fprintf(&page, "chapter_title: %s\n", yamlString(target.ChapterTitle))
	fmt.Fprintf(&page, "volume: %d\n", target.Volume)
	fmt.Fprintf(&page, "book_pages: %s\n", yamlString(target.BookPages))
	fmt.Fprintf(&page, "exercise: %d\n", target.Number)
	fmt.Fprintf(&page, "rating: %s\n", yamlString(target.Rating))
	fmt.Fprintf(&page, "category: %s\n", yamlString(target.Category))
	fmt.Fprintf(&page, "recommended: %t\n", target.Recommended)
	fmt.Fprintf(&page, "verified: %t\n", value.Verified)
	fmt.Fprintf(&page, "solve_time_s: %d\n", int(value.SolveTime.Round(time.Second)/time.Second))
	fmt.Fprintf(&page, "weight: %d\n", Weight(target.SectionID, target.Number))
	page.WriteString("draft: false\n")
	page.WriteString("---\n\n")

	fmt.Fprintf(&page, "[Section %s: %s](../)\n\n", target.SectionID, target.SectionTitle)
	fmt.Fprintf(&page, "**Exercise %d.** %s[*%s*] %s\n\n", target.Number, recommended, target.Rating, body)
	// The two trailing spaces are Markdown hard breaks, so the two lines render
	// as one block rather than one paragraph.
	fmt.Fprintf(&page, "**Verified:** %s  \n", verified)
	fmt.Fprintf(&page, "**Solve time:** %s  \n\n", Duration(value.SolveTime))
	page.WriteString("---\n\n")
	page.WriteString(strings.TrimSpace(value.Body))
	page.WriteString("\n")
	return page.String()
}

// RenderSection renders a section index. Every exercise the source repo knows
// about gets a row, so an unsolved one shows up as a bare number and dashes.
// That is what makes the page a live list of what is left.
func RenderSection(value Section) string {
	solved := 0
	for _, entry := range value.Entries {
		if entry.Published {
			solved++
		}
	}
	count := fmt.Sprintf("%d solved", solved)
	if value.Total > 0 {
		count = fmt.Sprintf("%d/%d solved", solved, value.Total)
	}

	var page strings.Builder
	page.WriteString("---\n")
	fmt.Fprintf(&page, "title: %s\n", yamlString(fmt.Sprintf("TAOCP %s: %s", value.ID, value.Title)))
	fmt.Fprintf(&page, "description: %s\n", yamlString(fmt.Sprintf("Section %s exercises: %s.", value.ID, count)))
	page.WriteString("tags: [\"taocp\", \"mathematics\", \"algorithms\"]\n")
	page.WriteString("categories: [\"mathematics\"]\n")
	fmt.Fprintf(&page, "section: %s\n", yamlString(value.ID))
	fmt.Fprintf(&page, "section_title: %s\n", yamlString(value.Title))
	fmt.Fprintf(&page, "chapter: %d\n", value.Chapter)
	fmt.Fprintf(&page, "chapter_title: %s\n", yamlString(value.ChapterTitle))
	fmt.Fprintf(&page, "volume: %d\n", value.Volume)
	fmt.Fprintf(&page, "weight: %d\n", SectionWeight(value.ID))
	page.WriteString("draft: false\n")
	page.WriteString("---\n\n")

	fmt.Fprintf(&page, "# Section %s. %s\n\n", value.ID, value.Title)
	fmt.Fprintf(&page, "Exercises from [TAOCP Volume %d](../) Section %s: %s.\n\n", value.Volume, value.ID, count)
	page.WriteString("| # | Rating | Category | Status | Time |\n")
	page.WriteString("|---|--------|----------|--------|------|\n")
	for _, entry := range value.Entries {
		mark := ""
		if entry.Recommended {
			mark = "&#9654;"
		}
		link, status, elapsed := strconv.Itoa(entry.Number), "-", "-"
		if entry.Published {
			link = fmt.Sprintf("[%d](%02d.md)", entry.Number, entry.Number)
			status = "solved"
			if entry.Verified {
				status = "verified"
			}
			elapsed = Duration(entry.SolveTime)
		}
		fmt.Fprintf(&page, "| %s | %s [*%s*] | %s | %s | %s |\n", link, mark, entry.Rating, entry.Category, status, elapsed)
	}
	return page.String()
}

// RenderVolume renders a volume index.
func RenderVolume(value Volume) string {
	title := VolumeTitle(value.Number)
	var solved, verified, total int
	for _, row := range value.Sections {
		solved += row.Solved
		verified += row.Verified
		total += row.Total
	}
	counts := fmt.Sprintf("%d solved, %d verified, %d total", solved, verified, total)

	var page strings.Builder
	page.WriteString("---\n")
	fmt.Fprintf(&page, "title: %s\n", yamlString(fmt.Sprintf("TAOCP Vol %d: %s", value.Number, title)))
	fmt.Fprintf(&page, "description: %s\n", yamlString(fmt.Sprintf("Volume %d: %s. %s.", value.Number, title, counts)))
	page.WriteString("tags: [\"taocp\", \"mathematics\", \"algorithms\", \"knuth\"]\n")
	page.WriteString("categories: [\"mathematics\"]\n")
	fmt.Fprintf(&page, "weight: %d\n", value.Number*10)
	page.WriteString("draft: false\n")
	page.WriteString("---\n\n")

	fmt.Fprintf(&page, "# Volume %d: %s\n\n", value.Number, title)
	fmt.Fprintf(&page, "Exercise solutions for [TAOCP](../) Volume %d. %s.\n\n", value.Number, counts)
	page.WriteString("| Section | Title | Solved | Verified | Total |\n")
	page.WriteString("|---------|-------|-------:|--------:|------:|\n")
	if len(value.Sections) == 0 {
		page.WriteString("| (none yet) | | | | |\n")
		return page.String()
	}
	for _, row := range value.Sections {
		if row.Solved == 0 {
			fmt.Fprintf(&page, "| %s | %s | — | — | %d |\n", row.ID, row.Title, row.Total)
			continue
		}
		fmt.Fprintf(&page, "| [%s](%s/) | %s | %d | %d | %d |\n", row.ID, row.ID, row.Title, row.Solved, row.Verified, row.Total)
	}
	return page.String()
}

// RenderTop renders the index above the volumes.
func RenderTop(volumes []VolumeRow) string {
	var solved, verified, total int
	for _, row := range volumes {
		solved += row.Solved
		verified += row.Verified
		total += row.Total
	}

	var page strings.Builder
	page.WriteString("---\n")
	page.WriteString("title: \"TAOCP\"\n")
	page.WriteString("description: \"The Art of Computer Programming: exercise solutions.\"\n")
	page.WriteString("tags: [\"taocp\", \"mathematics\", \"algorithms\", \"knuth\"]\n")
	page.WriteString("categories: [\"mathematics\"]\n")
	page.WriteString("weight: 5\n")
	page.WriteString("draft: false\n")
	page.WriteString("---\n\n")
	page.WriteString("# The Art of Computer Programming\n\n")
	fmt.Fprintf(&page, "Exercise solutions for [The Art of Computer Programming](https://www-cs-faculty.stanford.edu/~knuth/taocp.html) by Donald E. Knuth. %d solved, %d verified, %d total.\n\n", solved, verified, total)
	page.WriteString("| Volume | Title | Solved | Verified | Total |\n")
	page.WriteString("|--------|-------|-------:|--------:|------:|\n")
	if len(volumes) == 0 {
		page.WriteString("| (none yet) | | | | |\n")
		return page.String()
	}
	for _, row := range volumes {
		fmt.Fprintf(&page, "| [Vol %d](vol%d/) | %s | %d | %d | %d |\n", row.Number, row.Number, VolumeTitle(row.Number), row.Solved, row.Verified, row.Total)
	}
	return page.String()
}
