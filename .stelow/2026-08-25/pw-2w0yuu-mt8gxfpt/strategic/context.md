# Strategic Context — ai-credits

**JTBD**: apps that resell/subsidize LLM usage (gogogo template, treinamento) need
metered, credit-based billing with BYOK — without adopting a SaaS metering platform.

**Market/domain**: billing/credits ledger. Key insight from PLAN.md §0.2 — evaluated
goquota (no SQLite), byok-relay (Node), doubleentry (Rust), ledger/azex (PG/no license).
Verdict: greenfield zero-dep Go lib. SQLite via app-provided *sql.DB is the right store
for single-binary Go apps (gogogo/treinamento both run SQLite).

**Strategic choices locked in PLAN.md**:
1. Separate repo (Go internal/ rule + MVS graph isolation from PocketBase pins).
2. Reserve→Execute→Capture→Release model (output tokens unknowable pre-call).
3. Immutable ledger + materialized balance; lazy idempotent monthly grant (no scheduler).
4. Refund policy: allow negative balance, block managed Reserve at balance < 0.
5. BYOK: XChaCha20-Poly1305 + in-process ReverseProxy relay (no Node dependency).
6. Explicit billing mode per request (managed|byok|explicit default).

**Risks**: driver-agnostic *sql.DB (SQLite-specific SQL in schema.sql — app must pass SQLite);
negative-balance policy is a product decision (documented); relay identity relies on app
auth middleware stripping X-Auth-User. All accepted with mitigations in PLAN.md.
