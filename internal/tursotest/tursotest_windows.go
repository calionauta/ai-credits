//go:build windows

package tursotest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	turso_libs "github.com/tursodatabase/turso-go-platform-libs"
	"golang.org/x/sys/windows"
)

// Warm extracts tursogo's native library ONCE, under an exclusive cross-process
// lock, repairing a cache left half-written by an interrupted run. See
// tursotest.go for the race this prevents — same bug on Windows: the
// platform-libs module embeds libs/windows_amd64 too, and the extraction is the
// same non-atomic os.Create/copy/verify sequence.
//
// Windows needs its own locking primitive (no flock), hence this file.
func Warm() error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	return verifyOrRepair(loadLibrary, purgeCache)
}

// CacheDir returns the directory tursogo uses for its extracted library.
func CacheDir() string {
	root := os.Getenv("TURSO_GO_CACHE_DIR")
	if root == "" {
		if d, err := os.UserCacheDir(); err == nil {
			root = d
		} else {
			root = os.TempDir()
		}
	}
	return filepath.Join(root, "turso-go")
}

func loadLibrary() error {
	_, err := turso_libs.LoadTursoLibrary(turso_libs.LoadTursoLibraryConfig{})
	return err
}

// verifyOrRepair mirrors the POSIX implementation: one repairable error, one
// rebuild, then report. Keep the two in step; a platform that heals silently on
// one OS and not the other is the kind of divergence nobody tests for.
func verifyOrRepair(load func() error, purge func() error) error {
	err := load()
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "hash sum mismatch") {
		return err
	}
	if purgeErr := purge(); purgeErr != nil {
		return fmt.Errorf("%w (discarding the cache also failed: %w)", err, purgeErr)
	}
	if retryErr := load(); retryErr != nil {
		return fmt.Errorf("%w (a previous extraction was interrupted; the cache was discarded "+
			"and rebuilding it failed: %w)", retryErr, retryErr)
	}
	return nil
}

// purgeCache deletes the whole cache tree; see tursotest.go for why the tree
// and not just its files.
func purgeCache() error {
	if err := os.RemoveAll(CacheDir()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// lock takes the lock BESIDE the cache directory, not inside it: purgeCache
// deletes the tree while this lock is held, and unlinking a locked file lets
// the next process lock a new file at the same path — two extractors, which is
// the race this package exists to stop.
func lock() (unlock func(), err error) {
	root := filepath.Dir(CacheDir())
	if mkErr := os.MkdirAll(root, cacheDirPerm); mkErr != nil { // #nosec G703 -- a cache dir this process chose, not request input
		return nil, mkErr
	}
	lockPath := filepath.Join(root, ".turso-go.lock") // #nosec G703 -- constant filename under the dir above
	f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, cacheFilePerm)
	if openErr != nil {
		return nil, openErr
	}
	// LOCKFILE_EXCLUSIVE_LOCK blocks; without FAIL_IMMEDIATELY LockFileEx waits,
	// which is what serializes the two processes.
	ol := new(windows.Overlapped)
	if lockErr := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); lockErr != nil {
		_ = f.Close()
		return nil, lockErr
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
		_ = f.Close()
	}, nil
}
