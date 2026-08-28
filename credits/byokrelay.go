package credits

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// ByokRelay is an in-process pass-through proxy for OpenAI-compatible
// BYOK requests (PLAN §7.2). It is NOT an auth boundary — the app must mount
// it behind auth middleware that sets the internal X-Auth-User header and
// strips any external copy.
//
// Request form: POST /api/byok/{provider}/{path...}
//
// The relay meters every upstream call: it captures the OpenAI-compatible
// usage from the upstream response (JSON or final SSE chunk) and persists an
// llm_usage row with billing_mode=byok and credits_charged=0, so BYOK calls
// become visible to analytics/throttling without charging the user.
type ByokRelay struct {
	svc    *Service
	stores *CredentialStore
	bases  map[string]string // provider -> upstream base URL (e.g. https://api.openai.com/v1)
	logger *slog.Logger
}

// NewByokRelay builds the relay from the credential store, the provider->base
// map (BYOK_PROVIDERS env), and an optional logger.
func (s *Service) NewByokRelay(stores *CredentialStore, bases map[string]string, logger *slog.Logger) *ByokRelay {
	if logger == nil {
		logger = slog.Default()
	}
	return &ByokRelay{svc: s, stores: stores, bases: bases, logger: logger}
}

// ServeHTTP proxies the request to the configured provider base with the
// user's stored credential injected as the bearer token.
func (r *ByokRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	provider, rest := splitPath(req.URL.Path)
	if provider == "" {
		http.Error(w, "provider required", http.StatusBadRequest)
		return
	}
	base, ok := r.bases[provider]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	userID := req.Header.Get("X-Auth-User")
	cred, err := r.stores.Get(req.Context(), userID, provider)
	if err != nil {
		http.Error(w, "credential not configured", http.StatusNotFound)
		return
	}

	target, err := url.Parse(base)
	if err != nil {
		http.Error(w, "bad provider base", http.StatusInternalServerError)
		return
	}
	upstreamPath := outboundPath(target.Path, rest)

	errLogger := slog.NewLogLogger(r.logger.Handler(), slog.LevelWarn)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = upstreamPath
			pr.SetXForwarded()
			pr.Out.Header.Set("Authorization", "Bearer "+cred)
			pr.Out.Header.Del("X-Byok-Key")
			pr.Out.Header.Del("X-Auth-User")
		},
		ErrorLog: errLogger,
	}

	// Meter the call: wrap the writer to capture usage, persist after serving.
	// Persist on a context detached from the request (WithoutCancel): a long
	// SSE stream ends after the client has usually hung up, so the request ctx
	// is cancelled by the time finish() writes llm_usage — which would silently
	// drop the meter row. A short background timeout bounds the write.
	meterCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meter := &usageRW{
		ResponseWriter: w, rel: r, ctx: meterCtx,
		rec: Usage{
			RequestID: newRequestIDForRelay(req), UserID: userID,
			Provider: provider, Model: modelFromBody(req), BillingMode: billingModeByok,
		},
	}
	proxy.ServeHTTP(meter, req)
	meter.finish()
}

// newRequestIDForRelay derives a stable idempotent request id for the relay
// metering row. It prefers an inbound X-Byok-Request-Id (if the app sets one
// so webhooks/retries dedupe) and otherwise mints a fresh one per call.
func newRequestIDForRelay(req *http.Request) string {
	if id := req.Header.Get("X-Byok-Request-Id"); id != "" {
		return "byok:" + id
	}
	return NewRequestID()
}

// modelFromBody reads the request's JSON body to extract the "model" field
// for the usage row, and — for a streaming chat completion — injects
// `stream_options.include_usage=true` so the provider returns usage in the
// final SSE chunk (otherwise the relay's streaming metering sees nothing).
// Best effort: parse failures or non-JSON bodies are passed through intact.
func modelFromBody(req *http.Request) string {
	if req.Body == nil {
		return ""
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}

	var v struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	_ = json.Unmarshal(body, &v)

	// Streaming chat completion: force include_usage=true for metering.
	if v.Stream != nil && *v.Stream {
		var patched map[string]any
		if json.Unmarshal(body, &patched) == nil {
			opts, _ := patched["stream_options"].(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			opts["include_usage"] = true
			patched["stream_options"] = opts
			if out, err := json.Marshal(patched); err == nil {
				body = out
			}
		}
	}

	req.Body = io.NopCloser(bytes.NewReader(body))
	// The reverse proxy forwards ContentLength bytes upstream; if the patched
	// body is longer than the original (include_usage added), keep it in sync
	// or upstream truncates at the stale length.
	req.ContentLength = int64(len(body))
	return v.Model
}
