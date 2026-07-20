package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	section, number, err := parseTarget([]string{"1.2.6", "10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
	section, number, err = parseTarget([]string{"1.2.6.10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "taocp solve") || !strings.Contains(output.String(), "taocp compare") {
		t.Fatalf("help = %q", output.String())
	}
}
