// Package gitx runs git and parses what it prints. It never takes a lock a
// git run elsewhere would wait for: GIT_OPTIONAL_LOCKS=0 keeps git status
// from refreshing the index.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run runs git in dir and returns its standard output with the trailing
// newline removed. A non-zero exit returns an *Error carrying stderr.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: strings.TrimSpace(errb.String()), Err: err}
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Error is a git run that failed.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := e.Stderr
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Failed reports whether err is git exiting non-zero, as opposed to git not
// running at all or the context ending.
func Failed(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// Status is what git status --porcelain=v2 --branch --show-stash says.
type Status struct {
	Head      string // branch name; empty when detached
	Oid       string // HEAD's commit; empty in a repository with no commits
	Upstream  string // such as origin/main; empty without one
	Ahead     int
	Behind    int
	HasAB     bool // Ahead and Behind are known (the upstream exists)
	Staged    int
	Unstaged  int
	Untracked int
	Conflicts int
	Stash     int
}

// ParseStatus parses git status --porcelain=v2 --branch --show-stash -z.
func ParseStatus(out string) Status {
	var s Status
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		switch {
		case strings.HasPrefix(r, "# branch.oid "):
			if v := strings.TrimPrefix(r, "# branch.oid "); v != "(initial)" {
				s.Oid = v
			}
		case strings.HasPrefix(r, "# branch.head "):
			if v := strings.TrimPrefix(r, "# branch.head "); v != "(detached)" {
				s.Head = v
			}
		case strings.HasPrefix(r, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			var a, b int
			if _, err := fmt.Sscanf(strings.TrimPrefix(r, "# branch.ab "), "+%d -%d", &a, &b); err == nil {
				s.Ahead, s.Behind, s.HasAB = a, b, true
			}
		case strings.HasPrefix(r, "# stash "):
			fmt.Sscanf(strings.TrimPrefix(r, "# stash "), "%d", &s.Stash)
		case strings.HasPrefix(r, "1 "), strings.HasPrefix(r, "2 "):
			if len(r) < 4 {
				continue
			}
			x, y := r[2], r[3]
			if x != '.' {
				s.Staged++
			}
			if y != '.' {
				s.Unstaged++
			}
			if r[0] == '2' {
				i++ // a rename or copy is followed by its original path
			}
		case strings.HasPrefix(r, "u "):
			s.Conflicts++
		case strings.HasPrefix(r, "? "):
			s.Untracked++
		}
	}
	return s
}

// Worktree is one entry of git worktree list --porcelain.
type Worktree struct {
	Path     string
	Head     string // commit
	Branch   string // short name; empty when detached
	Bare     bool
	Prunable bool // its directory is gone
	Locked   bool
}

// ParseWorktrees parses git worktree list --porcelain -z. The first entry
// is the main worktree.
func ParseWorktrees(out string) []Worktree {
	var wts []Worktree
	var cur *Worktree
	for _, f := range strings.Split(out, "\x00") {
		switch {
		case f == "":
			if cur != nil {
				wts = append(wts, *cur)
				cur = nil
			}
		case strings.HasPrefix(f, "worktree "):
			cur = &Worktree{Path: strings.TrimPrefix(f, "worktree ")}
		case cur == nil:
		case strings.HasPrefix(f, "HEAD "):
			cur.Head = strings.TrimPrefix(f, "HEAD ")
		case strings.HasPrefix(f, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(f, "branch "), "refs/heads/")
		case f == "bare":
			cur.Bare = true
		case f == "prunable" || strings.HasPrefix(f, "prunable "):
			cur.Prunable = true
		case f == "locked" || strings.HasPrefix(f, "locked "):
			cur.Locked = true
		}
	}
	if cur != nil {
		wts = append(wts, *cur)
	}
	return wts
}

// Branch is one local branch from git for-each-ref.
type Branch struct {
	Name     string
	Upstream string // empty without one
	Ahead    int
	Behind   int
	Gone     bool // the upstream is configured but no longer exists
	Date     int64
}

// BranchFormat is the --format ParseBranches reads.
const BranchFormat = "%(refname:short)%00%(upstream:short)%00%(upstream:track,nobracket)%00%(committerdate:unix)"

// ParseBranches parses git for-each-ref --format=BranchFormat refs/heads.
func ParseBranches(out string) []Branch {
	var bs []Branch
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 4 {
			continue
		}
		b := Branch{Name: f[0], Upstream: f[1]}
		for _, part := range strings.Split(f[2], ", ") {
			switch {
			case part == "gone":
				b.Gone = true
			case strings.HasPrefix(part, "ahead "):
				fmt.Sscanf(part, "ahead %d", &b.Ahead)
			case strings.HasPrefix(part, "behind "):
				fmt.Sscanf(part, "behind %d", &b.Behind)
			}
		}
		fmt.Sscanf(f[3], "%d", &b.Date)
		bs = append(bs, b)
	}
	return bs
}

// Slug turns a remote URL into owner/name for a github.com remote:
// git@github.com:o/n.git, ssh://git@github.com/o/n, ssh://git@ssh.github.com:443/o/n.git,
// https://github.com/o/n. host is github.com unless the URL names another;
// ok is false when the URL is not owner/name on a host.
func Slug(url string) (host, slug string, ok bool) {
	u := strings.TrimSuffix(strings.TrimSpace(url), "/")
	u = strings.TrimSuffix(u, ".git")
	var rest string
	switch {
	case strings.Contains(u, "://"):
		rest = u[strings.Index(u, "://")+3:]
		if at := strings.Index(rest, "@"); at >= 0 && at < strings.Index(rest+"/", "/") {
			rest = rest[at+1:]
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", false
		}
		host, rest = rest[:slash], rest[slash+1:]
		if c := strings.Index(host, ":"); c >= 0 {
			host = host[:c]
		}
	case strings.Contains(u, ":"):
		c := strings.Index(u, ":")
		host, rest = u[:c], u[c+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	default:
		return "", "", false
	}
	if host == "ssh.github.com" {
		host = "github.com"
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return host, parts[0] + "/" + parts[1], true
}
