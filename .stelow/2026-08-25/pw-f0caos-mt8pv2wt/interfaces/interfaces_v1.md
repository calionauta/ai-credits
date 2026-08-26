# ai-credits — Interface Alternatives (public Go API surface)

**Work artifact for a library, not a UI.** The "interface" a consumer developer
(gogogo/treinamento) interacts with is the exported Go API. Archetypes adapted:
A=Conventional Go idiom, D=Radical simplicity, E=Expert/command-first.
Appetite: Core → 3 proposals + hybrid.

---

## Proposal A — Conventional Go Service Facade

**Work Pattern:** Configure + Operate
**Implication:** A single `*Credits` service struct owns all state; consumers
call typed methods. Composition follows standard Go package conventions.
**Layout Choice:** Package `credits` with one aggregate service type + small
value types; mirrors most Go SDKs a consumer already knows.

### Philosophy
Predictable, familiar Go API. One entry point (`credits.New(db, cfg)`), a
service struct grouping all operations, and value types (`Transaction`,
`Reservation`, `Usage`) that hide SQL. Lowest learning curve for the gogogo
and treinamento teams.

### Breadboarding / loop
```
New(db,cfg) → svc
svc.Grant(user, 1000, "signup", key)   // idempotent
svc.GetBalance(user)
svc.Reserve(user, reqID, 500)          → (*Reservation, error)
svc.Settle(rsv, usage)                  // usage ≤ reserved
svc.Release(rsv)
svc.EnsureMonthlyGrant(user)
svc.Reconcile(ctx)
svc.Cost(model, prompt, maxOut)
svc.Pricer() / svc.CredentialStore()
svc.HandleBYOK(handler)                 // wraps http.Handler
```

### Trade-offs
- + Most familiar; easy onboarding; typed errors.
- − Couples all ops onto one struct; larger type surface.

### Smell audit
Avoids: god-object (grouped by domain sub-interfaces), surprise API,
over-abstraction.

---

## Proposal D — Radical Simplicity (single-purpose funcs)

**Work Pattern:** Operate
**Layout Choice:** Package-level functions operating on a thin `*DB` handle —
no service struct, no configuration object beyond `credits.Open`.

### Philosophy
Smallest surface that solves the job. User holds a `*credits.Client`
(thin wrapper over `*sql.DB` + config) and calls free-standing functions.
Fewest concepts: no Reservation type — `Reserve` returns an opaque ID string.

### Loop
```
c, _ := credits.Open(db, cfg)
credits.Grant(c, user, 1000, "signup", idem)
credits.Reserve(c, user, reqID, 500)   → string rsvID
credits.Settle(c, rsvID, usage)
credits.Release(c, rsvID)
credits.Balance(c, user)
```
Types kept to plain strings/ints; heavy use of `context.Context` as first arg.

### Trade-offs
- + Tiny API, minimal concepts, trivially testable.
- − Opaque IDs hide structure; less discoverable/type-safe; harder to extend.

### Smell audit
Avoids: premature abstraction, over-engineering, config sprawl.

---

## Proposal E — Expert / Command-First (table + low-level primitives)

**Work Pattern:** Configure
**Layout Choice:** A `Pricer` table type + transaction-friendly low-level
primitives; consumer retains full control over DB transactions and batching.

### Philosophy
Optimized for developers who want direct control: explicit transaction
boundaries (caller passes a `*sql.Tx`), table-driven pricing, and minimal
magic. Best throughput for high-volume billing paths.

### Loop
```
p, _ := credits.NewPricer(cfg)
tx, _ := db.Begin()
credits.Enqueue(tx, LedgerEntry{...})   // raw, explicit
credits.Apply(tx, user)                  // materialize balance
tx.Commit()
```
Reserve/Settle as explicit SQL-level primitives; `Reserve` returns a
`*Reservation` with raw fields (amount, status, timestamps).

### Trade-offs
- + Maximum control, no hidden "magic"; high throughput, composable with app tx.
- − Verbose; easy to misuse (caller owns correctness of tx boundaries).

### Smell audit
Avoids: hidden magic, "smart default that hides what it did", opaque scheduling.

---

## Hybrid (Recommended)

Combine **A's service facade** (typed, discoverable, safe for both teams) with
**E's explicit transaction discipline** as an escape hatch: `*Credits` methods
run their own tx by default, but each accepts an optional `WithTx(*sql.Tx)`
option for consumers who need atomicity with app-owned writes.

**Why:** gogogo/treinamento are ordinary Go apps that value a clear API (A),
but the reconcile/settle path benefits from explicit transaction control (E).
D's free-standing funcs are too untyped for a payments-adjacent domain and
risk misuse.

**Interface shape (hybrid):**
```
svc := credits.New(db, credits.Config{Pricer: p, MicrounitsPerCredit})
svc.Grant(...)               // default own-tx
svc.ReserveWithTx(ctx, tx, ...)  // E escape hatch for app-atomic flows
svc.CredentialStore()
svc.HandleBYOK(next)         // relay handler
```
Work Pattern: Configure + Operate in one coherent structure.

**Verdict:** Proceed with **Hybrid (A + E)** as the selected interface for
tech planning. D recorded as rejected (untyped risk in billing domain).