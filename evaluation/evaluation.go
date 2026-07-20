// Package evaluation compares generated solutions with existing study material.
package evaluation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/textguard"
)

var winnerLine = regexp.MustCompile(`(?m)^WINNER:\s*(A|B|TIE)\s*$`)

type Judgment struct {
	Order  string `json:"order"`
	Winner string `json:"winner"`
	Review string `json:"review"`
}

type Report struct {
	Section    string     `json:"section"`
	Number     int        `json:"number"`
	Winner     string     `json:"winner"`
	Judgments  []Judgment `json:"judgments"`
	CandidateA string     `json:"candidate_a"`
	CandidateB string     `json:"candidate_b"`
}

type Comparator struct {
	Repository *exercise.Repository
	Client     api.Completer
	Prompts    prompt.Builder
	Model      string
}

func (c *Comparator) Compare(ctx context.Context, section string, number int, candidate, baseline string) (Report, error) {
	if c.Repository == nil || c.Client == nil {
		return Report{}, errors.New("comparator is not configured")
	}
	ex, sourceContext, err := c.Repository.Load(section, number)
	if err != nil {
		return Report{}, err
	}
	first, err := c.judge(ctx, ex, sourceContext, candidate, baseline, "candidate,baseline")
	if err != nil {
		return Report{}, err
	}
	second, err := c.judge(ctx, ex, sourceContext, baseline, candidate, "baseline,candidate")
	if err != nil {
		return Report{}, err
	}
	firstMapped := mapWinner(first.Winner, false)
	secondMapped := mapWinner(second.Winner, true)
	winner := "TIE"
	if firstMapped == secondMapped {
		winner = firstMapped
	}
	return Report{
		Section: section, Number: number, Winner: winner,
		Judgments:  []Judgment{first, second},
		CandidateA: "new", CandidateB: "brain",
	}, nil
}

func (c *Comparator) judge(ctx context.Context, ex exercise.Exercise, sourceContext exercise.Context, first, second, order string) (Judgment, error) {
	instructions, input := c.Prompts.Compare(ex, sourceContext, first, second)
	response, err := c.Client.Complete(ctx, api.Request{
		Model: c.Model, Instructions: instructions, Input: input, Effort: "high",
		Metadata: map[string]string{"task": "taocp-comparison", "exercise": fmt.Sprintf("%s.%d", ex.SectionID, ex.Number)},
	})
	if err != nil {
		return Judgment{}, fmt.Errorf("compare %s: %w", order, err)
	}
	review := textguard.CleanGeneratedText(response.Text)
	winner := parseWinner(review)
	if winner == "UNKNOWN" {
		return Judgment{}, fmt.Errorf("comparison for order %s has no valid winner", order)
	}
	return Judgment{Order: order, Winner: winner, Review: review}, nil
}

func LoadBrainSolution(root string, volume int, section string, number int) (string, string, error) {
	path := filepath.Join(root, fmt.Sprintf("vol%d", volume), section, fmt.Sprintf("%02d.md", number))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", path, fmt.Errorf("read brain solution: %w", err)
	}
	text := string(data)
	if strings.HasPrefix(text, "---\n") {
		if index := strings.Index(text[4:], "\n---\n"); index >= 0 {
			text = text[index+9:]
		}
	}
	parts := regexp.MustCompile(`(?m)^---\s*$`).Split(text, -1)
	if len(parts) > 1 {
		text = parts[len(parts)-1]
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", path, errors.New("brain solution is empty")
	}
	return text, path, nil
}

func parseWinner(review string) string {
	matches := winnerLine.FindAllStringSubmatch(review, -1)
	if len(matches) == 0 {
		return "UNKNOWN"
	}
	return matches[len(matches)-1][1]
}

func mapWinner(winner string, reversed bool) string {
	if winner == "TIE" {
		return "TIE"
	}
	if reversed {
		if winner == "A" {
			return "brain"
		}
		return "new"
	}
	if winner == "A" {
		return "new"
	}
	return "brain"
}
