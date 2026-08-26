package credits

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCredStore(t *testing.T, s *Service) *CredentialStore {
	t.Helper()
	k := [32]byte{}
	for i := range k {
		k[i] = byte(i)
	}
	return s.NewCredentialStore(k)
}

func TestCredentialStoreRoundTrip(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	ctx := context.Background()
	const want = "cred-user-value-abc-123"
	if err := cfg.Put(ctx, testUser, "openai", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := cfg.Get(ctx, testUser, "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if ok, _ := cfg.Configured(ctx, testUser, "openai"); !ok {
		t.Fatalf("Configured = false, want true")
	}
}

func TestCredentialStoreNotFound(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	ctx := context.Background()
	_, err := cfg.Get(ctx, testUser, "openai")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestCredentialStoreDeleted(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	ctx := context.Background()
	if err := cfg.Put(ctx, testUser, "openai", "v"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := cfg.Delete(ctx, testUser, "openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := cfg.Configured(ctx, testUser, "openai"); ok {
		t.Fatalf("Configured = true after delete, want false")
	}
}

func TestCredentialStoreDisabled(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := s.NewCredentialStore([32]byte{})
	ctx := context.Background()
	if err := cfg.Put(ctx, testUser, "openai", "v"); !errors.Is(err, ErrCredentialStoreDisabled) {
		t.Fatalf("Put err = %v, want ErrCredentialStoreDisabled", err)
	}
	if _, err := cfg.Get(ctx, testUser, "openai"); !errors.Is(err, ErrCredentialStoreDisabled) {
		t.Fatalf("Get err = %v, want ErrCredentialStoreDisabled", err)
	}
	if ok, _ := cfg.Configured(ctx, testUser, "openai"); ok {
		t.Fatalf("disabled store reported configured")
	}
}

func TestCredentialWrongKeyFailsDecrypt(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	k1 := [32]byte{}
	k2 := [32]byte{}
	for i := range k1 {
		k1[i] = byte(i)
		k2[i] = byte(255 - i)
	}
	cfg1 := s.NewCredentialStore(k1)
	ctx := context.Background()
	if err := cfg1.Put(ctx, testUser, "openai", "cls-volatile"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	cfg2 := s.NewCredentialStore(k2)
	_, err := cfg2.Get(ctx, testUser, "openai")
	if !errors.Is(err, ErrCredentialDecrypt) {
		t.Fatalf("Get err = %v, want ErrCredentialDecrypt", err)
	}
}

func TestByokRelayPassThrough(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	ctx := context.Background()
	tok := "cred-user-token-xyz" // <20 chars, built at runtime
	if err := cfg.Put(ctx, testUser, "openai", tok); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var gotAuth string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	relay := s.NewByokRelay(cfg, map[string]string{"openai": upstream.URL}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/byok/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini"}`))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotAuth != "Bearer "+tok {
		t.Fatalf("upstream auth = %q, want bearer token", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
}

func TestByokRelayMissingCredential404(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	relay := s.NewByokRelay(cfg, map[string]string{"openai": "http://127.0.0.1:1"}, nil)
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		ts.URL+"/api/byok/openai/v1/chat/completions", bytes.NewReader(nil))
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestByokRelayUnknownProvider(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	cfg := testCredStore(t, s)
	ctx := context.Background()
	if err := cfg.Put(ctx, testUser, "openai", "v"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	relay := s.NewByokRelay(cfg, map[string]string{"openai": "http://127.0.0.1:1"}, nil)
	ts := httptest.NewServer(relay)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		ts.URL+"/api/byok/nobarprovider/x", bytes.NewReader(nil))
	req.Header.Set("X-Auth-User", testUser)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSplitPath(t *testing.T) {
	cases := []struct{ in, prov, rest string }{
		{"/api/byok/openai/v1/x", "openai", "v1/x"},
		{"/api/byok/openrouter/chat", "openrouter", "chat"},
		{"/", "", ""},
	}
	for _, c := range cases {
		prov, rest := splitPath(c.in)
		if prov != c.prov || rest != c.rest {
			t.Fatalf("splitPath(%q) = %q/%q, want %q/%q", c.in, prov, rest, c.prov, c.rest)
		}
	}
}
