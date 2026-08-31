package credits

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BYOK relay meters non-streaming responses: usage parsed from the JSON body.
func TestByokRelayMetersJSON(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	cfg := testCredStore(t, s)
	if err := cfg.Put(ctx, testUser, "openai", "tok"); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o-mini","usage":`+
			`{"prompt_tokens":120,"completion_tokens":40,"total_tokens":160}}`)
	}))
	defer upstream.Close()

	relay := s.NewByokRelay(cfg, map[string]string{"openai": upstream.URL}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		ts.URL+"/api/byok/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini"}`))
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The usage row must be present with billing_mode=byok, no credits charged.
	u, err := s.queryRecentUsage(ctx, t, testUser)
	if err != nil {
		t.Fatalf("usage not recorded: %v", err)
	}
	if u.BillingMode != "byok" || u.CreditsCharged != 0 {
		t.Fatalf("usage = %+v, want byok/0", u)
	}
	if u.InputTokens != 120 || u.OutputTokens != 40 || u.CachedTokens != 0 {
		t.Fatalf("usage tokens = in%d/out%d/cache%d, want 120/40/0",
			u.InputTokens, u.OutputTokens, u.CachedTokens)
	}
}

// BYOK relay meters streaming responses: usage from the final SSE data chunk.
func TestByokRelayMetersSSE(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	cfg := testCredStore(t, s)
	if err := cfg.Put(ctx, testUser, "openai", "tok"); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"1","choices":[{"delta":{"content":"hi"}}],"usage":null}`,
			`data: {"id":"1","choices":[]}`,
			`data: {"id":"1","choices":[],"usage":{"prompt_tokens":95,"completion_tokens":22,"total_tokens":117}}`,
			"data: [DONE]",
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, c+"\n\n")
			fl.Flush()
		}
	}))
	defer upstream.Close()

	relay := s.NewByokRelay(cfg, map[string]string{"openai": upstream.URL}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		ts.URL+"/api/byok/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini"}`))
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain to EOF so capture sees it
	_ = resp.Body.Close()

	u, err := s.queryRecentUsage(ctx, t, testUser)
	if err != nil {
		t.Fatalf("usage not recorded: %v", err)
	}
	if u.InputTokens != 95 || u.OutputTokens != 22 {
		t.Fatalf("usage tokens = in%d/out%d, want 95/22", u.InputTokens, u.OutputTokens)
	}
}

// The relay injects stream_options.include_usage=true on streaming requests so
// the provider returns usage in the final SSE chunk (the metering depends on
// it). Non-streaming bodies are passed through unchanged.
func TestByokRelayStreamsWithoutBufferingWholeResponse(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	cfg := testCredStore(t, s)
	if err := cfg.Put(ctx, testUser, "openai", "tok"); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher) //nolint:errcheck
		for range 2048 {       // 2 MiB of stream payload; capture must remain bounded.
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\""+strings.Repeat("x", 1024)+"\"}}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
		fl.Flush()
	}))
	defer upstream.Close()

	relay := s.NewByokRelay(cfg, map[string]string{"openai": upstream.URL}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/byok/openai/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	u, err := s.queryRecentUsage(ctx, t, testUser)
	if err != nil || u.InputTokens != 3 || u.OutputTokens != 2 {
		t.Fatalf("usage = %+v, err = %v", u, err)
	}
}

func TestByokRelayInjectsStreamUsage(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	cfg := testCredStore(t, s)
	if err := cfg.Put(ctx, testUser, "openai", "tok"); err != nil {
		t.Fatal(err)
	}

	var gotStream, gotNonStream string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(r.URL.Path, "/stream") {
			gotStream = string(b)
		} else {
			gotNonStream = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	relay := s.NewByokRelay(cfg, map[string]string{"openai": upstream.URL}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(relay)
	defer ts.Close()

	post := func(path, body string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			ts.URL+"/api/byok/openai"+path, strings.NewReader(body))
		req.Header.Set("X-Auth-User", testUser)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	// Streaming → include_usage added.
	post("/v1/chat/completions/stream", `{"model":"m","stream":true}`)
	if !strings.Contains(gotStream, `"include_usage":true`) {
		t.Fatalf("streaming body missing include_usage: %s", gotStream)
	}

	// Non-streaming → body unchanged (no include_usage injected).
	post("/v1/chat/completions", `{"model":"m"}`)
	if strings.Contains(gotNonStream, "include_usage") {
		t.Fatalf("non-streaming body should not get include_usage: %s", gotNonStream)
	}
	if !strings.Contains(gotNonStream, `"model":"m"`) {
		t.Fatalf("non-streaming body mangled: %s", gotNonStream)
	}
}

// queryRecentUsage reads the newest llm_usage row for a user for assertions.
func (s *Service) queryRecentUsage(ctx context.Context, t *testing.T, userID string) (Usage, error) {
	t.Helper()
	var u Usage
	err := s.db.QueryRowContext(ctx,
		`SELECT provider, model, billing_mode, input_tokens, output_tokens,
		        cached_input_tokens, credits_charged
		   FROM llm_usage WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`,
		userID).Scan(&u.Provider, &u.Model, &u.BillingMode,
		&u.InputTokens, &u.OutputTokens, &u.CachedTokens, &u.CreditsCharged)
	if err != nil {
		return u, err
	}
	return u, nil
}
