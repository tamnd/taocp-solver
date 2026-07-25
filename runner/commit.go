package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultLock is the advisory lock the content repository's git sequence runs
// under. It is a fixed path rather than something under the repository so that
// a second runner, or one of the shell scripts this replaces, contends for the
// same file.
const DefaultLock = "/tmp/brain_git.lock"

// Committer owns every git command that touches the content repository. It is
// the only part of the runner that writes to a repository it does not own, so
// it does one thing: add what is there, describe it, and get it onto the
// remote. It never resets, never checks out, and never discards work.
type Committer struct {
	Repo   string
	Lock   string
	Remote string
	Branch string
	// Git runs one git command and returns its combined output. Tests replace
	// it only to observe; the committer's own tests drive real git.
	Git func(ctx context.Context, dir string, args ...string) (string, error)
	// Jitter spaces out the push rounds so two hosts pushing to the same remote
	// do not collide on every retry.
	Jitter func() time.Duration
	Sleep  func(ctx context.Context, d time.Duration) error
	Now    func() time.Time
	Log    func(Event)
}

// NewCommitter builds a committer over a working copy with the usual defaults.
func NewCommitter(repo string) *Committer {
	return &Committer{Repo: repo, Lock: DefaultLock, Remote: "origin", Branch: "main"}
}

// Commit stages everything in the working copy, commits it under a generated
// message, and pushes. It reports how many solution files went in. Zero means
// there was nothing to do, which is the common case on a quiet tick.
func (c *Committer) Commit(ctx context.Context) (int, error) {
	release, err := acquire(ctx, c.lockPath())
	if err != nil {
		return 0, err
	}
	defer release()

	status, err := c.git(ctx, "status", "--porcelain")
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(status) == "" {
		return 0, nil
	}
	if _, err := c.git(ctx, "add", "-A"); err != nil {
		return 0, err
	}
	staged, err := c.git(ctx, "diff", "--cached", "--name-status")
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(staged) == "" {
		// Everything the working copy showed was already staged elsewhere, or
		// was ignored. Committing an empty tree would be noise.
		return 0, nil
	}
	message, files := CommitMessage(staged)
	if _, err := c.git(ctx, "commit", "-m", message); err != nil {
		return 0, err
	}
	if err := c.push(ctx); err != nil {
		// The commit is made and safe in the local history. A failed push is
		// worth reporting but is not worth losing the commit over, and the next
		// tick will try again.
		return files, err
	}
	c.log(Event{Kind: KindCommit, Files: files, Message: "pushed"})
	return files, nil
}

// push brings the branch up to date and sends it, retrying a few times because
// two hosts publishing into the same repository will sometimes race.
func (c *Committer) push(ctx context.Context) error {
	var last error
	for round := 0; round < 3; round++ {
		if round > 0 {
			if err := c.sleep(ctx, c.jitter()); err != nil {
				return err
			}
		}
		if _, err := c.git(ctx, "fetch", c.remote()); err != nil {
			last = err
			continue
		}
		// Ours is the newer render of the same page, so where the two disagree
		// the local side wins. A merge that stopped for a conflict would leave
		// an unattended runner wedged.
		merge := fmt.Sprintf("%s/%s", c.remote(), c.branch())
		if _, err := c.git(ctx, "merge", "-X", "theirs", merge, "-m", "Merge brain [auto]"); err != nil {
			last = err
			continue
		}
		if _, err := c.git(ctx, "push", c.remote(), c.branch()); err != nil {
			last = err
			continue
		}
		return nil
	}
	return last
}

// CommitMessage describes a staged change set the way the shell script this
// replaces did, because the content repository's history should not visibly
// change hands. It returns the message and the number of solution files in it.
//
// The input is `git diff --cached --name-status` output.
func CommitMessage(nameStatus string) (string, int) {
	added, updated := 0, 0
	counts := map[string]int{}
	for _, line := range strings.Split(nameStatus, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		status, path := fields[0], fields[len(fields)-1]
		if filepath.Ext(path) != ".md" {
			continue
		}
		// Index pages are regenerated whenever anything near them moves, so
		// counting them would report five files for a single new solution.
		if filepath.Base(path) == "_index.md" {
			continue
		}
		if strings.HasPrefix(status, "A") {
			added++
		} else {
			updated++
		}
		counts[groupOf(path)]++
	}

	total := added + updated
	if total == 0 {
		return "Update site content [auto]", 0
	}
	var title string
	switch {
	case added > 0 && updated > 0:
		title = fmt.Sprintf("Add %d, update %d solutions [auto]", added, updated)
	case added > 0:
		title = fmt.Sprintf("Add %d solutions [auto]", added)
	default:
		title = fmt.Sprintf("Update %d solutions [auto]", updated)
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	body := make([]string, 0, len(names))
	for _, name := range names {
		body = append(body, fmt.Sprintf("  %s: %d files", name, counts[name]))
	}
	return title + "\n\n" + strings.Join(body, "\n") + "\n", total
}

// groupOf names the collection a published page belongs to, as the history
// spells it: "taocp 7.2.1.3", "codeforces 103604". Volume directories are an
// artefact of how the pages are laid out rather than something a reader of the
// history cares about, so they drop out.
func groupOf(path string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(path)), "/")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || isVolumeDir(part) {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return "content"
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return strings.Join(kept[len(kept)-2:], " ")
}

func isVolumeDir(part string) bool {
	if !strings.HasPrefix(part, "vol") || len(part) < 4 {
		return false
	}
	return part[3] >= '0' && part[3] <= '9'
}

func (c *Committer) git(ctx context.Context, args ...string) (string, error) {
	if c.Git != nil {
		return c.Git(ctx, c.Repo, args...)
	}
	return RunGit(ctx, c.Repo, args...)
}

// RunGit is the default git driver: one command, combined output, errors
// carrying enough of that output to diagnose from a log file.
func RunGit(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	// An unattended runner has no terminal to answer a credential prompt on, so
	// a missing credential should fail rather than hang.
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (c *Committer) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleep(ctx, d)
}

func (c *Committer) jitter() time.Duration {
	if c.Jitter != nil {
		return c.Jitter()
	}
	return jitter(2*time.Second, 7*time.Second)
}

func (c *Committer) lockPath() string {
	if strings.TrimSpace(c.Lock) != "" {
		return c.Lock
	}
	return DefaultLock
}

func (c *Committer) remote() string {
	if strings.TrimSpace(c.Remote) != "" {
		return c.Remote
	}
	return "origin"
}

func (c *Committer) branch() string {
	if strings.TrimSpace(c.Branch) != "" {
		return c.Branch
	}
	return "main"
}

func (c *Committer) log(event Event) {
	if c.Log == nil {
		return
	}
	if event.Time.IsZero() {
		if c.Now != nil {
			event.Time = c.Now()
		} else {
			event.Time = time.Now()
		}
	}
	c.Log(event)
}
