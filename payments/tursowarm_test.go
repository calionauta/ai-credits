package payments

import (
	"fmt"
	"os"
	"testing"

	_ "turso.tech/database/tursogo"

	"github.com/calionauta/ai-credits/internal/tursotest"
)

// TestMain warms tursogo's shared native-library cache before any test runs.
// See internal/tursotest for why the extraction needs serializing: it is not
// atomic, and `go test ./...` starts this binary at the same instant as the
// other packages that import tursogo, on the same cache.
func TestMain(m *testing.M) {
	if err := tursotest.Warm(); err != nil {
		fmt.Fprintf(os.Stderr, "payments: warming the turso library cache failed: %v\n\n"+
			"a hash mismatch on a file that already exists means a previous\n"+
			"extraction was interrupted; delete %s and re-run\n", err, tursotest.CacheDir())
		os.Exit(1)
	}
	os.Exit(m.Run())
}
