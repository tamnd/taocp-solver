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
