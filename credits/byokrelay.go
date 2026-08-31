package credits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type ByokRelay struct {
	svc    *Service
	stores *CredentialStore
	bases  map[string]string
	logger *slog.Logger
	// UpstreamTimeout is the explicit timeout for the upstream provider call.
	// Zero means 30s default.
	UpstreamTimeout time.Duration
}

func (s *Service) NewByokRelay(stores *CredentialStore, bases map[string]string, logger *slog.Logger) *ByokRelay {
	if logger == nil {
		logger = slog.Default()
	}
	return &ByokRelay{svc: s, stores: stores, bases: bases, logger: logger, UpstreamTimeout: 30 * time.Second}
}

func (r *ByokRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if err := requireIdentifier("X-Auth-User", userID); err != nil {
		http.Error(w, "authenticated user required", http.StatusUnauthorized)
		return
	}
	cred, err := r.stores.Get(req.Context(), userID, provider)
	if err != nil {
		http.Error(w, "credential not configured", http.StatusNotFound)
		return
	}

	target, err := url.Parse(base)
	if err != nil || target.Scheme != "https" && (target.Scheme != "http" || !isLoopbackHost(target.Hostname())) || target.Host == "" {
		http.Error(w, "bad provider base", http.StatusInternalServerError)
		return
	}
	upstreamPath := outboundPath(target.Path, rest)

	errLogger := slog.NewLogLogger(r.logger.Handler(), slog.LevelWarn)

	timeout := r.UpstreamTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       90 * time.Second,
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
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

	model, stream, bodyErr := modelFromBody(req)
	if bodyErr != nil {
		http.Error(w, bodyErr.Error(), http.StatusRequestEntityTooLarge)
		return
	}

	meterCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meter := &usageRW{
		ResponseWriter: w, rel: r, ctx: meterCtx, stream: stream,
		rec: Usage{
			RequestID: newRequestIDForRelay(req), UserID: userID,
			Provider: provider, Model: model, BillingMode: billingModeByok,
		},
	}
	proxy.ServeHTTP(meter, req)
	meter.finish()
}

func newRequestIDForRelay(req *http.Request) string {
	if id := req.Header.Get("X-Byok-Request-Id"); id != "" {
		return "byok:" + id
	}
	return NewRequestID()
}

const maxByokRequestBytes = 1 << 20

func modelFromBody(req *http.Request) (string, bool, error) {
	if req.Body == nil {
		return "", false, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxByokRequestBytes+1))
	req.Body.Close()
	if err != nil {
		return "", false, err
	}
	if len(body) > maxByokRequestBytes {
		return "", false, errors.New("request body too large")
	}

	var v struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	_ = json.Unmarshal(body, &v)

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
	req.ContentLength = int64(len(body))
	return v.Model, v.Stream != nil && *v.Stream, nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
