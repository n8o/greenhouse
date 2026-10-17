package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/n8o/greenhouse/internal/ledger"
	"github.com/n8o/greenhouse/internal/verify"
)

// errLocked is returned when another scheduler holds the run lock.
var errLocked = errors.New("another greenhouse scheduler holds the lock")

// lock takes the exclusive scheduler lock at path, without waiting, so two
// schedulers never tick the same repo at once. The kernel drops the lock
// when the process dies, so a crash never leaves it stale. unlock releases
// it.
func lock(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", errLocked, path)
		}
		return nil, err
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// excludeState keeps the run-state directory out of `git status` in repo by
// listing it in the clone's own info/exclude, which is never committed. A
// repo that already ignores it (greenhouse's .gitignore does) is left as it
// is: greenhouse runs on repos it does not own and must not edit their
// tracked files.
func excludeState(ctx context.Context, r verify.Runner, repo string) error {
	pattern := "/" + ledger.Dir + "/"
	if _, err := r.Run(ctx, repo, "git", "check-ignore", "-q", ledger.Dir+"/"); err == nil {
		return nil
	}
	out, err := r.Run(ctx, repo, "git", "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("find info/exclude: %w: %s", err, strings.TrimSpace(string(out)))
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
		pattern = "\n" + pattern
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(pattern + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
