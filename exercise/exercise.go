package exercise

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const defaultContextLimit = 12000

var exerciseFile = regexp.MustCompile(`^(\d+)(?:_(\d+))?\.md$`)

type Exercise struct {
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
	Body         string `json:"exercise_md"`
	SourcePath   string `json:"source_path"`
}

type Context struct {
	Section   string
	Preceding string
}

type Repository struct {
	Root         string
	ContextLimit int
	Preceding    int
}

func NewRepository(root string) *Repository {
	return &Repository{Root: root, ContextLimit: defaultContextLimit, Preceding: 3}
}

func VolumeDir(sectionID string) string {
	parts := strings.Split(sectionID, ".")
	chapter, err := strconv.Atoi(parts[0])
	if err != nil {
		return "vol1"
	}
	switch {
	case chapter <= 2:
		return "vol1"
	case chapter <= 4:
		return "vol2"
	case chapter <= 6:
		return "vol3"
	case strings.HasPrefix(sectionID, "7.2.2.2"):
		return "vol4f6"
	case strings.HasPrefix(sectionID, "7.2.2"):
		return "vol4b"
	default:
		return "vol4a"
	}
}

func (r *Repository) Load(sectionID string, number int) (Exercise, Context, error) {
	path, err := r.exercisePath(sectionID, number)
	if err != nil {
		return Exercise{}, Context{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Exercise{}, Context{}, fmt.Errorf("read exercise: %w", err)
	}
	meta, body := parseFrontmatter(string(data))
	ex := Exercise{
		SectionID:    stringValue(meta, "section", sectionID),
		Number:       intValue(meta, "exercise", number),
		SectionTitle: stringValue(meta, "section_title", ""),
		Chapter:      intValue(meta, "chapter", 0),
		ChapterTitle: stringValue(meta, "chapter_title", ""),
		Volume:       intValue(meta, "volume", volumeNumber(VolumeDir(sectionID))),
		BookPages:    stringValue(meta, "book_pages", ""),
		Rating:       stringValue(meta, "rating", ""),
		Category:     stringValue(meta, "category", ""),
		Recommended:  boolValue(meta, "recommended"),
		Body:         strings.TrimSpace(body),
		SourcePath:   path,
	}
	if ex.Body == "" {
		return Exercise{}, Context{}, fmt.Errorf("exercise %s.%d is empty", sectionID, number)
	}
	section, err := r.sectionContext(sectionID)
	if err != nil {
		return Exercise{}, Context{}, err
	}
	preceding, err := r.precedingContext(sectionID, number)
	if err != nil {
		return Exercise{}, Context{}, err
	}
	return ex, Context{Section: section, Preceding: preceding}, nil
}

func (r *Repository) List(sectionID string) ([]int, error) {
	dir := filepath.Join(r.Root, "content", VolumeDir(sectionID), "exercises", sectionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list exercises in %s: %w", dir, err)
	}
	seen := make(map[int]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := exerciseFile.FindStringSubmatch(entry.Name())
		if match == nil || match[2] != "" && match[2] != "01" {
			continue
		}
		n, err := strconv.Atoi(match[1])
		if err == nil {
			seen[n] = true
		}
	}
	numbers := make([]int, 0, len(seen))
	for n := range seen {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	return numbers, nil
}

func (r *Repository) exercisePath(sectionID string, number int) (string, error) {
	dir := filepath.Join(r.Root, "content", VolumeDir(sectionID), "exercises", sectionID)
	plain := filepath.Join(dir, fmt.Sprintf("%02d.md", number))
	if regularFile(plain) {
		return plain, nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%02d_*.md", number)))
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	for _, path := range matches {
		if regularFile(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("exercise not found: %s.%d under %s", sectionID, number, dir)
}

func (r *Repository) sectionContext(sectionID string) (string, error) {
	dir := filepath.Join(r.Root, "content", VolumeDir(sectionID))
	needle := "_" + strings.ReplaceAll(sectionID, ".", "_")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("list section content: %w", err)
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && strings.Contains(entry.Name(), needle) {
			candidates = append(candidates, filepath.Join(dir, entry.Name()))
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Strings(candidates)
	data, err := os.ReadFile(candidates[0])
	if err != nil {
		return "", fmt.Errorf("read section context: %w", err)
	}
	_, body := parseFrontmatter(string(data))
	if index := findExercisesHeading(body); index >= 0 {
		body = body[:index]
	}
	return trimContext(strings.TrimSpace(body), r.ContextLimit), nil
}

func (r *Repository) precedingContext(sectionID string, number int) (string, error) {
	count := r.Preceding
	if count < 0 {
		count = 0
	}
	var bodies []string
	for n := max(1, number-count); n < number; n++ {
		path, err := r.exercisePath(sectionID, n)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read preceding exercise: %w", err)
		}
		_, body := parseFrontmatter(string(data))
		if body = strings.TrimSpace(body); body != "" {
			bodies = append(bodies, body)
		}
	}
	return strings.Join(bodies, "\n\n"), nil
}

func parseFrontmatter(text string) (map[string]string, string) {
	meta := make(map[string]string)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return meta, text
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	offset := 0
	lineNumber := 0
	for scanner.Scan() {
		line := scanner.Text()
		offset += len(line) + 1
		lineNumber++
		if lineNumber == 1 {
			continue
		}
		if strings.TrimSpace(line) == "---" {
			if offset > len(text) {
				offset = len(text)
			}
			return meta, text[offset:]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if key != "" {
			meta[key] = value
		}
	}
	return map[string]string{}, text
}

func findExercisesHeading(text string) int {
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\n")) == "## Exercises" {
			return offset
		}
		offset += len(line)
	}
	return -1
}

func trimContext(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := strings.LastIndex(text[:limit], "\n\n")
	if cut < limit/2 {
		cut = limit
	}
	return strings.TrimSpace(text[:cut]) + "\n\n[Section context continues in the source.]"
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func stringValue(meta map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(meta[key]); value != "" {
		return value
	}
	return fallback
}

func intValue(meta map[string]string, key string, fallback int) int {
	n, err := strconv.Atoi(meta[key])
	if err != nil {
		return fallback
	}
	return n
}

func boolValue(meta map[string]string, key string) bool {
	b, err := strconv.ParseBool(meta[key])
	return err == nil && b
}

func volumeNumber(dir string) int {
	switch dir {
	case "vol1":
		return 1
	case "vol2":
		return 2
	case "vol3":
		return 3
	default:
		return 4
	}
}

var ErrInvalidReference = errors.New("invalid exercise reference")

func ParseReference(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	index := strings.LastIndex(value, ".")
	if index <= 0 || index == len(value)-1 {
		return "", 0, fmt.Errorf("%w: %q, want SECTION.NUMBER", ErrInvalidReference, value)
	}
	number, err := strconv.Atoi(value[index+1:])
	if err != nil || number < 1 {
		return "", 0, fmt.Errorf("%w: %q, want SECTION.NUMBER", ErrInvalidReference, value)
	}
	return value[:index], number, nil
}
