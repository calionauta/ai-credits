package credits

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// BYOK relay usage metering (audit gap: BYOK calls were invisible to
// llm_usage analytics). OpenAI-compatible endpoints return usage in the
// response:
//   - non-streaming: a top-level "usage" object in the JSON body
//   - streaming:      the final "data: {json}" chunk carries "usage",
//     right before "data: [DONE]"
//
// usageCapturingRW wraps the response writer, echoing every byte through,
// and on completion extracts usage and persists an llm_usage row with
// billing_mode=byok and credits_charged=0 (BYOK is pass-through; the user
// pays their own provider, we only meter the call).

// usageRW is a ResponseWriter that passes through untouched and captures the
// body so usage can be parsed at the end (both JSON and SSE forms). The ctx
// field threads the request context to the async finish() persistence step.
//
//nolint:containedctx // ResponseWriter wrapper; ctx belongs to the proxied request it wraps.
type usageRW struct {
	http.ResponseWriter
	rel  *ByokRelay
	rec  Usage
	ctx  context.Context
	buf  bytes.Buffer
	done bool
}

func (u *usageRW) Write(p []byte) (int, error) {
	u.buf.Write(p) // capture for usage parse
	return u.ResponseWriter.Write(p)
}

func (u *usageRW) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (u *usageRW) WriteHeader(code int) {
	u.ResponseWriter.WriteHeader(code)
}

// finish parses usage from the captured stream and records it once. The
// relay calls it after the proxied request completes.
func (u *usageRW) finish() {
	if u.done {
		return
	}
	u.done = true
	if err := u.recordUsage(); err != nil {
		u.rel.logger.Debug("byok usage record failed", "err", err)
	}
}

// openaiUsage mirrors the OpenAI-compatible usage payload.
type openaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func parseUsage(body []byte) *openaiUsage {
	// Non-streaming: the whole body is one JSON object with "usage".
	var whole struct {
		Usage *openaiUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &whole); err == nil && whole.Usage != nil {
		return whole.Usage
	}
	// Streaming: walk "data: {json}" lines; keep the last one that has usage.
	var last *openaiUsage
	for ln := range bytes.SplitSeq(body, []byte("\n")) {
		s := bytes.TrimSpace(ln)
		if !bytes.HasPrefix(s, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(s[len("data:"):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var chunk struct {
			Usage *openaiUsage `json:"usage"`
		}
		if err := json.Unmarshal(payload, &chunk); err == nil && chunk.Usage != nil {
			last = chunk.Usage
		}
	}
	return last
}

// recordUsage parses the captured body and writes the llm_usage row.
func (u *usageRW) recordUsage() error {
	usage := parseUsage(u.buf.Bytes())
	if usage == nil {
		// No usage in the response (e.g. error body) — nothing to meter.
		return nil
	}
	rec := u.rec
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = u.rel.svc.cfg.Now()
	}
	rec.InputTokens = usage.PromptTokens
	rec.OutputTokens = usage.CompletionTokens
	if usage.PromptTokensDetails != nil {
		rec.CachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	return u.rel.svc.RecordUsage(u.ctx, rec)
}

var (
	_ http.ResponseWriter = (*usageRW)(nil)
	_ http.Flusher        = (*usageRW)(nil)
)
