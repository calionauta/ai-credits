package credits

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCostExact(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	// gpt-4o-mini: in 150000, out 600000 per Mtok.
	// 10k in + 4k out → 10000/1e6*150000 + 4000/1e6*600000
	//              = 1500 + 2400 = 3900 microunits → ceil → 4 credits @ 1000/cred.
	u := Usage{Model: "gpt-4o-mini", InputTokens: 10000, OutputTokens: 4000}
	mu, err := s.Cost(ctx, u)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if mu != 3900 {
		t.Fatalf("cost = %d, want 3900", mu)
	}
	cred, err := s.Credits(ctx, u)
	if err != nil {
		t.Fatalf("Credits: %v", err)
	}
	if cred != 4 {
		t.Fatalf("credits = %d, want 4 (ceil 3900/1000)", cred)
	}
}

func TestCostCacheAndReasoning(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	u := Usage{
		Model: "gpt-4o-mini", InputTokens: 1000, OutputTokens: 0,
		CachedTokens: 2000, ReasoningTokens: 1000,
	}
	// 1000/1e6*150000 + 2000/1e6*150000 + 1000/1e6*0 = 150 + 300 = 450
	mu, err := s.Cost(ctx, u)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if mu != 450 {
		t.Fatalf("cost = %d, want 450", mu)
	}
}

func TestErrUnknownModel(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	_, err := s.Cost(ctx, Usage{Model: "does-not-exist"})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err = %v, want ErrUnknownModel", err)
	}
	_, err = s.EstimateMax(ctx, "does-not-exist", 10, 10)
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("EstimateMax err = %v, want ErrUnknownModel", err)
	}
}

// TestEstimateMaxGeCost asserts the invariant EstimateMax >= Cost for any
// valid usage within the max bounds.
func TestEstimateMaxGeCost(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	for _, model := range []string{"gpt-4o-mini", "gpt-4o", "claude-3-5-haiku"} {
		for _, tc := range []struct {
			in, out int
		}{
			{0, 0}, {1000, 0}, {0, 2048}, {10000, 4096}, {1500, 800},
		} {
			est, err := s.EstimateMax(ctx, model, tc.in, tc.out)
			if err != nil {
				t.Fatalf("EstimateMax(%s): %v", model, err)
			}
			if est < 1 {
				t.Fatalf("EstimateMax(%s,%d,%d) = %d, want >=1", model, tc.in, tc.out, est)
			}
			cost, err := s.Cost(ctx, Usage{Model: model, InputTokens: tc.in, OutputTokens: tc.out})
			if err != nil {
				t.Fatalf("Cost: %v", err)
			}
			costCred := creditsFromMicrounits(cost, s.pricer.microunitsPerCredit)
			if est < costCred {
				t.Fatalf("EstimateMax(%d) < Cost(%d): reservation would under-cover",
					est, costCred)
			}
		}
	}
}

// TestJSONPricingFileOverrides ensures file prices override built-in defaults.
func TestJSONPricingFileOverrides(t *testing.T) {
	json := `{
	  "version": "2026-08-test",
	  "microunits_per_credit": 1000,
	  "models": {
	    "my-model": {"input_per_mtok": 100000, "output_per_mtok": 200000,
	                "cached_input_per_mtok": 50000, "reasoning_per_mtok": 0}
	  }
	}`
	// Use a service built with a pricing reader.
	s, err := New(testDB(t), Config{
		PricingReader:  strings.NewReader(json),
		PricingVersion: "2026-08-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.db.Close()
	ctx := context.Background()
	// 1M in → 100000 microunits → 100 credits.
	mu, err := s.Cost(ctx, Usage{Model: "my-model", InputTokens: 1000000})
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if mu != 100000 {
		t.Fatalf("cost = %d, want 100000", mu)
	}
	// Built-in model no longer present in the file — override removes it.
	if _, err := s.Cost(ctx, Usage{Model: testMiniModel}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err = %v, want ErrUnknownModel (file replaces defaults)", err)
	}
}

func TestRecordUsage(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	u := Usage{
		RequestID: "req-1", UserID: "u1", Provider: "openai",
		Model: "gpt-4o-mini", BillingMode: "managed", InputTokens: 10,
		OutputTokens: 20, CostMicrounits: 3900, CreditsCharged: 4,
	}
	if err := s.RecordUsage(ctx, u); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	// Duplicate request_id is idempotent (not an error).
	if err := s.RecordUsage(ctx, u); err != nil {
		t.Fatalf("RecordUsage dup: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM llm_usage WHERE request_id=?`, "req-1").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("dup insert created %d rows, want 1", n)
	}
	var mode, ver string
	if err := s.db.QueryRowContext(ctx,
		`SELECT billing_mode, pricing_version FROM llm_usage WHERE request_id=?`, "req-1").
		Scan(&mode, &ver); err != nil {
		t.Fatalf("row: %v", err)
	}
	if mode != "managed" || ver != "builtin-2026-08" {
		t.Fatalf("row = %q/%q, want managed/builtin-2026-08", mode, ver)
	}
}
