package credits

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

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
	svc, svcErr := New(db, Config{})
	if svcErr != nil {
		t.Fatalf("new: %v", svcErr)
	}
	if schemaErr := svc.EnsureSchema(ctx); schemaErr != nil {
		t.Fatalf("ensure schema: %v", schemaErr)
	}
}
