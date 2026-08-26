package credits

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ByokRelay is an in-process pass-through proxy for OpenAI-compatible
// BYOK requests (PLAN §7.2). It is NOT an auth boundary — the app must mount
// it behind auth middleware that sets the internal X-Auth-User header and
// strips any external copy.
//
// Request form: POST /api/byok/{provider}/{path...}
type ByokRelay struct {
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
	return &ByokRelay{stores: stores, bases: bases, logger: logger}
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
		ErrorLog: slog.NewLogLogger(r.logger.Handler(), slog.LevelWarn),
	}
	proxy.ServeHTTP(w, req)
}
