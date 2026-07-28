package textguard

import (
	"errors"
	"regexp"
	"strings"
)

var (
	brandMarker       = regexp.MustCompile(`(?i)\bopen\s*ai\b|\bchat\s*gpt\b|\bgpt[- ]?[0-9]|\bas an? (?:ai|language model)\b`)
	ruleLine          = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	verdictLine       = decision("VERDICT", `PASS|FAIL`)
	scoreLine         = decision("SCORE", `[0-7]`, `\s*/\s*7`)
	truthLine         = decision("TRUTH", `TRUE|FALSE`)
	completeLine      = decision("COMPLETE", `YES|NO`)
	selfContainedLine = decision("SELF_CONTAINED", `YES|NO`)
	humanReadableLine = decision("HUMAN_READABLE", `YES|NO`)
	verifiableLine    = decision("VERIFIABLE", `YES|NO`)
	selectedLine      = decision("SELECTED", `[1-5]`)
)

// decision matches one decision line. The prompts ask for the verdict on a line
// of its own, and the models are not all equally obedient about that: a weaker
// one bolds it, bullets it, quotes it, or ends it with a full stop. None of
// that changes what it decided, and throwing away a whole solve over a pair of
// asterisks is expensive.
//
// The decision must still be the whole line. A gate that accepted a verdict
// mentioned in passing would not be a gate.
func decision(key, values string, suffix ...string) *regexp.Regexp {
	const mark = "[\\s*_`>#-]*"
	tail := ""
	if len(suffix) > 0 {
		tail = suffix[0]
	}
	return regexp.MustCompile(`(?m)^` + mark + key + `:` + mark + `(` + values + `)` + tail + mark + `[.]?` + mark + `$`)
}

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
