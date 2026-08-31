# Changelog

Todas as mudanças notáveis em `ai-credits` são documentadas neste arquivo.

O formato segue [Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/),
e o projeto adere ao [Versionamento Semântico](https://semver.org/lang/pt-BR/).

## [v0.1.1] - 2026-08-28

### Fixed
- README example: `Settle` agora recebe **credits** (via `svc.Credits`), não
  micro-units — o exemplo anterior quebrava com `ErrReservationExceeded`.
- README example: usa `OpenSQLite` (pragmas WAL/busy_timeout), não um
  `sql.Open` nu que podia deadlock em escrita concorrente.
- PLAN.md §3/§6.2/§6.3 sincronizados com a API real (Reserve/Settle/Release
  trabalham em credits e recebem `*Reservation`; `EnsureMonthlyGrant` retorna
  `(bool, error)`; novo `Credits(ctx, usage)`). Documentada a política de
  under-reserve (§6.3.1) e a mitigação no app.

## [v0.4.1] - 2026-08-31

### Added
- **Payment integrations no módulo raiz**: `payments` e `stripe` agora fazem
  parte do módulo principal (antes viviam em branches/matriz separada), com
  checkout, worker de eventos com backoff e dead-letter, e reconciliação.
- **Durable settlement outbox**: `EnqueueSettlement` / `SettleViaOutbox` /
  `ProcessSettlementOutbox` — o settle sobrevive a crash do app após o
  provider retornar (`.request_id` idempotente).
- **`invoice.paid` auto-grant**: o adaptador Stripe concede créditos do ciclo
  automaticamente ao receber o webhook (inclui proration skip e
  `payment_failed` → `past_due`).
- **BYOK timeout + rotação**: credenciais com `expires_at`/rotação e
  boundary hardening do relay.
- `OpenSQLite`, `NewRequestID`, `EstimateTokens` (movidos de gogogo/ensaiter).

### Changed
- **Webhook de invoice usa `stripego.Invoice` do SDK oficial**: o struct JSON
  manual de ~40 linhas + `extractStringID` foram substituídos pelo tipo do
  SDK; mantido um `invoiceShadow` pequeno só para os campos v2 não modelados
  (subscription_details / parent.subscription_item_details).
- DRY: duplicação das integrações removida; settlement e billing coexistem
  num único `settlement.go`.

### Fixed
- **43 falhas de golangci-lint** (noctx, mnd, modernize, govet, revive,
  gofumpt, nolintlint, lll) em `payments/`, `credits/`, `stripe/`.
- **Nil deref em `SettleViaOutbox`**: o erro usava `err.Error()` após `err`
  ser `nil` (deveria ser `err2`).
- **Colunas do settlement corrigidas** e ordem do `subID` nas queries de
  proration.

## [v0.4.0] - 2026-08-29

### Fixed
- **Money safety — double-refund guard**: `finalize` agora faz CAS no status
  (`UPDATE … WHERE id=? AND status='reserved'` + `RowsAffected()==0`), então
  um `Settle`/`Release` concorrente (ou repetido) não reembolsa duas vezes.
- **Money safety — settle pelo valor do DB, não do struct do caller**: o
  reembolso/overage é derivado da linha `credit_reservations` relida dentro
  da write tx, não do `*Reservation` que o caller passa (imune a struct
  adulterado/obsoleto).
- **Write-lock determinístico**: `OpenSQLite` agora seta `_txlock=immediate`
  no DSN. Antes a garantia de `BEGIN IMMEDIATE` dependia do driver (ncruces
  honra `LevelSerializable`; modernc ignora — usava begin deferred). Com o
  `_txlock` explícito, escritas concorrentes serializam no lock em qualquer
  driver. Comentário de `immediateTx` reescrito (o antigo era falso p/ modernc).
- **BYOK metering não é cancelado pelo hangup do cliente**: o registro de
  `llm_usage` por SSE usa agora um contexto destacado da request
  (`WithTimeout(Background, 10s)`), pois o ctx da request já está cancelado
  quando um stream longo termina — a linha era descartada silenciosamente.
- **Pricing aritmética inteira (sem float)**: `cost`, `Credits`, `EstimateMax`
  e `creditsFromMicrounits` usam divisão inteira com arredondamento para cima
  (`(a+b-1)/b`), eliminando erro de fronteira de float que podia subcobrar 1
  crédito. `reserveMargin` virou fração inteira 6/5.
- **Validação de `Grant`/`Refund`**: ambos rejeitam `amount <= 0` (só o
  `adjust` interno aceita valor com sinal), travando um caller bugado/malicioso
  que movimentava saldo negativo arbitrário.

## [v0.3.0] - 2026-08-28

### Changed
- **Under-reserve auto-draws overage**: `Settle` deixa de retornar
  `ErrReservationExceeded` quando o custo real excede a reserva. Agora cobra o
  custo integral e **debita o excedente do saldo disponível** (`reservation_overage`),
  padrão da indústria (Stripe credits / Orb on-demand). Se o saldo ficar
  negativo, o próximo `Reserve` falha (dívida/fail-closed). Invariante
  `balance == SUM(ledger)` preservado (verificado por `Reconcile`).
  `ErrReservationExceeded` mantido como deprecated (nunca mais retornado).

### Fixed
- **BYOK streaming metering**: o relay não pedia `stream_options.include_usage`,
  então a maioria dos providers OpenAI-compat omitia `usage` no chunk SSE final
  e o metering de streaming não via nada. Agora injeta `include_usage=true` em
  `stream:true` (corpo preservado em não-streaming).
- **Bug de `ContentLength` no relay**: o corpo era trocado pelo patched mas o
  `ContentLength` mantinha o valor original (menor), truncando o body upstream
  sempre que o patch (ou um body maior) ultrapassava o tamanho original.

## [v0.2.0] - 2026-08-28

### Added
- **BYOK relay metering**: o relay agora captura o `usage` da resposta upstream
  (JSON não-streaming OU o último chunk `data: {json}` antes de `[DONE]` no
  streaming) e persiste uma linha em `llm_usage` com `billing_mode=byok`,
  `credits_charged=0`. BYOK vira passível de analytics/throttling sem cobrar o
  usuário. `X-Byok-Request-Id` no inbound permite idempotência (`byok:<id>`).
- **Subscription lifecycle** (gate de entitlement): tabela `subscriptions`
  (user_id, plan, status) + `SetSubscription` / `CancelSubscription` /
  `Subscription`. `EnsureMonthlyGrant` recusa conceder quando a assinatura
  existe e não está `active`. Sem registro (apps pré-pago) o grant continua
  incondicional — retrocompatível.

### Fixed
- (v0.1.1) README example usava micro-units no `Settle` (devia ser credits) e
  `sql.Open` sem pragmas WAL — trocado para `OpenSQLite` + `svc.Credits`.

## [v0.1.0] - 2026-08-28

### Fixed
- Bloqueio `balance == SUM(ledger)` mantido (o balance materializado nunca é
  mutado fora de uma transação da lib).

### Changed
- Paginador de pricing com defaults embutidos (modelos gpt-4o-mini, gpt-4o,
  claude-3-5-haiku, claude-3-5-sonnet) e `microunits_per_credit` padrão.

### Added
- Stage 0–5 do plano original: scaffold, schema idempotente, ledger imutável
  com balance materializado, pricing JSON configurável, Reserva/Settle/Release
  para chamadas de custo incerto, grant mensal idempotente por período,
  reconcile (detecção de drift + expiração de reservas obsoletas), store de
  credenciais BYOK (XChaCha20-Poly1305) + relay HTTP.

[Unreleased]: https://github.com/calionauta/ai-credits/compare/v0.4.1...HEAD
[v0.4.1]: https://github.com/calionauta/ai-credits/releases/tag/v0.4.1
[v0.4.0]: https://github.com/calionauta/ai-credits/releases/tag/v0.4.0
[v0.3.0]: https://github.com/calionauta/ai-credits/releases/tag/v0.3.0
[v0.2.0]: https://github.com/calionauta/ai-credits/releases/tag/v0.2.0
[v0.1.1]: https://github.com/calionauta/ai-credits/releases/tag/v0.1.1
[v0.1.0]: https://github.com/calionauta/ai-credits/releases/tag/v0.1.0