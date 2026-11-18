package tursotest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepairsHashMismatch is the behaviour a killed extraction needs: the next
// run discards the cache and loads cleanly instead of failing forever.
func TestRepairsHashMismatch(t *testing.T) {
	loads, purges := 0, 0
	load := func() error {
		loads++
		if loads == 1 {
			return errors.New("cached library file hash sum mismatch at /x/y: expected=a, got=b")
		}
		return nil
	}
	purge := func() error { purges++; return nil }

	if err := verifyOrRepair(load, purge); err != nil {
		t.Fatalf("verifyOrRepair = %v, want nil after a repair", err)
	}
	if loads != 2 || purges != 1 {
		t.Errorf("loads=%d purges=%d, want 2 and 1", loads, purges)
	}
}

// TestDoesNotPurgeOnOtherErrors pins the blast radius: deleting a cache because
// of an unrelated failure destroys a working library.
func TestDoesNotPurgeOnOtherErrors(t *testing.T) {
	purges := 0
	load := func() error { return errors.New("no such platform library") }
	purge := func() error { purges++; return nil }

	if err := verifyOrRepair(load, purge); err == nil {
		t.Fatal("want the original error")
	}
	if purges != 0 {
		t.Errorf("purged %d times on an unrelated error, want 0", purges)
	}
}

// TestRepairIsBounded keeps a genuinely bad library from spinning.
func TestRepairIsBounded(t *testing.T) {
	loads := 0
	load := func() error { loads++; return errors.New("hash sum mismatch") }
	purge := func() error { return nil }

	if err := verifyOrRepair(load, purge); err == nil {
		t.Fatal("want an error when the rebuild also fails")
	}
	if loads != 2 {
		t.Errorf("loaded %d times, want exactly 2 (one repair, one retry)", loads)
	}
}

// TestLockFileIsBesideTheCache is the safety property the repair depends on.
//
// purgeCache deletes the cache tree while holding the lock. If the lock file
// were inside that tree, the delete would unlink a locked file — and the next
// process would create a NEW file at the same path and take a lock nobody else
// holds, producing exactly the two simultaneous extractors this package exists
// to prevent. This is the bug the POSIX and Windows implementations both had
// before the repair was added.
func TestLockFileIsBesideTheCache(t *testing.T) {
	t.Setenv("TURSO_GO_CACHE_DIR", t.TempDir())

	unlock, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	inside, _ := filepath.Glob(filepath.Join(CacheDir(), ".*lock*"))
	if len(inside) != 0 {
		t.Errorf("lock file lives inside the cache dir (%v); purgeCache would unlink it while held", inside)
	}
	if err := purgeCache(); err != nil {
		t.Fatalf("purge while holding the lock: %v", err)
	}
	if _, statErr := os.Stat(CacheDir()); !os.IsNotExist(statErr) {
		t.Errorf("cache dir survived the purge: %v", statErr)
	}
	beside, _ := filepath.Glob(filepath.Join(filepath.Dir(CacheDir()), ".*lock"))
	if len(beside) == 0 {
		t.Error("no lock file beside the cache dir — the lock was deleted with the tree")
	}
}

// TestPurgeRemovesTheTree pins the measured bug: the loader extracts into a
// hash-named SUBdirectory, so removing only the top level's files repaired
// nothing and reported success.
func TestPurgeRemovesTheTree(t *testing.T) {
	t.Setenv("TURSO_GO_CACHE_DIR", t.TempDir())
	sub := filepath.Join(CacheDir(), "d3b31cad")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(sub, "libturso.dylib")
	if err := os.WriteFile(stale, []byte("TRUNCATED"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := purgeCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the corrupt library survived the purge at %s", stale)
	}
}

// TestErrorNamesTheCure keeps the operator message actionable: a hash mismatch
// reads like "the binary is wrong", and the cure is deleting one file.
func TestErrorNamesTheCure(t *testing.T) {
	load := func() error { return errors.New("failed to load turso library: cached library file hash sum mismatch") }
	purge := func() error { return nil }

	err := verifyOrRepair(load, purge)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error never says the extraction was interrupted: %v", err)
	}
}
