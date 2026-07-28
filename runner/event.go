package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"
)

// Event kinds. A run's whole log is these few lines, which is deliberate: a
// person reading a week of output should be able to grep it without a manual.
const (
	KindQueue   = "queue"
	KindStart   = "start"
	KindStep    = "step"
	KindSolve   = "solve"
	KindPublish = "publish"
	KindCommit  = "commit"
	KindRoute   = "route"
	KindSleep   = "sleep"
	KindError   = "error"
)

// Event is one line of a run's log. The same value renders as text for a person
// reading a screen session and as JSON for anything reading the file later, so
// the two can never drift apart.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"event"`
	Section string    `json:"section,omitempty"`
	Number  int       `json:"number,omitempty"`
	Route   string    `json:"route,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	Verdict string    `json:"verdict,omitempty"`
	Truth   *bool     `json:"truth,omitempty"`
	Elapsed string    `json:"elapsed,omitempty"`
	Tokens  int       `json:"tokens,omitempty"`
	Cost    float64   `json:"cost,omitempty"`
	Files   int       `json:"files,omitempty"`
	Indexes int       `json:"indexes,omitempty"`
	Message string    `json:"message,omitempty"`
}

func (e Event) String() string {
	line := e.Time.Format(time.RFC3339) + " " + e.Kind
	if e.Section != "" {
		line += fmt.Sprintf(" %s/%d", e.Section, e.Number)
	}
	if e.Kind == KindCommit {
		line += fmt.Sprintf(" %d files", e.Files)
	}
	if e.Message != "" {
		line += " " + e.Message
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"route", e.Route},
		{"mode", e.Mode},
		{"verdict", e.Verdict},
		{"truth", boolText(e.Truth)},
		{"time", e.Elapsed},
	} {
		if field.value != "" {
			line += " " + field.name + "=" + field.value
		}
	}
	if e.Tokens > 0 {
		line += fmt.Sprintf(" tokens=%d", e.Tokens)
	}
	if e.Cost > 0 {
		line += fmt.Sprintf(" cost=$%.2f", e.Cost)
	}
	if e.Indexes > 0 {
		line += fmt.Sprintf(" indexes=%d", e.Indexes)
	}
	return line
}

func boolText(value *bool) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%t", *value)
}

// Logger writes events, one per line, as text or as JSON. It is safe to call
// from every worker at once, which matters because parallel solves interleave,
// and it writes synchronously so a killed run loses nothing it had reported.
func Logger(out io.Writer, asJSON bool) func(Event) {
	var mu sync.Mutex
	encoder := json.NewEncoder(out)
	return func(event Event) {
		mu.Lock()
		defer mu.Unlock()
		if asJSON {
			_ = encoder.Encode(event)
			return
		}
		_, _ = fmt.Fprintln(out, event)
	}
}

// sleep waits, but wakes early when the run is cancelled. Nothing in an
// unattended runner should sit through an hour-long quota wait after someone
// has pressed Ctrl-C.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func jitter(low, high time.Duration) time.Duration {
	if high <= low {
		return low
	}
	return low + time.Duration(rand.Int64N(int64(high-low)))
}

// acquire takes the advisory lock and returns the release. It retries rather
// than blocking in the kernel so a cancelled run stops waiting.
func acquire(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	for {
		taken, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if taken {
			return func() {
				_ = unlock(file)
				_ = file.Close()
			}, nil
		}
		if err := sleep(ctx, 200*time.Millisecond); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
}

// oneLine flattens an error for a log line, because a git failure arrives with
// the whole of stderr in it and a log with embedded newlines is unreadable.
func oneLine(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return text
}
