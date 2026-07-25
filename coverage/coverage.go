// Package coverage answers the one question a long run asks constantly: which
// exercises are still missing, and where.
//
// The three inputs are all directories, so there is no database and no state
// file. Restarting the runner recomputes the queue, which is why the queue can
// never drift from the truth on disk.
package coverage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/publish"
	"github.com/tamnd/taocp-solver/result"
)

// VolumeDirs are the source repository's fascicle directories, in reading
// order. The report keeps this order rather than sorting, because vol4f6 sorts
// before vol4a and no reader expects that.
var VolumeDirs = []string{"vol1", "vol2", "vol3", "vol4a", "vol4b", "vol4f6"}

// Section is the coverage of one section.
type Section struct {
	ID string `json:"section"`
	// VolumeDir is the source fascicle, VolumeNumber the published book volume.
	// They differ: 7.2.2.2 lives in vol4b and publishes under vol4.
	VolumeDir    string `json:"volume_dir"`
	VolumeNumber int    `json:"volume"`
	Total        int    `json:"total"`
	Solved       int    `json:"solved"`
	Published    int    `json:"published"`
	Verified     int    `json:"verified"`
	// Imported counts exercises that are published but have no result in the
	// store, which is every proof written before this tool kept one.
	Imported int `json:"imported"`
	// Missing lists the exercise numbers with neither a stored result nor a
	// published page, ascending. It is the work queue for this section.
	Missing []int `json:"missing,omitempty"`
	// Orphans lists published numbers the source repository does not enumerate.
	// Reported, never deleted.
	Orphans []int `json:"orphans,omitempty"`
}

// Volume aggregates the sections of one source fascicle.
type Volume struct {
	Dir       string    `json:"volume_dir"`
	Total     int       `json:"total"`
	Solved    int       `json:"solved"`
	Published int       `json:"published"`
	Verified  int       `json:"verified"`
	Imported  int       `json:"imported"`
	Missing   int       `json:"missing"`
	Orphans   int       `json:"orphans"`
	Sections  []Section `json:"sections,omitempty"`
}

// Report is the whole picture.
type Report struct {
	Volumes   []Volume `json:"volumes"`
	Total     int      `json:"total"`
	Solved    int      `json:"solved"`
	Published int      `json:"published"`
	Verified  int      `json:"verified"`
	Imported  int      `json:"imported"`
	Missing   int      `json:"missing"`
	Orphans   int      `json:"orphans"`
}

// Filter narrows a scan. An empty filter means everything.
type Filter struct {
	// Volume accepts either a fascicle directory or the bare suffix, so both
	// --volume vol4a and --volume 4a work.
	Volume  string
	Section string
}

// Scanner reads the three sources of truth.
type Scanner struct {
	Repository *exercise.Repository
	Store      result.Store
	// Brain is the content directory holding vol1 through vol4. An empty or
	// absent brain reports zero published rather than failing, because coverage
	// is useful on a machine that only has the solver.
	Brain string
}

// New builds a scanner over a source repository, a result store, and a brain
// checkout.
func New(source, output, brain string) Scanner {
	scanner := Scanner{
		Repository: exercise.NewRepository(source),
		Store:      result.Store{Root: output},
	}
	if strings.TrimSpace(brain) != "" {
		scanner.Brain = publish.New(brain, source, scanner.Store).ContentDir()
	}
	return scanner
}

// Run scans every section the filter admits.
func (s Scanner) Run(filter Filter) (Report, error) {
	sections, err := s.sections(filter)
	if err != nil {
		return Report{}, err
	}
	byDir := map[string]*Volume{}
	var report Report
	for _, id := range sections {
		section, err := s.section(id)
		if err != nil {
			return Report{}, err
		}
		volume := byDir[section.VolumeDir]
		if volume == nil {
			volume = &Volume{Dir: section.VolumeDir}
			byDir[section.VolumeDir] = volume
		}
		volume.Sections = append(volume.Sections, section)
		volume.Total += section.Total
		volume.Solved += section.Solved
		volume.Published += section.Published
		volume.Verified += section.Verified
		volume.Imported += section.Imported
		volume.Missing += len(section.Missing)
		volume.Orphans += len(section.Orphans)
	}
	for _, dir := range VolumeDirs {
		volume := byDir[dir]
		if volume == nil {
			continue
		}
		report.Volumes = append(report.Volumes, *volume)
		report.Total += volume.Total
		report.Solved += volume.Solved
		report.Published += volume.Published
		report.Verified += volume.Verified
		report.Imported += volume.Imported
		report.Missing += volume.Missing
		report.Orphans += volume.Orphans
	}
	return report, nil
}

// Queue is the flat work list, ordered by section then number. Consecutive runs
// over an unchanged tree give the same order, so a run that stops halfway and
// restarts picks up where it was without a cursor.
func (r Report) Queue() []Target {
	var out []Target
	for _, volume := range r.Volumes {
		for _, section := range volume.Sections {
			for _, number := range section.Missing {
				out = append(out, Target{Section: section.ID, Number: number})
			}
		}
	}
	return out
}

// Target is one exercise to solve.
type Target struct {
	Section string `json:"section"`
	Number  int    `json:"number"`
}

func (t Target) String() string { return fmt.Sprintf("%s %d", t.Section, t.Number) }

// Gaps returns a volume's sections that are missing something, worst first.
// Ties break by section order so the list is stable.
func (v Volume) Gaps() []Section {
	var out []Section
	for _, section := range v.Sections {
		if len(section.Missing) > 0 {
			out = append(out, section)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Missing) != len(out[j].Missing) {
			return len(out[i].Missing) > len(out[j].Missing)
		}
		return exercise.CompareSections(out[i].ID, out[j].ID) < 0
	})
	return out
}

func (s Scanner) sections(filter Filter) ([]string, error) {
	if section := strings.TrimSpace(filter.Section); section != "" {
		return []string{section}, nil
	}
	all, err := s.Repository.Sections()
	if err != nil {
		return nil, err
	}
	dir, err := NormalizeVolume(filter.Volume)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return all, nil
	}
	var out []string
	for _, id := range all {
		if exercise.VolumeDir(id) == dir {
			out = append(out, id)
		}
	}
	return out, nil
}

// NormalizeVolume turns what someone typed into a fascicle directory. An empty
// value means every volume.
func NormalizeVolume(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "vol") {
		value = "vol" + value
	}
	for _, dir := range VolumeDirs {
		if dir == value {
			return dir, nil
		}
	}
	return "", fmt.Errorf("unknown volume %q, want one of %s", value, strings.Join(VolumeDirs, ", "))
}

func (s Scanner) section(id string) (Section, error) {
	section := Section{
		ID:           id,
		VolumeDir:    exercise.VolumeDir(id),
		VolumeNumber: publish.VolumeNumber(id),
	}
	// A section directory that cannot be read is a section with no exercises
	// rather than an error. Several live sections have an empty source directory
	// and the report should still show what is published under them.
	numbers, err := s.Repository.List(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Section{}, err
	}
	published, err := s.published(section)
	if err != nil {
		return Section{}, err
	}
	section.Total = len(numbers)
	for _, number := range numbers {
		solved, err := s.solved(id, number)
		if err != nil {
			return Section{}, err
		}
		_, live := published[number]
		switch {
		case solved:
			section.Solved++
		case live:
			// A published page with no result JSON is a proof that predates this
			// tool's store. Queueing it would spend a model on work that already
			// exists, so it is counted apart rather than called missing.
			section.Imported++
		default:
			section.Missing = append(section.Missing, number)
		}
	}
	known := map[int]bool{}
	for _, number := range numbers {
		known[number] = true
	}
	for _, number := range sortedKeys(published) {
		section.Published++
		if published[number] {
			section.Verified++
		}
		if !known[number] {
			section.Orphans = append(section.Orphans, number)
		}
	}
	return section, nil
}

// stored is the part of a result JSON coverage needs. Decoding into this rather
// than result.Result skips the candidates, reviews, and per-attempt metrics,
// which are the bulk of a file that can run to hundreds of kilobytes.
type stored struct {
	Solution string `json:"solution_md"`
}

func (s Scanner) solved(section string, number int) (bool, error) {
	raw, err := os.ReadFile(s.Store.JSONPath(section, number))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var value stored
	if err := json.Unmarshal(raw, &value); err != nil {
		// A truncated or hand edited result is not solved, and saying so is more
		// useful than aborting a scan of six thousand exercises.
		return false, nil
	}
	return strings.TrimSpace(value.Solution) != "", nil
}

// published maps each published exercise number to whether it is verified.
func (s Scanner) published(section Section) (map[int]bool, error) {
	out := map[int]bool{}
	if s.Brain == "" {
		return out, nil
	}
	dir := filepath.Join(s.Brain, fmt.Sprintf("vol%d", section.VolumeNumber), section.ID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list published section %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "_") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(name, ".md"))
		if err != nil {
			continue
		}
		verified, err := verified(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out[number] = verified
	}
	return out, nil
}

// verified reads the frontmatter flag. A page with no frontmatter is published
// but unverified, which is what the Python era left behind in places.
func verified(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return false, nil
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return false, nil
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "verified" {
			continue
		}
		return strings.TrimSpace(value) == "true", nil
	}
	return false, nil
}

func sortedKeys(values map[int]bool) []int {
	out := make([]int, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Ints(out)
	return out
}
