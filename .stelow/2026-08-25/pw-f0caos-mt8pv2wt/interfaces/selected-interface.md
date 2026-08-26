---
wf: pw-f0caos-mt8pv2wt
intent: new-product
appetite: Core
review_mode: Auto
approved_via: auto (Review Mode=Auto)
selected_at: 2026-08-25T16:25:00Z
---
# Selected Interface

**Hybrid (A + E):** Conventional Go service facade (`*Credits`) with an
explicit-transaction escape hatch (`ReserveWithTx`/`SettleWithTx`), plus
`CredentialStore` and `HandleBYOK` relay.

Rationale: typed/discoverable API for ordinary Go apps + explicit tx control
for the reconcile/settle billing path. E fully, D rejected (untyped risk in a
payments-adjacent domain).

## Interface shape
```
svc := credits.New(db, credits.Config{Pricer: p, MicrounitsPerCredit})
svc.Grant(ctx, user, 1000, "signup", idempotKey)   // default own-tx
svc.Reserve(ctx, user, reqID, amount)  → (*Reservation, error)
svc.Settle(ctx, rsv, usage)            // usage ≤ reserved; >reserved→ErrReservationExceeded
svc.Release(ctx, rsv)
svc.GetBalance(ctx, user)
svc.EnsureMonthlyGrant(ctx, user)
svc.Reconcile(ctx, clock)
svc.Cost(model, promptTok, maxOutTok) → (Credits, error)
svc.ReserveWithTx(ctx, tx, ...) / SettleWithTx(...)  // E escape hatch
svc.CredentialStore().Put/Get/Delete
svc.HandleBYOK(next http.Handler) → http.Handler
```
