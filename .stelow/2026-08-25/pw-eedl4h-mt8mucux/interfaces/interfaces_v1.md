# Interface Alternatives — ai-credits (v1)

> Appetite **Core** → 3 archetypes (A, D, E) + hybrid. The "interface" here is the
> **developer-facing Go API** of the `ai-credits` library (plus the two app
> integration points). UI states are mapped to API-call states (error paths,
> idempotency, concurrency).

---

## Proposal A — Conventional Standard (idiomatic Go service)

**Work Pattern:** Operate — callers act on credit state with precision (reserve/settle/release as explicit verbs). Composition: one `*credits.Service` handle, methods grouped by domain verb, errors as values.

**Implication:** The API is a plain struct with methods, exactly what Go developers expect: `credits.New(db, cfg)` → `svc.Grant(...)`, `svc.Reserve(...)`. No magic, no wrappers.

**Philosophy:** Maximize familiarity. Every method takes explicit params, returns `(T, error)`. Types mirror the domain (ledger entries, reservations, usage records). The shape is already pinned by PLAN.md §4-9.

**Primary interaction loop:**
```go
svc, _ := credits.New(db, cfg)
svc.EnsureMonthlyGrant(ctx, userID, plan)
est, _ := svc.EstimateMax(ctx, model, maxOut)
res, _ := svc.Reserve(ctx, userID, est, reqID)
// ... LLM call ...
cost, _ := svc.Cost(ctx, model, usage)
svc.Settle(ctx, res.ID, cost)          // or svc.Release(ctx, res.ID)
svc.RecordUsage(ctx, userID, model, usage)
bal, _ := svc.Balance(ctx, userID)
```

**Trade-offs:** pros — predictable, greppable, matches PLAN signatures 1:1, easy to test; cons — more boilerplate per call site (5 calls per request), caller must sequence correctly; effort — low (PLAN already defines it); risk — low; maintainability — high.

**Design Smell Audit:** ✅ avoids kitchen-sink (methods grouped by verb); ✅ avoids hidden state (all state on svc); ⚠️ 5-call sequence is ceremony — acceptable because it keeps each step testable and the pricing/usage types explicit.

**State Coverage (API-call states):**

| API method | Type | OK | Err | Idempotent | Concurr. | Empty | Coverage |
|-----------|------|:--:|:--:|:--:|:--:|:--:|:--:|
| Grant | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Refund | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Balance | Int | ✅ | ✅ | N/A | ✅ | ✅ | 4/4 |
| Reserve | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Settle/Release | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| RecordUsage | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Reconcile | Int | ✅ | ✅ | ✅ | N/A | ✅ | 4/4 |
| Cost/EstimateMax | Int | ✅ | ✅ | N/A | ✅ | N/A | 3/3 |

---

## Proposal D — Radical Simplicity (transactional envelope)

**Work Pattern:** Operate, but collapsed — one envelope that hides the sequencing. Implication: callers never see Reserve/Settle/Release; they pass a closure that runs inside a reservation.

**Philosophy:** The true job-to-be-done is "run this LLM call and charge the user the right amount." Remove everything else from the caller's view. The lib handles grant→estimate→reserve→call→settle internally; the caller supplies the actual LLM invocation.

**Primary interaction loop:**
```go
svc, _ := credits.New(db, cfg)
out, err := svc.Run(ctx, credits.Request{
    UserID: userID, Model: model, Prompt: prompt, MaxOut: 4000,
    Billing: credits.Managed,          // managed | byok | explicit
    Call: func(ctx context.Context, maxOut int) (usage credits.Usage, text string, err error) {
        return llm.GenerateText(ctx, model, prompt, maxOut)  // mapper inside
    },
})
```
Billing-mode, grant, reserve, settle, usage all handled by `Run`. Explicit APIs (`Grant`, `Balance`) remain for admin/refunds.

**Trade-offs:** pros — one-line integration (perfect for treinamento's `chat_send.go` and gogogo's gateway), impossible to mis-sequence, less boilerplate; cons — less control for advanced flows (billing mode per sub-step), closure indirection is less greppable, harder to test in isolation; effort — medium (Run must be built on the same primitives); risk — low-medium (hidden sequence is a black box for debugging).

**State Coverage:**

| API method | Type | OK | Err | Idempotent | Concurr. | Empty | Coverage |
|-----------|------|:--:|:--:|:--:|:--:|:--:|:--:|
| Run | Int | ✅ | ✅ | ✅ | ✅ | ✅ | 5/5 |
| Grant/Refund | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Balance | Int | ✅ | ✅ | N/A | ✅ | ✅ | 4/4 |
| Reconcile | Int | ✅ | ✅ | ✅ | N/A | ✅ | 4/4 |

---

## Proposal E — Expert / Command-First (chained commands + functional options)

**Work Pattern:** Operate, command-palette style — each operation is a typed command with functional options; experts chain them. Implication: dense but composable; callers who know the domain skip helpers.

**Philosophy:** Optimize for fluent throughput. `credits.Reserve(...)` returns a builder; options set policy (timeout, billing mode, idempotency); a `.Commit()` executes. Batch and admin ops are first-class.

**Primary interaction loop:**
```go
credits.New(db, cfg)                      // *credits.Client
res := c.Reserve(userID).Amount(500).TTL(5 * time.Minute).Key(reqID)
if err := res.Commit(ctx); err != nil { /* handle */ }
// later:
res.Settle(cost)    // or res.Release()
// admin batch:
c.Grant(userID, 1000).Reason("promo").Key("promo:jul").Commit(ctx)
```

**Trade-offs:** pros — expressive, progressive disclosure (`.TTL()` optional), minimal per-call params; cons — builder pattern hides defaults, more API surface to document, error handling scattered across commit points; effort — high (largest surface); risk — medium (fluent APIs invite misuse); maintainability — medium.

**State Coverage:**

| API method | Type | OK | Err | Idempotent | Concurr. | Empty | Coverage |
|-----------|------|:--:|:--:|:--:|:--:|:--:|:--:|
| Reserve builder | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Commit | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Settle/Release | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Grant batch | Int | ✅ | ✅ | ✅ | ✅ | N/A | 4/4 |
| Balance | Int | ✅ | ✅ | N/A | ✅ | ✅ | 4/4 |

---

## Hybrid Recommendation

**Primary: Proposal A** (conventional `*credits.Service`) — because PLAN.md pins the exact
signatures and both apps' tests target them; A keeps the mapping to goai trivially
diff-able and satisfies the zero-dep, testable, idiomatic requirements.

**Borrow from D:** add a convenience `Run(ctx, Request, Call)` wrapper built ON TOP of the
A primitives (same internal path, no new sequencing logic). This gives treinamento's
`chat_send.go` a one-line integration while keeping the granular API for gogogo's gateway
and tests.

**Borrow from E:** functional options ONLY where they reduce call-site noise without
hiding behavior — e.g. `credits.New(db, WithMonthlyCredits(1000))` config options instead
of a 6-field positional config. Not the chainable command builders (they'd duplicate the
verb methods).

**Do NOT combine:** D's closure-only model with E's builders (two competing sequencing
models = confusion). Do NOT add E's batch command surface in v1 — `Grant` with an
`idempotency_key` already covers replay; batch is YAGNI.

**Preserved trade-offs:** the 5-call ceremony of A stays for gogogo's gateway (explicit
per-step control); the Run wrapper is the only simplification added. Errors stay as
values (`ErrInsufficientCredits`, `ErrDuplicateGrant`, `ErrReservationClosed`,
`ErrUnknownModel`) — no panics, no sentinel-wrapping helpers.

**Selection order (for the interface gate):**
1. Hybrid A+D (recommended) — `*credits.Service` + `Run` convenience wrapper
2. Pure A — conventional only
3. D — transactional envelope only
