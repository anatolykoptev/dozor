package engine

import (
	"context"
	"fmt"
	"strings"
)

// GitStatus describes the state of a git repository.
type GitStatus struct {
	Path          string
	Branch        string
	CommitHash    string
	CommitMessage string
	CommitAuthor  string
	CommitDate    string
	Dirty         bool
	DirtyFiles    int
	Ahead         int
	Behind        int
	RemoteURL     string
}

// GetGitStatus returns git deployment status for a path.
func (a *ServerAgent) GetGitStatus(ctx context.Context, path string) GitStatus {
	s := GitStatus{Path: path}

	run := func(cmd string) string {
		res := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("git -C %s %s 2>/dev/null", path, cmd))
		if !res.Success {
			return ""
		}
		return strings.TrimSpace(res.Stdout)
	}

	s.Branch = run("rev-parse --abbrev-ref HEAD")
	s.CommitHash = run("log -1 --format=%h")
	s.CommitMessage = run("log -1 --format=%s")
	s.CommitAuthor = run("log -1 --format=%an")
	s.CommitDate = run("log -1 --format=%ar")
	s.RemoteURL = run("remote get-url origin")

	// Dirty state
	status := run("status --porcelain")
	if status != "" {
		s.Dirty = true
		lines := strings.Split(strings.TrimSpace(status), "\n")
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				s.DirtyFiles++
			}
		}
	}

	// Ahead/behind vs origin
	aheadBehind := run("rev-list --left-right --count HEAD...@{upstream}")
	if aheadBehind != "" {
		fields := strings.Fields(aheadBehind)
		if len(fields) == 2 {
			_, _ = fmt.Sscanf(fields[0], "%d", &s.Ahead)
			_, _ = fmt.Sscanf(fields[1], "%d", &s.Behind)
		}
	}

	return s
}

// FormatGitStatus formats git status for display.
func FormatGitStatus(s GitStatus) string {
	if s.Branch == "" && s.CommitHash == "" {
		return fmt.Sprintf("No git repository found at: %s\n", s.Path)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Git Status: %s\n\n", s.Path)

	fmt.Fprintf(&b, "Branch:  %s\n", s.Branch)
	fmt.Fprintf(&b, "Commit:  %s — %s\n", s.CommitHash, s.CommitMessage)
	fmt.Fprintf(&b, "Author:  %s (%s)\n", s.CommitAuthor, s.CommitDate)

	if s.RemoteURL != "" {
		fmt.Fprintf(&b, "Remote:  %s\n", s.RemoteURL)
	}

	if s.Ahead > 0 || s.Behind > 0 {
		fmt.Fprintf(&b, "Sync:    %d ahead, %d behind origin\n", s.Ahead, s.Behind)
	}

	if s.Dirty {
		fmt.Fprintf(&b, "\n[WARNING] Working tree is dirty (%d modified/untracked files)\n", s.DirtyFiles)
	} else {
		fmt.Fprintf(&b, "\nWorking tree: clean\n")
	}

	return b.String()
}
