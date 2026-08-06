package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoGit is returned when git operations are attempted but git is not
// installed or the store is not a repository.
var ErrNoGit = errors.New("git is not available")

// git runs a git subcommand inside the store directory and returns its trimmed
// stdout. Stderr is folded into the returned error for diagnostics.
func (s *Store) git(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoGit, err)
	}

	//nolint:gosec // git subcommand args are built internally, not from a shell string
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.Dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}

		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// IsRepo reports whether the store directory is inside a git work tree.
func (s *Store) IsRepo(ctx context.Context) bool {
	out, err := s.git(ctx, "rev-parse", "--is-inside-work-tree")

	return err == nil && out == "true"
}

// InitRepo initializes a git repository in the store directory and, when remote
// is non-empty, adds it as origin. It is idempotent.
func (s *Store) InitRepo(ctx context.Context, remote string) error {
	if err := s.Ensure(); err != nil {
		return err
	}

	if !s.IsRepo(ctx) {
		if _, err := s.git(ctx, "init"); err != nil {
			return err
		}
	}

	if remote != "" {
		// Replace any existing origin so re-running with a new URL just works.
		if _, err := s.git(ctx, "remote", "remove", "origin"); err != nil {
			// Missing remote is fine; only a real failure should stop us.
			if !strings.Contains(err.Error(), "No such remote") {
				return err
			}
		}
		if _, err := s.git(ctx, "remote", "add", "origin", remote); err != nil {
			return err
		}
	}

	return nil
}

// Remote returns the configured origin push URL, or an empty string when none
// is set.
func (s *Store) Remote(ctx context.Context) string {
	out, _ := s.git(ctx, "remote", "get-url", "origin")

	return out
}

// Dirty reports whether the store has changes — tracked or untracked — that a
// sync would commit. Untracked files count because Sync stages everything the
// store owns, so a new secrets/<name>.age is pending work just like an edited
// profiles.toml.
func (s *Store) Dirty(ctx context.Context) (bool, error) {
	out, err := s.git(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}

	return out != "", nil
}

// staged reports whether the index holds anything to commit. It is checked
// against the index rather than the worktree because `git commit` fails outright
// when nothing is staged, and untracked-but-ignored files (the unencrypted
// data/<name>/ working copies) must not be mistaken for pending work.
func (s *Store) staged(ctx context.Context) (bool, error) {
	// --cached diffs the index against HEAD, and degrades to "everything is new"
	// on a repository that has no commits yet, which is exactly the first sync.
	out, err := s.git(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}

	return out != "", nil
}

// Sync commits any local store changes, pulls with rebase, and pushes to origin
// when one is configured. It is the one-shot "share" action.
func (s *Store) Sync(ctx context.Context, message string) error {
	if !s.IsRepo(ctx) {
		return fmt.Errorf("%w: run 'store init' first", ErrNoGit)
	}

	// Stage the whole store, not a fixed file list: the shared artifacts are
	// profiles.toml, recipients.txt, the encrypted secrets/<name>.age and the
	// encrypted data/<name>.tar.age bundles, and naming only the first of those
	// would silently leave every secret and session behind. -A also records the
	// deletions left by `profile remove`. The store's .gitignore keeps the
	// unencrypted data/<name>/ working copies local.
	if _, err := s.git(ctx, "add", "-A", "."); err != nil {
		return err
	}

	staged, err := s.staged(ctx)
	if err != nil {
		return err
	}
	if staged {
		if message == "" {
			message = "sync profiles"
		}
		if _, err := s.git(ctx, "commit", "-m", message); err != nil {
			return err
		}
	}

	hasRemote := s.Remote(ctx) != ""
	if !hasRemote {
		return nil
	}

	if _, err := s.git(ctx, "pull", "--rebase", "origin", "HEAD"); err != nil {
		return err
	}
	if _, err := s.git(ctx, "push", "-u", "origin", "HEAD"); err != nil {
		return err
	}

	return nil
}
