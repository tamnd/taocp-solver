package result

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tamnd/taocp-solver/exercise"
)

type Attempt struct {
	Phase        string `json:"phase"`
	Iteration    int    `json:"iteration"`
	ResponseID   string `json:"response_id,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
}

type Review struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Verdict string `json:"verdict"`
	Score   int    `json:"score,omitempty"`
}

type Result struct {
	ID          string            `json:"id"`
	Exercise    exercise.Exercise `json:"exercise"`
	Solution    string            `json:"solution_md"`
	Review      string            `json:"review_md"`
	Reviews     []Review          `json:"reviews"`
	Verdict     string            `json:"verdict"`
	Verified    bool              `json:"verified"`
	Model       string            `json:"model"`
	SolveTime   time.Duration     `json:"solve_time"`
	CompletedAt time.Time         `json:"completed_at"`
	Attempts    []Attempt         `json:"attempts"`
}

type Store struct {
	Root string
}

func (s Store) Load(section string, number int) (Result, error) {
	path := s.JSONPath(section, number)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var value Result
	if err := json.Unmarshal(data, &value); err != nil {
		return Result{}, fmt.Errorf("decode cached result %s: %w", path, err)
	}
	return value, nil
}

func (s Store) Exists(section string, number int) bool {
	_, err := os.Stat(s.JSONPath(section, number))
	return err == nil
}

func (s Store) Save(value Result) error {
	if s.Root == "" {
		return errors.New("output root is empty")
	}
	dir := filepath.Dir(s.JSONPath(value.Exercise.SectionID, value.Exercise.Number))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWrite(s.JSONPath(value.Exercise.SectionID, value.Exercise.Number), data, 0o644); err != nil {
		return err
	}
	header := fmt.Sprintf("# TAOCP %s Exercise %d\n\n", value.Exercise.SectionID, value.Exercise.Number)
	if err := atomicWrite(s.MarkdownPath(value.Exercise.SectionID, value.Exercise.Number), []byte(header+value.Solution+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

func (s Store) JSONPath(section string, number int) string {
	return filepath.Join(s.Root, section, fmt.Sprintf("%02d.json", number))
}

func (s Store) MarkdownPath(section string, number int) string {
	return filepath.Join(s.Root, section, fmt.Sprintf("%02d.md", number))
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary result: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary result: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary result: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary result: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace result %s: %w", path, err)
	}
	return nil
}
