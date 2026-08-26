# Setup — environment facts (verified)

| Fact | Value |
|---|---|
| Repo | `~/repos/ai-credits`, no commits yet, module `github.com/calionauta/ai-credits` |
| Go | local go1.22.2, but GOTOOLCHAIN=auto downloads go1.26.6 (verified: gogogo pins go 1.26.5, treinamento 1.26.1) |
| Linters | golangci-lint, gofumpt, govulncheck installed |
| gogogo | `~/repos/gogogo-fullstack-template`, goai v0.9.6, `internal/llm` (goai.go, streaming.go, fakeserver/), config in `config/config.go`, router in `router/router.go`, `app.DB()` exposes *sql.DB |
| treinamento | `~/repos/treinamento-praticas-narrativas`, goai v0.7.6, `features/narrative-therapy/handlers/llm_goai_helpers.go` + `chat_send.go` present |
| goai Usage | PLAN.md §3 verified: `Result.TotalUsage provider.Usage{InputTokens, OutputTokens, TotalTokens, ReasoningTokens, CacheReadTokens, CacheWriteTokens}` (module cache cold; facts per PLAN 2026-08-24) |

**Requirements (from PLAN.md)**: zero deps lib (only golang.org/x/crypto), INTEGER timestamps,
EnsureSchema idempotent, reserve→execute→capture→release, lazy monthly grant, reconcile,
BYOK XChaCha20-Poly1305 + in-process ReverseProxy relay, integrations as dependency (not vendored).
