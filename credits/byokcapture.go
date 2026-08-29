package credits

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// maxUsageCaptureBytes bounds response buffering in the BYOK relay. JSON
// responses need their full body for parsing; SSE responses keep only the
// current event line, so long streams do not accumulate in memory.
const maxUsageCaptureBytes = 1 << 20

// usageRW passes bytes through unchanged while extracting OpenAI-compatible
// usage at the end of a non-streaming JSON body or from SSE event lines.
//
//nolint:containedctx // ResponseWriter wrapper owns a bounded persistence context.
type usageRW struct {
	http.ResponseWriter
	rel *ByokRelay
	rec Usage
	ctx context.Context

	body   bytes.Buffer
	line   bytes.Buffer
	usage  *openaiUsage
	stream bool
	done   bool
}

func (u *usageRW) Write(p []byte) (int, error) {
	u.capture(p)
	return u.ResponseWriter.Write(p)
}

func (u *usageRW) capture(p []byte) {
	if !u.stream {
		if u.body.Len()+len(p) <= maxUsageCaptureBytes {
			_, _ = u.body.Write(p)
		}
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			u.appendLine(p)
			return
		}
		u.appendLine(p[:i])
		u.parseLine()
		u.line.Reset()
		p = p[i+1:]
	}
}

func (u *usageRW) appendLine(p []byte) {
	if u.line.Len()+len(p) <= maxUsageCaptureBytes {
		_, _ = u.line.Write(p)
	}
}

func (u *usageRW) parseLine() {
	line := bytes.TrimSpace(u.line.Bytes())
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	var chunk struct {
		Usage *openaiUsage `json:"usage"`
	}
	if json.Unmarshal(payload, &chunk) == nil && chunk.Usage != nil {
		u.usage = chunk.Usage
	}
}

func (u *usageRW) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (u *usageRW) WriteHeader(code int) {
	u.ResponseWriter.WriteHeader(code)
}

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

func (u *usageRW) recordUsage() error {
	usage := u.usage
	if !u.stream {
		var whole struct {
			Usage *openaiUsage `json:"usage"`
		}
		if json.Unmarshal(u.body.Bytes(), &whole) == nil {
			usage = whole.Usage
		}
	}
	if usage == nil {
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
	return u.rel.svc.RecordUsageRetry(u.ctx, rec)
}

var (
	_ http.ResponseWriter = (*usageRW)(nil)
	_ http.Flusher        = (*usageRW)(nil)
)
