//go:build unix

package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFile takes an exclusive advisory lock on <root>/auth.lock, serializing
// token refreshes across processes that share the same auth.json. The returned
// func releases the lock. The lock file is created if absent and never
// removed (removing it would race with another holder).
func (s *Store) lockFile() (func(), error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create %s: %w", s.root, err)
	}
	f, err := os.OpenFile(filepath.Join(s.root, "auth.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("auth: open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("auth: acquire lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
