package credits

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRequestID(t *testing.T) {
	a := NewRequestID()
	b := NewRequestID()
	if a == "" || a == b {
		t.Fatalf("expected distinct non-empty ids, got %q and %q", a, b)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("12345678"); got != 2 {
		t.Fatalf("8 chars at 4/token = 2, got %d", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty -> 0, got %d", got)
	}
}

func TestOpenSQLite(t *testing.T) {
	dir := t.TempDir()
	db, openErr := OpenSQLite("sqlite", filepath.Join(dir, "h.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if pingErr := db.PingContext(ctx); pingErr != nil {
		t.Fatalf("ping: %v", pingErr)
	}
	// Ensure schema + engine run on an OpenSQLite handle too (apps do New+EnsureSchema).
	svc, svcErr := New(db, Config{})
	if svcErr != nil {
		t.Fatalf("new: %v", svcErr)
	}
	if schemaErr := svc.EnsureSchema(ctx); schemaErr != nil {
		t.Fatalf("ensure schema: %v", schemaErr)
	}
}
