package textguard

import (
	"errors"
	"regexp"
	"strings"
)

var (
	brandMarker = regexp.MustCompile(`(?i)\bopen\s*ai\b|\bchat\s*gpt\b|\bgpt[- ]?[0-9]|\bas an? (?:ai|language model)\b`)
	ruleLine    = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	verdictLine = regexp.MustCompile(`(?m)^VERDICT:\s*(PASS|FAIL)\s*$`)
	scoreLine   = regexp.MustCompile(`(?m)^SCORE:\s*([0-7])/7\s*$`)
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
