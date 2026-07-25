package exercise

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRepositoryLoadAndList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	volume := filepath.Join(root, "content", "vol1")
	dir := filepath.Join(volume, "exercises", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(volume, "01_1_1_basics.md"), "---\ntitle: Basics\n---\nSection body.\n\n## Exercises\nIgnored.")
	mustWrite(t, filepath.Join(dir, "01.md"), "---\nsection: 1.1\nexercise: 1\nrating: M10\nrecommended: true\n---\nFirst body.")
	mustWrite(t, filepath.Join(dir, "02_01.md"), "---\nsection: 1.1\nexercise: 2\nvolume: 1\nsection_title: Basics\n---\nSecond body.")
	mustWrite(t, filepath.Join(dir, "02_02.md"), "duplicate occurrence")

	repository := NewRepository(root)
	ex, context, err := repository.Load("1.1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Body != "Second body." || ex.SectionTitle != "Basics" || ex.SourcePath == "" {
		t.Fatalf("exercise = %+v", ex)
	}
	if context.Section != "Section body." || context.Preceding != "First body." {
		t.Fatalf("context = %+v", context)
	}
	numbers, err := repository.List("1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2}) {
		t.Fatalf("numbers = %v", numbers)
	}
}

func TestVolumeDir(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"1.2.3":   "vol1",
		"4.6.1":   "vol2",
		"5.2":     "vol3",
		"7.1":     "vol4a",
		"7.2.2.1": "vol4b",
		"7.2.2.2": "vol4f6",
	}
	for section, want := range tests {
		if got := VolumeDir(section); got != want {
			t.Errorf("VolumeDir(%q) = %q, want %q", section, got, want)
		}
	}
}

// Metadata is what the index pages read, so it must not drag in the section and
// preceding context that Load gathers for a prompt.
func TestMetadataReadsFrontmatterOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "content", "vol1", "exercises", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "01.md"), "---\nsection: 1.1\nexercise: 1\nrating: M10\ncategory: math-simple\nrecommended: true\n---\nFirst body.")
	mustWrite(t, filepath.Join(dir, "02.md"), "---\nsection: 1.1\nexercise: 2\nrating: \"20\"\n---\nSecond body.")

	items, err := NewRepository(root).Metadata("1.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Rating != "M10" || items[0].Category != "math-simple" || !items[0].Recommended {
		t.Errorf("first item = %+v", items[0])
	}
	if items[1].Number != 2 || items[1].Recommended {
		t.Errorf("second item = %+v", items[1])
	}
}

func TestSectionsAcrossVolumeDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, dir := range []string{"vol1/exercises/1.2.9", "vol1/exercises/1.2.10", "vol4a/exercises/7.2.1.1", "vol4b/exercises/7.2.2", "vol1/exercises/notes"} {
		if err := os.MkdirAll(filepath.Join(root, "content", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sections, err := NewRepository(root).Sections()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1.2.9", "1.2.10", "7.2.1.1", "7.2.2"}
	if !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections = %v, want %v", sections, want)
	}
}

func TestCompareSectionsCountsRatherThanSpells(t *testing.T) {
	t.Parallel()
	if CompareSections("1.2.9", "1.2.10") >= 0 {
		t.Error("1.2.9 must come before 1.2.10")
	}
	if CompareSections("7.2.1.1", "7.2.2") >= 0 {
		t.Error("7.2.1.1 must come before 7.2.2")
	}
	if CompareSections("7.2.2", "7.2.2.1") >= 0 {
		t.Error("a section must come before its subsections")
	}
	if CompareSections("1.1", "1.1") != 0 {
		t.Error("a section must equal itself")
	}
}

func TestParseReference(t *testing.T) {
	t.Parallel()
	section, number, err := ParseReference("1.2.6.10")
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}
