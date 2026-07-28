package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheCommitMessageMatchesTheHistoryItInherits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		nameStatus string
		want       string
		files      int
	}{
		{
			name: "two new solutions and the indexes they moved",
			nameStatus: strings.Join([]string{
				"M\tcontent/en/practice/maths/taocp/_index.md",
				"A\tcontent/en/practice/maths/taocp/vol4/7.2.1.3/09.md",
				"A\tcontent/en/practice/maths/taocp/vol4/7.2.1.3/10.md",
				"M\tcontent/en/practice/maths/taocp/vol4/7.2.1.3/_index.md",
				"M\tcontent/en/practice/maths/taocp/vol4/_index.md",
			}, "\n"),
			want:  "Add 2 solutions [auto]\n\n  taocp 7.2.1.3: 2 files\n",
			files: 2,
		},
		{
			name: "one added and three rewritten",
			nameStatus: strings.Join([]string{
				"A\tcontent/en/practice/codeforces/103604/D.md",
				"M\tcontent/en/practice/codeforces/103604/A.md",
				"M\tcontent/en/practice/codeforces/103604/B.md",
				"M\tcontent/en/practice/codeforces/103604/C.md",
			}, "\n"),
			want:  "Add 1, update 3 solutions [auto]\n\n  codeforces 103604: 4 files\n",
			files: 4,
		},
		{
			name:       "only updates",
			nameStatus: "M\tcontent/en/practice/maths/taocp/vol1/1.2.1/08.md",
			want:       "Update 1 solutions [auto]\n\n  taocp 1.2.1: 1 files\n",
			files:      1,
		},
		{
			name: "several collections, sorted",
			nameStatus: strings.Join([]string{
				"A\tcontent/en/practice/maths/taocp/vol3/5.2.4/12.md",
				"A\tcontent/en/practice/codeforces/103604/D.md",
			}, "\n"),
			want:  "Add 2 solutions [auto]\n\n  codeforces 103604: 1 files\n  taocp 5.2.4: 1 files\n",
			files: 2,
		},
		{
			// A rebuild that only touched index pages is real work, but calling
			// it "Add 0 solutions" would be a lie in the history.
			name: "indexes only",
			nameStatus: strings.Join([]string{
				"M\tcontent/en/practice/maths/taocp/_index.md",
				"M\tcontent/en/practice/maths/taocp/vol4/_index.md",
			}, "\n"),
			want:  "Update site content [auto]",
			files: 0,
		},
		{
			name:       "a figure that travelled with a page does not count as a solution",
			nameStatus: "A\tcontent/en/practice/maths/taocp/vol3/5.2.4/fig-1.png",
			want:       "Update site content [auto]",
			files:      0,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			message, files := CommitMessage(item.nameStatus)
			if message != item.want {
				t.Fatalf("message =\n%q\nwant\n%q", message, item.want)
			}
			if files != item.files {
				t.Fatalf("files = %d, want %d", files, item.files)
			}
		})
	}
}

// scratch builds a working copy with a local bare remote, which is enough to
// exercise the whole fetch, merge, push sequence without a network.
func scratch(t *testing.T) (repo string, remote string) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		if _, err := os.Stat("/usr/local/bin/git"); err != nil {
			t.Skip("git is not installed")
		}
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	repo = filepath.Join(root, "work")
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "--bare", "--initial-branch=main", remote)
	mustGit(t, ctx, root, "clone", remote, repo)
	mustGit(t, ctx, repo, "config", "user.email", "runner@example.test")
	mustGit(t, ctx, repo, "config", "user.name", "Runner")
	write(t, filepath.Join(repo, "README.md"), "start\n")
	mustGit(t, ctx, repo, "add", "-A")
	mustGit(t, ctx, repo, "commit", "-m", "start")
	mustGit(t, ctx, repo, "push", "origin", "main")
	return repo, remote
}

func mustGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	out, err := RunGit(ctx, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func committerFor(t *testing.T, repo string) *Committer {
	t.Helper()
	c := NewCommitter(repo)
	// The real lock lives in /tmp and is shared on purpose. A test must not
	// contend with a runner that happens to be going on the same machine.
	c.Lock = filepath.Join(t.TempDir(), "git.lock")
	c.Jitter = func() time.Duration { return 0 }
	return c
}

func TestACommitLandsOnTheRemote(t *testing.T) {
	t.Parallel()
	repo, remote := scratch(t)
	write(t, filepath.Join(repo, "content/en/practice/maths/taocp/vol3/5.2.4/12.md"), "a proof\n")
	files, err := committerFor(t, repo).Commit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("files = %d, want 1", files)
	}
	log := mustGit(t, context.Background(), remote, "log", "--format=%s", "-n", "1")
	if strings.TrimSpace(log) != "Add 1 solutions [auto]" {
		t.Fatalf("remote head = %q", strings.TrimSpace(log))
	}
}

func TestNothingToCommitIsNotAFailure(t *testing.T) {
	t.Parallel()
	repo, _ := scratch(t)
	files, err := committerFor(t, repo).Commit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("files = %d, want 0 on a clean tree", files)
	}
}

func TestTheRemoteMovingFirstIsMergedRatherThanRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, remote := scratch(t)

	// Another host publishes the same page and gets there first.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, ctx, filepath.Dir(other), "clone", remote, other)
	mustGit(t, ctx, other, "config", "user.email", "other@example.test")
	mustGit(t, ctx, other, "config", "user.name", "Other")
	write(t, filepath.Join(other, "content/en/practice/maths/taocp/vol3/5.2.4/12.md"), "their proof\n")
	mustGit(t, ctx, other, "add", "-A")
	mustGit(t, ctx, other, "commit", "-m", "theirs")
	mustGit(t, ctx, other, "push", "origin", "main")

	write(t, filepath.Join(repo, "content/en/practice/maths/taocp/vol3/5.2.4/12.md"), "our proof\n")
	if _, err := committerFor(t, repo).Commit(ctx); err != nil {
		t.Fatal(err)
	}

	check := filepath.Join(t.TempDir(), "check")
	mustGit(t, ctx, filepath.Dir(check), "clone", remote, check)
	body, err := os.ReadFile(filepath.Join(check, "content/en/practice/maths/taocp/vol3/5.2.4/12.md"))
	if err != nil {
		t.Fatal(err)
	}
	// -X theirs resolves in favour of the branch being merged in, which from
	// inside the merge is the remote side. What matters for the runner is that
	// the merge completed without stopping for a person.
	if len(body) == 0 {
		t.Fatal("the merged page is empty")
	}
	log := mustGit(t, ctx, check, "log", "--format=%s", "-n", "3")
	if !strings.Contains(log, "Merge brain [auto]") {
		t.Fatalf("no automatic merge in the history:\n%s", log)
	}
}

func TestTheLockIsHeldForTheWholeGitSequence(t *testing.T) {
	t.Parallel()
	repo, _ := scratch(t)
	lock := filepath.Join(t.TempDir(), "git.lock")

	inside := make(chan struct{})
	release := make(chan struct{})
	slow := committerFor(t, repo)
	slow.Lock = lock
	slow.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "status" {
			close(inside)
			<-release
		}
		return RunGit(ctx, dir, args...)
	}
	write(t, filepath.Join(repo, "content/en/practice/maths/taocp/vol3/5.2.4/12.md"), "a proof\n")

	go func() {
		_, _ = slow.Commit(context.Background())
	}()
	<-inside

	// A second holder must not get in while the first is mid-sequence.
	blocked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		done, err := acquire(ctx, lock)
		if err == nil {
			done()
		}
		blocked <- err
	}()
	if err := <-blocked; err == nil {
		t.Fatal("the lock was handed out while the git sequence was running")
	}
	close(release)
}

func TestAGroupNameDropsTheVolumeDirectory(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"content/en/practice/maths/taocp/vol4/7.2.1.3/09.md": "taocp 7.2.1.3",
		"content/en/practice/codeforces/103604/D.md":         "codeforces 103604",
		"content/en/blog/post.md":                            "en blog",
		"top.md":                                             "content",
	}
	for path, want := range cases {
		if got := groupOf(path); got != want {
			t.Errorf("groupOf(%q) = %q, want %q", path, got, want)
		}
	}
}
