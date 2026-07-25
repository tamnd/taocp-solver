package publish

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	// Windows ships no zone database, so loading Asia/Ho_Chi_Minh there fails
	// unless the binary carries one. A published date with the wrong offset is
	// worse than the few hundred kilobytes this costs.
	_ "time/tzdata"

	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/textguard"
)

// ContentPath is where brain keeps the published solutions, relative to the
// repository root.
var ContentPath = filepath.Join("content", "en", "practice", "maths", "taocp")

// Zone is the timezone the frontmatter dates are stamped in. The old publisher
// wrote a UTC clock and labelled it +07:00, so every published date was seven
// hours early against its own offset. Formatting a real local time fixes that
// without triggering a rewrite, because the comparison ignores the date line.
const Zone = "Asia/Ho_Chi_Minh"

var (
	solutionFile = regexp.MustCompile(`^(\d+)\.md$`)
	dateLine     = regexp.MustCompile(`(?m)^date:\s*".*?"\s*$`)
	imageLink    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	// volumes is fixed rather than discovered, so the top index lists a volume
	// with no solutions yet instead of quietly dropping it.
	volumes = []int{1, 2, 3, 4}
)

// Publisher writes solutions from the result store into the brain content tree.
// It never touches git, so running it by hand is safe.
type Publisher struct {
	Brain  string
	Source string
	Store  result.Store
	// Now supplies the render timestamp. Tests pin it; nothing else sets it.
	Now func() time.Time
}

// Report is what a run did, in the terms a person would ask about.
type Report struct {
	Written   int
	Deleted   int
	Unchanged int
	Sections  int
	Volumes   int
	Top       int
	Solved    int
	Verified  int
	Total     int
	// Images counts figures copied out of the source repository, which happens
	// only when a page that references one is written.
	Images int
	// Changes lists the paths a check run would have touched, so --check can say
	// which files rather than only how many.
	Changes []string
}

// Target names one exercise, or a whole section when Number is zero.
type Target struct {
	Section string
	Number  int
}

// New builds a publisher over the usual three directories.
func New(brain, source string, store result.Store) Publisher {
	return Publisher{Brain: brain, Source: source, Store: store, Now: time.Now}
}

// ContentDir is the taocp subtree inside the brain repository.
func (p Publisher) ContentDir() string {
	return filepath.Join(p.Brain, ContentPath)
}

// Run publishes the selected results and regenerates the indexes they affect.
// With check set nothing is written, and the report says what would have been.
func (p Publisher) Run(targets []Target, check bool) (Report, error) {
	var report Report
	if strings.TrimSpace(p.Brain) == "" {
		return report, errors.New("no brain directory")
	}
	results, err := p.selectResults(targets)
	if err != nil {
		return report, err
	}

	zone, err := time.LoadLocation(Zone)
	if err != nil {
		return report, fmt.Errorf("load timezone: %w", err)
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}

	repository := exercise.NewRepository(p.Source)
	touched := map[string]bool{}
	for _, value := range results {
		section := value.Exercise.SectionID
		path := p.solutionPath(section, value.Exercise.Number)
		changed, deleted, images, err := p.write(value, path, now().In(zone), check)
		if err != nil {
			return report, err
		}
		report.Images += len(images)
		report.Changes = append(report.Changes, images...)
		switch {
		case deleted:
			report.Deleted++
			touched[section] = true
			report.Changes = append(report.Changes, path)
		case changed:
			report.Written++
			touched[section] = true
			report.Changes = append(report.Changes, path)
		default:
			report.Unchanged++
		}
	}

	// Indexes are regenerated for the sections a run touched, and the volume and
	// top indexes always, because their counts move whenever any section does.
	sections := make([]string, 0, len(touched))
	for section := range touched {
		sections = append(sections, section)
	}
	sort.Slice(sections, func(i, j int) bool { return exercise.CompareSections(sections[i], sections[j]) < 0 })
	for _, section := range sections {
		page, err := p.renderSectionIndex(repository, section)
		if err != nil {
			return report, err
		}
		path := filepath.Join(p.sectionDir(section), "_index.md")
		wrote, err := writeIfChanged(path, page, check)
		if err != nil {
			return report, err
		}
		if wrote {
			report.Sections++
			report.Changes = append(report.Changes, path)
		}
	}

	// A volume counts when brain already has a directory for it, or when this run
	// touched one of its sections. A check run writes nothing, so without the
	// second half it would report no volumes at all on a fresh tree.
	live := map[int]bool{}
	for _, number := range volumes {
		live[number] = exists(p.volumeDir(number))
	}
	for section := range touched {
		live[VolumeNumber(section)] = true
	}

	rows, err := p.volumeRows(repository, live)
	if err != nil {
		return report, err
	}
	for _, row := range rows {
		report.Solved += row.Solved
		report.Verified += row.Verified
		report.Total += row.Total
	}
	if len(touched) > 0 {
		for _, number := range volumes {
			if !live[number] {
				continue
			}
			path := filepath.Join(p.volumeDir(number), "_index.md")
			page, err := p.renderVolumeIndex(repository, number)
			if err != nil {
				return report, err
			}
			wrote, err := writeIfChanged(path, page, check)
			if err != nil {
				return report, err
			}
			if wrote {
				report.Volumes++
				report.Changes = append(report.Changes, path)
			}
		}
		path := filepath.Join(p.ContentDir(), "_index.md")
		wrote, err := writeIfChanged(path, RenderTop(rows), check)
		if err != nil {
			return report, err
		}
		if wrote {
			report.Top++
			report.Changes = append(report.Changes, path)
		}
	}
	return report, nil
}

// selectResults resolves the command line into stored results. An empty target
// list means everything in the store, which is what an unattended run wants.
func (p Publisher) selectResults(targets []Target) ([]result.Result, error) {
	if len(targets) == 0 {
		sections, err := p.storeSections()
		if err != nil {
			return nil, err
		}
		for _, section := range sections {
			targets = append(targets, Target{Section: section})
		}
	}
	var out []result.Result
	for _, target := range targets {
		numbers := []int{target.Number}
		if target.Number == 0 {
			found, err := p.storeNumbers(target.Section)
			if err != nil {
				return nil, err
			}
			numbers = found
		}
		for _, number := range numbers {
			value, err := p.Store.Load(target.Section, number)
			if err != nil {
				return nil, fmt.Errorf("load result %s.%d: %w", target.Section, number, err)
			}
			out = append(out, value)
		}
	}
	return out, nil
}

func (p Publisher) storeSections() ([]string, error) {
	entries, err := os.ReadDir(p.Store.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list result store: %w", err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return exercise.CompareSections(out[i], out[j]) < 0 })
	return out, nil
}

func (p Publisher) storeNumbers(section string) ([]int, error) {
	entries, err := os.ReadDir(filepath.Join(p.Store.Root, section))
	if err != nil {
		return nil, fmt.Errorf("list stored section %s: %w", section, err)
	}
	var out []int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err == nil {
			out = append(out, number)
		}
	}
	sort.Ints(out)
	return out, nil
}

// write publishes one solution. It reports whether the file changed and whether
// it was removed by the leak gate, which are different enough that a caller has
// to be able to tell them apart.
func (p Publisher) write(value result.Result, path string, date time.Time, check bool) (changed, deleted bool, images []string, err error) {
	// The last gate before the repository. An error page, a refusal or a model
	// identity disclosure must never be published, and one that slipped through
	// before the guards existed is removed on the way past.
	body, guardErr := textguard.CleanSolution(value.Solution)
	if guardErr != nil {
		if !exists(path) {
			return false, false, nil, nil
		}
		if check {
			return false, true, nil, nil
		}
		if err := os.Remove(path); err != nil {
			return false, false, nil, fmt.Errorf("remove leaked solution %s: %w", path, err)
		}
		return false, true, nil, nil
	}

	page := RenderSolution(Solution{
		Exercise:  value.Exercise,
		Body:      body,
		Verified:  value.Verified,
		SolveTime: value.SolveTime,
		Date:      date,
	})
	// Figures have to be localised before the comparison, or an unchanged page
	// would compare against bytes that still hold the source repository's paths.
	page, images, imageErr := p.localizeImages(page, value.Exercise, check)
	if imageErr != nil {
		return false, false, nil, imageErr
	}
	stored, readErr := os.ReadFile(path)
	// The date is stamped at render time, so two renders of an unchanged solution
	// differ on exactly that line. Blanking it on both sides is what keeps an
	// unchanged page's original date and mtime, and keeps git quiet.
	if readErr == nil && blankDate(string(stored)) == blankDate(page) {
		return false, false, images, nil
	}
	if check {
		return true, false, images, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, nil, fmt.Errorf("create section directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
		return false, false, nil, fmt.Errorf("write solution %s: %w", path, err)
	}
	return true, false, images, nil
}

// localizeImages copies the figures a page references out of the source
// repository and rewrites the links to bare filenames.
//
// An exercise body carries paths like ../../../../md/vol1/images/fig.png, which
// resolve inside the source repository and point at nothing once the page is
// published four directories deeper in a different tree. The already published
// pages that carry figures keep them next to the page under a plain name, so
// that is what this reproduces.
func (p Publisher) localizeImages(page string, target exercise.Exercise, check bool) (string, []string, error) {
	if !strings.Contains(page, "![") {
		return page, nil, nil
	}
	source := exercise.NewRepository(p.Source).Dir(target.SectionID)
	dir := p.sectionDir(target.SectionID)
	var copied []string
	var failure error
	page = imageLink.ReplaceAllStringFunc(page, func(match string) string {
		parts := imageLink.FindStringSubmatch(match)
		alt, link := parts[1], parts[2]
		// A remote image needs no copy, and one that is already a bare filename has
		// been localised by an earlier run.
		if strings.Contains(link, "://") || strings.HasPrefix(link, "data:") || !strings.Contains(link, "/") {
			return match
		}
		from := filepath.Join(source, filepath.FromSlash(link))
		// A link the source repository cannot resolve is left exactly as it is. It is
		// already broken, and inventing a target would hide that.
		if !regularFile(from) {
			return match
		}
		name := filepath.Base(from)
		to := filepath.Join(dir, name)
		// Two exercises in a section may name different figures the same thing, so a
		// genuine clash is disambiguated by the exercise it belongs to.
		if same, err := sameFile(from, to); err != nil {
			failure = err
			return match
		} else if !same && exists(to) {
			name = fmt.Sprintf("%02d_%s", target.Number, name)
			to = filepath.Join(dir, name)
		}
		wrote, err := copyIfChanged(from, to, check)
		if err != nil {
			failure = err
			return match
		}
		if wrote {
			copied = append(copied, to)
		}
		return fmt.Sprintf("![%s](%s)", alt, name)
	})
	if failure != nil {
		return "", nil, failure
	}
	return page, copied, nil
}

// renderSectionIndex builds a section page from the source repo's exercise list
// and the solutions already on disk.
func (p Publisher) renderSectionIndex(repository *exercise.Repository, section string) (string, error) {
	published, err := p.publishedSolutions(section)
	if err != nil {
		return "", err
	}
	meta, metaErr := repository.Metadata(section)

	page := Section{ID: section, Volume: VolumeNumber(section), Total: len(meta)}
	seen := map[int]bool{}
	for _, item := range meta {
		if page.Title == "" {
			page.Title, page.Chapter, page.ChapterTitle = item.SectionTitle, item.Chapter, item.ChapterTitle
		}
		seen[item.Number] = true
		page.Entries = append(page.Entries, entryFor(item, published[item.Number]))
	}
	// A section the source repo cannot be read for used to produce a table with a
	// header and no rows, which is how 30 live pages ended up empty. Falling back
	// to the published files means the page is still a list of what exists.
	if metaErr != nil || len(meta) == 0 {
		page.Total = 0
	}
	numbers := make([]int, 0, len(published))
	for number := range published {
		if !seen[number] {
			numbers = append(numbers, number)
		}
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		page.Entries = append(page.Entries, entryFor(exercise.Exercise{Number: number}, published[number]))
	}
	sort.Slice(page.Entries, func(i, j int) bool { return page.Entries[i].Number < page.Entries[j].Number })

	if page.Title == "" {
		for _, number := range sortedKeys(published) {
			front := published[number]
			page.Title, page.ChapterTitle = front.SectionTitle, front.ChapterTitle
			page.Chapter = front.Chapter
			break
		}
	}
	return RenderSection(page), nil
}

func entryFor(item exercise.Exercise, front *frontmatter) Entry {
	entry := Entry{
		Number:      item.Number,
		Rating:      item.Rating,
		Category:    item.Category,
		Recommended: item.Recommended,
	}
	if front == nil {
		return entry
	}
	entry.Published = true
	entry.Verified = front.Verified
	entry.SolveTime = front.SolveTime
	// A published page that predates the source repo entry still knows its own
	// rating, so the row is not blank.
	if entry.Rating == "" {
		entry.Rating = front.Rating
	}
	if entry.Category == "" {
		entry.Category = front.Category
	}
	return entry
}

func (p Publisher) renderVolumeIndex(repository *exercise.Repository, number int) (string, error) {
	sections, err := p.volumeSections(repository, number)
	if err != nil {
		return "", err
	}
	return RenderVolume(Volume{Number: number, Sections: sections}), nil
}

// volumeRows counts every volume, which the top index needs and the summary line
// reports.
func (p Publisher) volumeRows(repository *exercise.Repository, live map[int]bool) ([]VolumeRow, error) {
	var out []VolumeRow
	for _, number := range volumes {
		if !live[number] {
			continue
		}
		sections, err := p.volumeSections(repository, number)
		if err != nil {
			return nil, err
		}
		row := VolumeRow{Number: number}
		for _, section := range sections {
			row.Solved += section.Solved
			row.Verified += section.Verified
			row.Total += section.Total
		}
		out = append(out, row)
	}
	return out, nil
}

// volumeSections is the row set of a volume index: every section either
// published in brain or known to the source repo, in dotted numeric order.
func (p Publisher) volumeSections(repository *exercise.Repository, number int) ([]SectionRow, error) {
	totals := map[string]int{}
	sections, err := repository.Sections()
	if err == nil {
		for _, section := range sections {
			if VolumeNumber(section) != number {
				continue
			}
			if found, err := repository.List(section); err == nil {
				totals[section] = len(found)
			}
		}
	}

	names := map[string]bool{}
	for section := range totals {
		names[section] = true
	}
	entries, err := os.ReadDir(p.volumeDir(number))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("list volume directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names[entry.Name()] = true
		}
	}

	ordered := make([]string, 0, len(names))
	for section := range names {
		ordered = append(ordered, section)
	}
	sort.Slice(ordered, func(i, j int) bool { return exercise.CompareSections(ordered[i], ordered[j]) < 0 })

	out := make([]SectionRow, 0, len(ordered))
	for _, section := range ordered {
		published, err := p.publishedSolutions(section)
		if err != nil {
			return nil, err
		}
		row := SectionRow{ID: section, Solved: len(published), Total: totals[section]}
		if row.Total == 0 {
			row.Total = row.Solved
		}
		for _, front := range published {
			if front.Verified {
				row.Verified++
			}
		}
		// The title comes from the section index rather than the source repo, so a
		// section with no published solutions renders with an empty title cell the
		// way the live pages do.
		row.Title = p.sectionTitle(section)
		out = append(out, row)
	}
	return out, nil
}

// frontmatter is the part of a published page the indexes read back.
type frontmatter struct {
	SectionTitle string
	ChapterTitle string
	Chapter      int
	Rating       string
	Category     string
	Verified     bool
	SolveTime    time.Duration
}

func (p Publisher) publishedSolutions(section string) (map[int]*frontmatter, error) {
	entries, err := os.ReadDir(p.sectionDir(section))
	if err != nil {
		if os.IsNotExist(err) {
			return map[int]*frontmatter{}, nil
		}
		return nil, fmt.Errorf("list published section %s: %w", section, err)
	}
	out := map[int]*frontmatter{}
	for _, entry := range entries {
		match := solutionFile.FindStringSubmatch(entry.Name())
		if entry.IsDir() || match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.sectionDir(section), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read published solution: %w", err)
		}
		out[number] = parseFrontmatter(string(data))
	}
	return out, nil
}

func (p Publisher) sectionTitle(section string) string {
	data, err := os.ReadFile(filepath.Join(p.sectionDir(section), "_index.md"))
	if err != nil {
		return ""
	}
	return parseFrontmatter(string(data)).SectionTitle
}

// parseFrontmatter reads the handful of keys the indexes need. A full YAML
// parser would be more general and would also have to round trip values it does
// not understand, which is a much larger promise than reading six fields.
func parseFrontmatter(text string) *frontmatter {
	front := &frontmatter{}
	if !strings.HasPrefix(text, "---\n") {
		return front
	}
	for _, line := range strings.Split(text[4:], "\n") {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.TrimSpace(key) {
		case "section_title":
			front.SectionTitle = value
		case "chapter_title":
			front.ChapterTitle = value
		case "chapter":
			front.Chapter, _ = strconv.Atoi(value)
		case "rating":
			front.Rating = value
		case "category":
			front.Category = value
		case "verified":
			front.Verified = value == "true"
		case "solve_time_s":
			seconds, _ := strconv.Atoi(value)
			front.SolveTime = time.Duration(seconds) * time.Second
		}
	}
	return front
}

func (p Publisher) volumeDir(number int) string {
	return filepath.Join(p.ContentDir(), fmt.Sprintf("vol%d", number))
}

func (p Publisher) sectionDir(section string) string {
	return filepath.Join(p.volumeDir(VolumeNumber(section)), section)
}

func (p Publisher) solutionPath(section string, number int) string {
	return filepath.Join(p.sectionDir(section), fmt.Sprintf("%02d.md", number))
}

// blankDate removes the render timestamp so two renders of the same content
// compare equal.
func blankDate(text string) string {
	return dateLine.ReplaceAllString(text, "date:")
}

// writeIfChanged compares plain bytes, which is right for indexes because they
// carry no timestamp.
func writeIfChanged(path, page string, check bool) (bool, error) {
	stored, err := os.ReadFile(path)
	if err == nil && string(stored) == page {
		return false, nil
	}
	if check {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// sameFile reports whether the destination already holds these bytes. Figures
// are a few kilobytes, so comparing them is cheaper than the mtime churn and git
// noise of copying one that has not changed.
func sameFile(from, to string) (bool, error) {
	stored, err := os.ReadFile(to)
	if err != nil {
		return false, nil
	}
	wanted, err := os.ReadFile(from)
	if err != nil {
		return false, fmt.Errorf("read image %s: %w", from, err)
	}
	return bytes.Equal(stored, wanted), nil
}

func copyIfChanged(from, to string, check bool) (bool, error) {
	same, err := sameFile(from, to)
	if err != nil {
		return false, err
	}
	if same {
		return false, nil
	}
	if check {
		return true, nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return false, fmt.Errorf("read image %s: %w", from, err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return false, fmt.Errorf("create directory for %s: %w", to, err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		return false, fmt.Errorf("write image %s: %w", to, err)
	}
	return true, nil
}

func sortedKeys(values map[int]*frontmatter) []int {
	out := make([]int, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Ints(out)
	return out
}
