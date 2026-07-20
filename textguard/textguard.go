package textguard

import (
	"errors"
	"regexp"
	"strings"
)

var (
	brandMarker       = regexp.MustCompile(`(?i)\bopen\s*ai\b|\bchat\s*gpt\b|\bgpt[- ]?[0-9]|\bas an? (?:ai|language model)\b`)
	ruleLine          = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	verdictLine       = regexp.MustCompile(`(?m)^VERDICT:\s*(PASS|FAIL)\s*$`)
	scoreLine         = regexp.MustCompile(`(?m)^SCORE:\s*([0-7])/7\s*$`)
	truthLine         = regexp.MustCompile(`(?m)^TRUTH:\s*(TRUE|FALSE)\s*$`)
	completeLine      = regexp.MustCompile(`(?m)^COMPLETE:\s*(YES|NO)\s*$`)
	selfContainedLine = regexp.MustCompile(`(?m)^SELF_CONTAINED:\s*(YES|NO)\s*$`)
	humanReadableLine = regexp.MustCompile(`(?m)^HUMAN_READABLE:\s*(YES|NO)\s*$`)
	verifiableLine    = regexp.MustCompile(`(?m)^VERIFIABLE:\s*(YES|NO)\s*$`)
	selectedLine      = regexp.MustCompile(`(?m)^SELECTED:\s*([1-5])\s*$`)
)

var ErrNonAnswer = errors.New("response is empty or not a publishable solution")

func CleanSolution(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || brandMarker.MatchString(value) {
		return "", ErrNonAnswer
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\f", "")
	value = strings.ReplaceAll(value, "\u2014", ",")
	value = strings.ReplaceAll(value, "\u2013", "-")
	value = ruleLine.ReplaceAllString(value, "")
	value = collapseBlankLines(value)
	return strings.TrimSpace(value), nil
}

func CleanGeneratedText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\f", "")
	value = strings.ReplaceAll(value, "\u2014", ",")
	value = strings.ReplaceAll(value, "\u2013", "-")
	return strings.TrimSpace(collapseBlankLines(value))
}

func Verdict(review string) string {
	matches := verdictLine.FindAllStringSubmatch(review, -1)
	if len(matches) == 0 {
		return "UNKNOWN"
	}
	return matches[len(matches)-1][1]
}

func Score(review string) int {
	matches := scoreLine.FindAllStringSubmatch(review, -1)
	if len(matches) == 0 {
		return -1
	}
	return int(matches[len(matches)-1][1][0] - '0')
}

type Assessment struct {
	Truth         bool
	Complete      bool
	SelfContained bool
	HumanReadable bool
	Verifiable    bool
	HasTruth      bool
	HasQuality    bool
}

func ParseAssessment(review string) Assessment {
	truth, hasTruth := lastBoolean(review, truthLine, "TRUE")
	complete, hasComplete := lastBoolean(review, completeLine, "YES")
	selfContained, hasSelfContained := lastBoolean(review, selfContainedLine, "YES")
	humanReadable, hasHumanReadable := lastBoolean(review, humanReadableLine, "YES")
	verifiable, hasVerifiable := lastBoolean(review, verifiableLine, "YES")
	return Assessment{
		Truth: truth, Complete: complete, SelfContained: selfContained,
		HumanReadable: humanReadable, Verifiable: verifiable,
		HasTruth:   hasTruth,
		HasQuality: hasComplete && hasSelfContained && hasHumanReadable && hasVerifiable,
	}
}

func SelectedCandidate(review string) int {
	matches := selectedLine.FindAllStringSubmatch(review, -1)
	if len(matches) == 0 {
		return 0
	}
	return int(matches[len(matches)-1][1][0] - '0')
}

func lastBoolean(value string, pattern *regexp.Regexp, affirmative string) (bool, bool) {
	matches := pattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return false, false
	}
	return matches[len(matches)-1][1] == affirmative, true
}

func ValidateRepositoryText(value string) error {
	if strings.ContainsRune(value, '\u2014') {
		return errors.New("text contains an em dash")
	}
	if strings.ContainsRune(value, '\f') {
		return errors.New("text contains a form feed")
	}
	return nil
}

func collapseBlankLines(value string) string {
	for strings.Contains(value, "\n\n\n") {
		value = strings.ReplaceAll(value, "\n\n\n", "\n\n")
	}
	return value
}
