package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
