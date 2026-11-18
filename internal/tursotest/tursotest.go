//go:build !windows

// Package tursotest serializes warming tursogo's native-library cache across
// test binaries, and repairs a cache left half-written by an interrupted run.
// It is test infrastructure, not part of the library's API.
package tursotest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	turso_libs "github.com/tursodatabase/turso-go-platform-libs"
	"golang.org/x/sys/unix"
)

// Warm extracts tursogo's native library ONCE, under an exclusive cross-process
// lock, repairing a cache left half-written by a killed process.
//
// The race it prevents. tursogo extracts the library into a shared cache
// (<UserCacheDir>/turso-go/<hash8>/) on first use, and the extraction is not
// atomic: os.Create (truncate in place), copy, THEN compare the hash it just
// wrote. Any two initializers that overlap both see a file that exists and is
// non-empty, both read it while the other is mid-copy, and both fail with
//
//	cached library file hash sum mismatch …
//	  expected=<embedded>, got=<garbage>
//
// "got" differs every run because it samples two writers interleaved.
//
// Why a cross-process lock and not just a warm-up: `go test ./...` runs one
// binary per package, concurrently, and credits/payments/stripe all import
// tursogo — so three processes hit a cold cache at the same instant. flock
// serializes them.
//
// The repair covers the other half: a process killed mid-extraction leaves a
// truncated file that EVERY later boot rejects, forever, until someone deletes
// it by hand. Warm now does that deletion itself, once, and only when the error
// is the hash mismatch — see verifyOrRepair.
func Warm() error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	return verifyOrRepair(loadLibrary, purgeCache)
}

// CacheDir returns the directory tursogo extracts into.
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

// verifyOrRepair loads once, and on a hash mismatch discards the cache and
// loads again.
//
// Bounded on purpose: one repairable error, one rebuild. A genuinely bad
// library is a different error, or fails the retry, and reports instead of
// spinning.
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

// purgeCache deletes the whole cache TREE, leaving the lock file in place.
//
// The tree, not just its files: the loader extracts into a hash-named
// subdirectory, so removing only the top level's files leaves the corrupt
// library exactly where it was — a repair that reports success and changes
// nothing.
func purgeCache() error {
	if err := os.RemoveAll(CacheDir()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// lock takes the cross-process lock that serializes extraction.
//
// The lock file lives BESIDE the cache directory, not inside it. Inside, it
// would be deleted by purgeCache while held — and unlinking a locked file
// releases nothing, so the next process would create a NEW file at the same
// path and take a lock no one else is holding: two extractors, which is the
// race this package exists to stop.
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
	if lockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX); lockErr != nil {
		_ = f.Close()
		return nil, lockErr
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
