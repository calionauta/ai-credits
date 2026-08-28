# ai-credits — Plano refinado de implementação

> Documento executável: outra LLM deve conseguir implementar seguindo este arquivo,
> na ordem dos estágios, sem precisar de mais contexto. Fatos verificados em 2026-08-24.

## 0. Decisão de arquitetura (registro de decisão)

### 0.1 Repositório: NOVO repo `github.com/calionauta/ai-credits` — NÃO dentro do gogogo

Motivos (verificados):

1. **Regra `internal/` do Go**: código em `internal/` do gogogo não pode ser importado
   por outro módulo. "Lib usável em outros projetos" não pode nascer em `internal/`.
2. **Gráfico de dependências**: se a lib morasse em `pkg/` do módulo gogogo, todo
   consumidor herdaria as `require` do gogogo (PocketBase, NATS, Datastar, Templ, etc.)
   no gráfico MVS → conflitos de versão reais. O treinamento pinna PocketBase v0.39.1;
   o gogogo usa outra versão. Quebra na prática.
3. **Filosofia do gogogo**: o gogogo é um *template* de referência; um sistema de
   billing de ~10 pacotes contraria a filosofia SCOPE/removível. A lib entra no gogogo
   como **dependência** (plugin `features/credits/`), não como código do template.
4. **Copiar no treinamento = fork drift** (o usuário aceitou "copiando ou referenciando";
   referenciar é estritamente melhor para um sistema deste tamanho).
5. Lib própria: ciclo de testes/CI independente, versionamento por tag, `go get` simples.

### 0.2 Veredito das dependências de terceiros (evidência em 2026-08-24)

| Lib (plano original) | Veredito | Evidência |
|---|---|---|
| `zendev-sh/goai` | ✅ **usar, mas só no app** (nunca importada pela lib) | Pura Go; já em gogogo v0.9.6 e treinamento v0.7.6. `provider.Usage{InputTokens, OutputTokens, TotalTokens, ReasoningTokens, CacheReadTokens, CacheWriteTokens}` cobre a tabela `llm_usage`. **Mismatch de versão entre os dois projetos** → a lib define o próprio `Usage`; cada app mapeia goai→lib em ~8 linhas. |
| `mihaimyh/goquota` | ❌ **descartar v1** | Storage adapters: redis/memory/postgres/tiered/firestore — **NÃO tem SQLite**. Escrever o adapter SQLite ≈ reimplementar o ledger. 0 estrelas, sem histórico de produção. O plano original já previa "native baseline"; a avaliação (Stage 4 do plano) está respondida: descartar. |
| `avikalpg/byok-relay` | ❌ **descartar como dependência** | É **JavaScript/Node**, serviço separado — viola "um único binário Go". Substituir por **relay Go em processo** na própria lib (~150 linhas, `httputil.ReverseProxy` + injeção de chave). O relay JS continua uma opção para clientes thin/frontend-only, mas não é dependência. |
| `hupe1980/doubleentry` | ❌ **descartar** | É **Rust** — impossível linkar em binário Go sem cgo/FFI. |
| `mkmbhs/ledger` | ❌ **descartar** | Backend **PostgreSQL** — viola o requisito SQLite. |
| `azex-ai/ledger` | ❌ **descartar** | **Sem licença** (GitHub API: `license: null`) — risco legal; 1 estrela. |
| `stripe/stripe-go` | ✅ **opcional, só no app** | Go puro, single-binary OK. Nunca no core da lib; entra como pacote do plugin gogogo (opcional, gated por env). |
| SQLite (ncruces/go-sqlite3 ou modernc) | ✅ | Driver fornecido pelo app (`*sql.DB`); a lib é agnóstica de driver. |

**Dependências finais da lib: ZERO além de `golang.org/x/crypto`** (XChaCha20-Poly1305
para criptografar credenciais BYOK). Nada de Redis/Postgres/ClickHouse/Kafka/Node.

### 0.3 O que o plano original acerta (manter)

- Modelo **reserve → execute → capture → release** (tokens de saída são desconhecidos
  antes da chamada).
- **Ledger imutável + saldo materializado**; nunca mutar saldo sem transação.
- **Idempotência** em grants/webhooks/grants mensais/reservas (chaves estáveis).
- SQLite é suficiente; OpenMeter/ClickHouse/Kafka fora de escopo.
- Implementação em estágios; billing mode explícito como default.
- Preço não hardcoded em handlers; `pricing_version` em todo registro de uso.

### 0.4 O que o plano original erra (corrigir)

- Conflito "sistema do gogogo" × "lib reutilizável" — resolvido em 0.1.
- Dependências não verificadas — resolvido em 0.2.
- `Reserve` com typo de assinatura (`(string, error)` sem parêntese).
- Política de reembolso não decidida — ver §6.4 (escolher UMA).
- Grant mensal dependente de scheduler — substituído por **grant preguiçoso idempotente**
  (`EnsureMonthlyGrant` chamado antes da primeira leitura de saldo do período).
- Sem timeout de reserva configurável — ver §6.5.
- Criptografia BYOK sem detalhe — XChaCha20-Poly1305 com chave de env.
- Dono do schema indefinido — `EnsureSchema(ctx, db)` idempotente na lib (sem framework
  de migração; apps chamam no boot).
- Datas: trocar `TEXT` por **INTEGER unix timestamp** (sem bugs de TZ, comparações
  simples no reconcile).

---

## 1. Escopo

### Faz (v1)
- Metragem de uso de LLM (`llm_usage`) com pricing versionado.
- Motor de pricing por modelo (JSON configurável, defaults embutidos).
- Ledger de créditos: saldo materializado + ledger imutável.
- Reservas/capture/release para chamadas de custo incerto.
- Grant mensal preguiçoso idempotente (sem scheduler).
- Créditos pré-pagos (top-up via Grant com chave de idempotência) — pagamento via Stripe no app (stripe-go; Stage 8).
- BYOK: credenciais criptografadas + relay em processo.
- Modo de cobrança explícito por request (`managed` | `byok`).
- Reconcile: saldo vs ledger + expiração de reservas órfãs.

### Não faz (v1)
- Stripe dentro da lib (fica no app, obrigatório no v1 como gateway de pagamento — ver Stage 8).
- Dupla entrada contábil, múltiplas wallets, transferências.
- Buckets com ordem de consumo configurável (pool único; ver §14).
- Streaming com settlement parcial (uso é registrado no fim do stream; ver §14).
- OpenMeter/ClickHouse/Kafka/Redis/Postgres.
- Scheduler interno (grant mensal é preguiçoso; reconcile é chamado pelo app).

---

## 2. Layout do repo

```
ai-credits/
  go.mod                  module github.com/calionauta/ai-credits  (go 1.26)
  LICENSE                 MIT (igual ao gogogo)
  AGENTS.md               convenções do repo (curto)
  README.md               exemplo de uso
  PLAN.md                 este documento
  credits/
    credits.go            Service, Config, New()
    types.go              errors, Usage, Result
    ledger.go             Grant, Refund, saldo materializado
    reservation.go        Reserve, Settle, Release
    monthly.go            EnsureMonthlyGrant
    reconcile.go          Reconcile
    pricing.go            Engine: Load, Cost, EstimateMax, Credits
    usage.go              RecordUsage
    schema.go             EnsureSchema (go:embed schema.sql)
    schema.sql
  byok/
    relay.go              http.Handler do relay
    credentials.go        CredentialStore (XChaCha20-Poly1305)
```

Dependências: `golang.org/x/crypto` (única). Sem `internal/` na lib (tudo exportado;
é lib). Testes `_test.go` junto de cada arquivo.

---

## 3. Contratos públicos (assinaturas Go definitivas)

```go
// package credits

type Config struct {
    DefaultBillingMode   string          // "managed" | "byok" | "explicit" (default "explicit")
    MonthlyCredits       int64           // grant mensal padrão (0 = desligado)
    PlanMonthlyCredits   map[string]int64 // plan -> grant mensal (opcional)
    ReservationTimeout   time.Duration   // default 5 * time.Minute
    PricingReader        io.Reader       // JSON de preços; nil = defaults embutidos
    PricingVersion       string          // default "builtin-2026-08"
    Now                  func() time.Time // injetável p/ testes; default time.Now
}

func New(db *sql.DB, cfg Config) (*Service, error)  // chama EnsureSchema
func (s *Service) EnsureSchema(ctx context.Context) error // idempotente, CREATE TABLE IF NOT EXISTS

// Ledger
func (s *Service) Balance(ctx context.Context, userID string) (int64, error)
func (s *Service) Grant(ctx context.Context, userID string, amount int64, source, reason, idempotencyKey string) error // idempotente
func (s *Service) Refund(ctx context.Context, userID string, amount int64, source, reason, idempotencyKey string) error

// Reservas — trabalham em CREDITS (não micro-units); Reserve devolve o
// *Reservation concreto (não um id string). Idempotente por requestID.
func (s *Service) Reserve(ctx context.Context, userID, requestID string, amount int64) (*Reservation, error)
func (s *Service) Settle(ctx context.Context, r *Reservation, actualCredits int64) error // capture + release do excedente
func (s *Service) Release(ctx context.Context, r *Reservation) error

type Reservation struct {
	ID        string
	UserID    string
	RequestID string
	Amount    int64 // credits reservados (= estimativa máxima)
	Status    string // reserved|captured|released|expired
}

// Mensal
func (s *Service) EnsureMonthlyGrant(ctx context.Context, userID, plan string) (bool, error) // idempotente por user+period; true se concedeu

// Uso e pricing
func (s *Service) RecordUsage(ctx context.Context, u Usage) error
func (s *Service) Cost(ctx context.Context, u Usage) (microunits int64, err error)   // pricing engine
func (s *Service) Credits(ctx context.Context, u Usage) (int64, error)               // ceil(cost / per-credit) — use ESTE p/ Settle
func (s *Service) EstimateMax(ctx context.Context, model string, inputTokens, maxOutputTokens int) (credits int64, err error)

// Reconcile
func (s *Service) Reconcile(ctx context.Context) ([]Mismatch, error)

// Types
type Usage struct {
    RequestID       string
    UserID          string
    Provider        string
    Model           string
    BillingMode     string    // "managed" | "byok"
    InputTokens     int
    OutputTokens    int
    CachedTokens    int       // CacheReadTokens
    ReasoningTokens int
    CostMicrounits  int64     // preenchido pelo app via Cost(); 0 em BYOK se quiser
    CreditsCharged  int64     // >0 em managed; 0 em BYOK
    PricingVersion  string
    CreatedAt       time.Time
}

var (
    ErrInsufficientCredits = errors.New("credits: insufficient credits")
    ErrReservationNotFound = errors.New("credits: reservation not found")
    ErrReservationClosed   = errors.New("credits: reservation already settled or released")
    ErrDuplicateGrant      = errors.New("credits: idempotency key already used")
    ErrUnknownModel        = errors.New("credits: unknown model in pricing table")
)

// package byok
type CredentialStore struct{ /* db *sql.DB, key []byte */ }
func NewCredentialStore(db *sql.DB, encKey []byte) (*CredentialStore, error) // key = 32 bytes
func (c *CredentialStore) Put(ctx context.Context, userID, provider, apiKey string) error
func (c *CredentialStore) Get(ctx context.Context, userID, provider string) (string, error)
func (c *CredentialStore) Delete(ctx context.Context, userID, provider string) error

type Relay struct{ /* creds, client, bases map[string]string */ }
func NewRelay(creds *CredentialStore, providerBases map[string]string, client *http.Client) *Relay
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) // ver §7
```

### Adaptador GoAI (fica NO APP, nunca na lib)

```go
// Exemplo gogogo: features/credits/gateway.go (usando internal/llm)
func ToLibUsage(r *goai.Result, mode string) credits.Usage {
    u := r.TotalUsage // provider.Usage
    return credits.Usage{
        Provider: r.Provider, Model: r.Model,
        BillingMode: mode,
        InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
        CachedTokens: u.CacheReadTokens, ReasoningTokens: u.ReasoningTokens,
    }
}
```
(v0.7.6 e v0.9.6 expõem `Result.TotalUsage provider.Usage` com os mesmos campos acima —
verificado no cache de módulos. Se o campo `Model` não existir no Result, o app preenche
com o model que ele mesmo enviou.)

---

## 4. Schema SQL (definitivo)

```sql
-- credits/schema.sql — EnsureSchema executa com CREATE TABLE IF NOT EXISTS
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS credit_accounts (
    user_id    TEXT PRIMARY KEY,
    balance    INTEGER NOT NULL DEFAULT 0,   -- saldo materializado (créditos)
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credit_transactions (
    id              TEXT PRIMARY KEY,          -- hex crypto/rand 16 bytes
    user_id         TEXT NOT NULL,
    amount          INTEGER NOT NULL,          -- +/- créditos
    type            TEXT NOT NULL,             -- grant|monthly|topup|refund|reservation|reservation_release|adjustment
    source          TEXT NOT NULL,             -- signup|admin|stripe|monthly|llm_request|reconcile|byok? (não)
    reference_id    TEXT,                      -- reservation_id | payment_intent_id | request_id
    idempotency_key TEXT UNIQUE,               -- ex: "stripe:pi_123", "monthly:u1:2026-08", "req:abc"
    metadata        TEXT,                      -- JSON opcional
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_tx_user ON credit_transactions(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    request_id      TEXT UNIQUE NOT NULL,      -- idempotência da reserva
    amount          INTEGER NOT NULL,          -- créditos reservados (= estimativa máxima)
    captured_amount INTEGER NOT NULL DEFAULT 0,
    released_amount INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,             -- reserved|captured|released|expired
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_usage (
    id                        TEXT PRIMARY KEY,
    request_id                TEXT UNIQUE NOT NULL,
    user_id                   TEXT NOT NULL,
    provider                  TEXT NOT NULL,
    model                     TEXT NOT NULL,
    billing_mode              TEXT NOT NULL,   -- managed|byok
    input_tokens              INTEGER NOT NULL DEFAULT 0,
    output_tokens             INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens       INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens          INTEGER NOT NULL DEFAULT 0,
    estimated_cost_microunits INTEGER,
    actual_cost_microunits    INTEGER,
    credits_charged           INTEGER NOT NULL DEFAULT 0,
    pricing_version           TEXT NOT NULL,
    created_at                INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_user ON llm_usage(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(model);

CREATE TABLE IF NOT EXISTS byok_credentials (
    user_id       TEXT NOT NULL,
    provider      TEXT NOT NULL,               -- "openai"|"openrouter"|... (chave do mapa de bases)
    encrypted_key BLOB NOT NULL,               -- base64(XChaCha20-Poly1305(apiKey))
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider)
);
```

Notas:
- Timestamps `INTEGER` (unix). A lib escreve `time.Now().Unix()` (ou `cfg.Now`).
- `credit_accounts` é cache materializado; a fonte de verdade é `credit_transactions`.
- O saldo NUNCA é mutado fora das transações da lib.

---

## 5. Pricing

### 5.1 Formato JSON (env `CREDITS_PRICING_FILE`; senão defaults embutidos)

```json
{
  "version": "2026-08",
  "microunits_per_credit": 1000,
  "models": {
    "gpt-4o-mini":  { "input_per_mtok": 150000, "output_per_mtok": 600000, "cached_input_per_mtok": 150000, "reasoning_per_mtok": 0 },
    "claude-3-5-haiku": { "input_per_mtok": 800000, "output_per_mtok": 4000000, "cached_input_per_mtok": 80000, "reasoning_per_mtok": 0 }
  }
}
```

Unidades (documentar no README):
- `*_per_mtok` = microunits (microdólares) por 1M tokens. Ex.: $0.15/1M → 150000.
- `microunits_per_credit` = 1000 ⇒ **1 crédito = $0.001** (default do plano original).
- Defaults embutidos: gpt-4o-mini, gpt-4o, claude-3-5-haiku, claude-3-5-sonnet (preços
  públicos de 2026-08; o app pode sobrescrever por arquivo).

### 5.2 Fórmulas

```
cost_microunits = in/1e6*input_per_mtok + out/1e6*output_per_mtok
                + cached/1e6*cached_input_per_mtok + reasoning/1e6*reasoning_per_mtok
credits = ceil(cost_microunits / microunits_per_credit)
```

- `Cost(ctx, u)` → microunits (arredonda para cima no final).
- `EstimateMax(model, inputTokens, maxOutputTokens)` → credits conservadores:
  `credits(input + maxOutput, ignorando cache)` + margem `* 1.2` (constante `RESERVE_MARGIN`).
- Modelo desconhecido → `ErrUnknownModel` (não silenciar; o app trata como config).

---

## 6. Regras de domínio

### 6.1 Reserve (transação única, idempotente)

```
tx:
  INSERT INTO credit_reservations (id, user_id, request_id, amount, status, ...)
    VALUES (?, ?, ?, ?, 'reserved', ...)
    ON CONFLICT(request_id) DO NOTHING;            -- duplicata: retorna reserva existente
  SELECT id FROM credit_reservations WHERE request_id = ?;   -- resolve id (novo ou existente)
  se reserva já existe e status != reserved → ErrReservationClosed
  UPDATE credit_accounts SET balance = balance - ?, updated_at = ?
    WHERE user_id = ? AND balance >= ?;            -- guard anti-negativo
  se rowcount == 0 → ROLLBACK → ErrInsufficientCredits
  INSERT INTO credit_transactions (type='reservation', amount=-?, reference_id=reservation_id, ...)
commit
```

### 6.2 Settle(r *Reservation, actualCredits)

`actualCredits` é em CREDITS (não micro-units). Se o custo real EXCEDER a
reserva, o excedente é **debitado do saldo disponível** (overage) — nunca
recusa cobrar trabalho já consumido, e nunca limita silenciosamente o custo.
Se o débito deixar o saldo negativo, o próximo `Reserve` falha (fail-closed /
sinal de dunning — o saldo negativo é uma dívida a cobrar).

```
tx:
  SELECT amount, status FROM credit_reservations WHERE id = ?   -- senão ErrReservationNotFound
  se status != 'reserved' → ErrReservationClosed
  se actualCost <= amount:
    excess = amount - actualCost
    se excess > 0:
      INSERT INTO credit_transactions (type='reservation_release', amount=+excess, ...)
      UPDATE credit_accounts SET balance = balance + ? WHERE user_id = ?
  senão:  -- actualCost > amount (overage)
    overage = actualCost - amount
    INSERT INTO credit_transactions (type='reservation_overage', amount=-overage, ...)
    UPDATE credit_accounts SET balance = balance - ? WHERE user_id = ?
  UPDATE credit_reservations SET status='captured', captured_amount=actualCost, ... WHERE id = ?
commit
```

### 6.3 Release(r *Reservation)

Igual ao Settle com `actualCredits = 0` (ou caminho próprio que devolve `amount` inteiro,
status='released'). Release nunca gera overage (cancela a chamada inteira).

### 6.3.1 Under-reserve (custo real > reserva) — política

A lib **gera o overage automaticamente** no Settle (débito do excedente do saldo,
§6.2) — nenhuma ação do app é necessária. `EstimateMax` é conservador (1.2×),
então overage é raro; quando ocorre, o custo real é cobrado integralmente e o saldo
pode ficar negativo (dívida). O app pode: (a) deixar a dívida pendente e cobrar depois
(Stripe invoice/dunning), ou (b) fazer upgrade do plano para repor crédito. Se um app
preferir NÃO permitir dívida, pode checar `Balance` antes de serve e recusar chamadas
com saldo abaixo de um piso mínimo.

### 6.4 Política de reembolso (DECIDIDA — uma só)

**Padrão v1: devolver ao saldo, permitindo saldo negativo; congelar uso managed se
`balance < 0` no momento da reserva.**

- `Grant`/`Refund` nunca falham por saldo negativo (reembolso de créditos já gastos
  pode deixar negativo — o ledger registra o fato).
- `Reserve` falha com `ErrInsufficientCredits` se `balance < 0` (não apenas < amount).
- Alternativa futura (documentada, não implementada): deduzir do pré-pago restante.

### 6.5 Reservas órfãs / timeout

- `ReservationTimeout` (default 5min). `Reconcile`:
  - `UPDATE credit_reservations SET status='expired', released_amount=amount, updated_at=?
     WHERE status='reserved' AND updated_at < now - timeout` + devolve ao saldo
     (transaction type='reservation_release').
  - Compara `SUM(amount) da ledger` vs `balance` por user; reporta mismatch (não
     auto-corrige; loga).

### 6.6 Grant mensal preguiçoso (sem scheduler)

```
EnsureMonthlyGrant(userID, plan):
  period = cfg.Now().UTC().Format("2006-01")        // "2026-08"
  amount = cfg.MonthlyCredits ou PlanMonthlyCredits[plan]  (0 → no-op)
  key    = "monthly:" + userID + ":" + period
  Grant(userID, amount, "monthly", "monthly included credits", key)   // idempotente
```
Chamado pelo app ANTES da primeira leitura de saldo do período (middleware/gateway).
`Grant` com chave duplicada → `ErrDuplicateGrant` tratado como sucesso (no-op) pelo
chamador de `EnsureMonthlyGrant`.

### 6.6.1 Gate de entitlement (subscription, v0.2.0)

`EnsureMonthlyGrant` consulta `subscriptions` antes de conceder: se existir um
registro (SetSubscription) e o status != `active` (cancelled/paused), o grant é
recusado (billing do plano parado; saldo existente permanece utilizável). Sem
registro (apps pré-pago, BYOK-first), o grant é incondicional — retrocompatível.

```
SetSubscription(userID, plan, active|cancelled|paused)
CancelSubscription(userID)   // shorthand → SetSubscription(..., cancelled)
Subscription(userID)         // → Subscription | ErrSubscriptionNotFound
```

### 6.7 Consumo: pool único

v1 usa um único saldo; a ordem "monthly → promo → prepaid" (§21 do plano original) é
**adiada** — exige buckets e fica em §14. O ledger preserva o tipo de cada transação,
então a separação futura é migração de leitura, não de dados.

---

## 7. BYOK (em processo)

### 7.1 CredentialStore

- Chave: `CREDITS_ENC_KEY` = hex de 32 bytes (`openssl rand -hex 32`), lida pelo APP e
  passada a `NewCredentialStore`. Sem chave → store desabilitado (Put/Get retornam erro
  claro). Nunca logar a chave.
- Criptografia: XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305`),
  nonce aleatório de 24 bytes, armazenado junto ao ciphertext em `encrypted_key` (BLOB).
- API keys NUNCA são retornadas ao browser: `Get` existe para o relay (server-side);
  endpoints públicos só expõem `{provider, configured: bool}`.

### 7.2 Relay (`http.Handler`)

- **goai NÃO é usado no relay** — é proxy pass-through (`httputil.ReverseProxy`):
  o cliente traz o próprio SDK (qualquer OpenAI-compat). O goai só aparece em
  managed mode, no app (`internal/llm` do gogogo v0.9.6 / treinamento v0.7.6;
  mapper goai→lib em §3). goai apontando pro relay via `compat.WithBaseURL` é
  escolha do app, nunca da lib.
- O app monta em `/api/byok/` **atrás do middleware de auth** (o relay não autentica;
  assume identidade do request — ver 7.3).
- Request: `POST /api/byok/{provider}/{path...}` (OpenAI-compatible, ex.
  `/v1/chat/completions`), corpo JSON idêntico ao OpenAI.
- Fluxo:
  1. `provider` do path; `apiKey := creds.Get(ctx, userID, provider)` → 404 se não
     configurado.
  2. `base := bases[provider]` (mapa de env `BYOK_PROVIDERS`, ex.
     `openai:https://api.openai.com/v1,openrouter:https://openrouter.ai/api/v1`).
  3. `httputil.ReverseProxy` com `Rewrite` (Go 1.20+): path → `base + /{path...}`,
     headers `Authorization: Bearer <apiKey>` e `X-Api-Key: <apiKey>`, remove
     `X-Byok-*` internos.
  4. Copia status/body (SSE/fluxo passam naturalmente — ReverseProxy copia o corpo
     chunked; não bufferizar).
  5. **Metering (v0.2.0)**: o relay envolve a resposta com `usageRW`, que
     repassa os bytes intactos e, ao final, extrai o `usage` OpenAI-compat do
     corpo (JSON não-streaming OU o último chunk `data: {json}` antes de
     `[DONE]`) e grava linha em `llm_usage` (billing_mode=byok,
     credits_charged=0). Model lido do body (best-effort); request id de
     `X-Byok-Request-Id` inbound (`byok:<id>`, idempotente) ou novo.
- Sem rate limit no v1 (ponytail: rate limit por usuário se abuso; o app decide).

### 7.3 Identidade

- O relay lê o userID de um header interno setado pelo middleware do app
  (ex. `X-Auth-User`) — **o app deve garantir que esse header só seja aceito quando
  a sessão já foi validada** (strip de headers externos no gateway do app; é
  responsabilidade do app, documentada no README).

---

## 8. Segurança (checagem para o implementador)

- Saldo: nunca confiar em valor client-side; grants só via `Grant` com `source`/`key`.
- `idempotency_key` UNIQUE é o backstop; checar duplicada dentro da tx (não
  check-then-insert fora da tx).
- Webhooks (app): verificar assinatura; grant com `key = "stripe:" + payment_intent_id`.
- BYOK: chaves criptografadas em repouso; nunca no HTML/JS; relay atrás de auth.
- Logs: nunca logar prompts completos nem chaves.
- `govulncheck` no CI (padrão do gogogo).

---

## 9. Scaffold do repo

- `go.mod`: `module github.com/calionauta/ai-credits`, `go 1.26`, única dep
  `golang.org/x/crypto`.
- `.golangci.yml`: copiar o conjunto de linters do gogogo-fullstack-template
  (battle-tested: errcheck, govet, staticcheck, gosec, revive, gocritic, sloglint,
  gocognit, testifylint, contextcheck, errorlint, modernize, etc.) — é o gate de
  qualidade, não só CI.
- `check-sizes`: convenção 500 linhas/arquivo (do gogogo) vale pra lib; sem lefthook
  (CI é o gate). datastar-lint NÃO se aplica (lib Go pura, sem templ).
- `AGENTS.md` (20-30 linhas): comandos `go test -race ./...`, `gofumpt`,
  `golangci-lint run ./...`, convenções (English code, lowercase-kebab, sem atribuição
  de IA em commits/releases, sem dependências sem pedir).
- CI (GitHub Actions `ci.yml`): `go test -race ./...` + `golangci-lint` + `govulncheck`
  + build; release manual por tag (`git tag v0.1.0`) com GitHub Release.
- `LICENSE`: MIT.

---

## 10. Estágios de implementação (ordem + critérios de aceite)

Cada estágio termina verde em `go test -race ./...` e `golangci-lint run ./...`.

### Stage 0 — Scaffold
- go.mod, LICENSE, AGENTS.md, CI, README stub.
- ✅ `go build ./...` verde.

### Stage 1 — Schema + ledger
- `schema.sql`, `EnsureSchema`, `New`, `Grant`, `Refund`, `Balance`.
- Testes: grant → saldo; idempotência (mesma key → `ErrDuplicateGrant` no 2º, saldo
  inalterado); refund com negativo; `ErrInsufficientCredits` nunca aplicável aqui.
- ✅ Ledger imutável: deletar/editar transação não altera saldo materializado.

### Stage 2 — Pricing + usage
- `pricing.go` (JSON, defaults embutidos, `Cost`, `EstimateMax`, `Credits`),
  `usage.go` (`RecordUsage`).
- Testes tabelados: custo exato (10k in + 4k out gpt-4o-mini → 3900 microunits →
  4 créditos com ceil); modelo desconhecido → `ErrUnknownModel`; `EstimateMax` ≥ custo
  real (invariante: para todo uso válido, `EstimateMax ≥ Cost`).
- ✅ Preço do arquivo sobrescreve default; `pricing_version` gravada no uso.

### Stage 3 — Reservas
- `reserve.go`: `Reserve`/`Settle`/`Release` conforme §6.1-6.3.
- Testes:
  - reserva 500 / captura 137 / libera 363 (exemplo do plano original).
  - **concorrência**: 100 goroutines, mesmo user, saldo inicial 1000, cada uma
    `Reserve(50)` → exatamente 20 sucesso / 80 `ErrInsufficientCredits`, saldo final 0,
    sem negativo. (`go test -race`)
  - idempotência: mesmo `requestID` duas vezes → mesma reservation, um único débito.
  - `Settle` após `Release` → `ErrReservationClosed`.
- ✅ Nenhum caminho deixa saldo negativo.

### Stage 4 — Mensal + reconcile
- `monthly.go`, `reconcile.go`.
- Testes: `EnsureMonthlyGrant` 2× no mesmo período → grant único; períodos diferentes
  → grants distintos; `Reconcile` com tx deletada manualmente → mismatch reportado;
  reserva órfã (updated_at velho) → expirada e saldo devolvido.
- ✅ Injeção de clock (`cfg.Now`) usada em todos os testes de tempo.

### Stage 5 — BYOK
- `byok/credentials.go`, `byok/relay.go`.
- Testes: round-trip Put/Get com chave de 32 bytes; Get com chave errada → erro de
  decrypt; relay com `httptest.Server` fake OpenAI-compat → request chega com
  `Authorization: Bearer <key do usuário>` e corpo intacto; provider sem credencial →
  404.
- ✅ Nenhuma chave aparece em logs ou respostas.

### Stage 6 — Integração gogogo (plugin `features/credits/`)
Ver §11. ✅ `make ci-local` verde.

### Stage 7 — Integração treinamento
Ver §12. ✅ `go test ./...` verde + fluxo de chat real usa créditos.

### Stage 8 — Stripe (gogogo + treinamento) — parte do v1 (treinamento cobra usuário final)
`features/credits/stripe.go` gated por `STRIPE_SECRET_KEY`/`STRIPE_WEBHOOK_SECRET`;
checkout (`STRIPE_SECRET_KEY`) + webhook → `Grant(key="stripe:"+payment_intent_id)`.
✅ Webhook duplicado não dobra grant (idempotency_key UNIQUE).
✅ Sem `STRIPE_SECRET_KEY` → feature desligada; app roda sem billing.

---

## 11. Integração no gogogo (`github.com/calionauta/gogogo-fullstack-template`)

Princípio: **dependência, não código embutido**. A lib entra no go.mod do gogogo
(`go get github.com/calionauta/ai-credits@v0.x`).

**Relação com o PocketBase do gogogo (verificado):** o gogogo roda PocketBase v0.39.11
sobre SQLite (ncruces/go-sqlite3). `app.DB()` devolve `*dbx.DB`; `app.DB().DB()`
devolve o `*sql.DB` subjacente — é exatamente o que `credits.New` recebe. As tabelas
de crédito vivem no MESMO arquivo SQLite, lado a lado com `_pb_*`, sem conflito de
migração (lib é dona das próprias tabelas via `EnsureSchema`; PB gerencia só `_pb_*`).
Bônus: backup/restore do PB (arquivo único) cobre as tabelas de crédito automaticamente.
NÃO usar coleções/dbx do PB dentro da lib — ela é agnóstica de driver por design.

Arquivos novos (todos com SCOPE — `make check-scope` exige):

- `features/credits/credits.go`
  `// SCOPE:layer=feature,removal=plugin — AI credits + BYOK (lib ai-credits).`
  `// To remove: delete features/credits/ + router/credits.go + config fields.`
  - `New(app, cfg)` → `*credits.Service` (usa `app.DB()`), `credits.EnsureSchema` no
    boot; `NewCredentialStore` com `CREDITS_ENC_KEY`.
- `features/credits/gateway.go` — `RunManaged(ctx, svc, llm *llm.Client, userID, model,
  prompt, maxOut)`:
  1. `svc.EnsureMonthlyGrant(userID, plan)`
  2. `EstimateMax` → `Reserve`
  3. `llm.GenerateText` (internal/llm já embrulha goai)
  4. erro → `Release`; sucesso → `Cost` + `Settle` + `RecordUsage` (mapper goai→lib).
- `features/credits/routes.go` — registrar em `router.Init` (se `CREDITS_ENABLED=true`):
  - `GET /api/credits` (saldo), `GET /api/credits/transactions` (últimas 50),
    `POST /api/ai/request` (JSON `{model, prompt, max_output_tokens, billing_mode}`),
    `POST /api/ai/billing-mode`, admin: `POST /api/admin/users/{id}/credits/grant`,
    `POST /api/admin/users/{id}/credits/revoke`, `GET /api/admin/credits/reconciliation`.
  - Relay BYOK montado em `/api/byok/` atrás do middleware de auth existente.
- `config/config.go` — `CreditsEnabled`, `CreditsEncKey`, `CreditsDefaultMode`,
  `CreditsMonthlyCredits`, `CreditsPricingFile`, `ByokProviders`.
- `router/router.go` — bloco condicional; `router/credits.go` para registro (espelhar
  padrão do router).
- `features/credits/credits_test.go` — gateway com `internal/llm/fakeserver`.
- (Opcional v1) `features/credits/views.templ` — página mínima: saldo, transações,
  config BYOK. Sem ela, endpoints bastam.

Regras gogogo respeitadas: sem `fmt.Sprintf` p/ HTML (Templ), sem HTMX (Datastar),
sem `log` (slog), nada em `internal/` que não seja infra, SCOPE em todo arquivo.

---

## 12. Integração no treinamento (`github.com/calionauta/treinador-praticas-narrativas`)

Módulo local: `~/repos/treinamento-praticas-narrativas` (go.mod:
`github.com/calionauta/treinador-praticas-narrativas`).

1. `go get github.com/calionauta/ai-credits@v0.x`.
2. `features/credits/credits.go` (thin): `credits.New(app.DB(), cfg)` +
   `EnsureSchema` no boot (`cmd/web`); envs iguais às do gogogo.
3. `features/credits/gateway.go` — `Runner` sobre os helpers goai existentes
   (`features/narrative-therapy/handlers/llm_goai_helpers.go`); mapper goai v0.7.6→lib
   (mesmos campos `TotalUsage`).
4. **Ponto de integração v1: `chat_send.go`** (o chat com o cliente simulado) — envolver
   a chamada principal com `RunManaged`/BYOK. Demais call sites (supervision, evaluate,
   meetings, session, fork) ficam como estão; o `Runner` torna a migração mecânica.
5. Rotas: `GET /api/credits`, relay em `/api/byok/` atrás do auth.
6. Teste: um fluxo de chat com `FakeEmbedder`-style stub de LLM (padrão já usado lá).

Critério: `go test ./...` verde; chat real debita crédito (verificável no
`llm_usage`).

---

## 13. Estratégia de teste (resumo)

- Pricing: tabelas (preço por modelo, cache, reasoning, ceil, desconhecido).
- Ledger: idempotência, refund negativo, invariante `balance == SUM(ledger)` após
  qualquer sequência.
- Reservas: exemplo canônico 500/137/363; concorrência 100 goroutines `-race`;
  duplicata; fechada.
- BYOK: round-trip cripto; relay com servidor fake (httptest) incluindo SSE.
- Apps: gateway com fakeserver do gogogo; chat do treinamento com stub.
- Nenhum teste real de LLM (regra do gogogo).

---

## 14. Simplificações deliberadas (conhecidas; reavaliar sob demanda)

- `ponytail: pool único de créditos` — ordem de consumo mensal→promo→prepago exige
  buckets; o ledger já guarda `type`, migração de leitura futura. Adicionar quando
  houver promo/prepago com expiração.
- `ponytail: grant mensal preguiçoso` — sem scheduler; 1 linha por chamada.
  Substituir por job (goqite no gogogo) quando houver muitos usuários ociosos.
- `ponytail: relay sem rate limit` — por usuário, se houver abuso real.
- `ponytail: BYOK só OpenAI-compatible` — Anthropic/Gemini têm formato próprio;
  o mapa `providerBases` + rewrite por provider cobre quando precisar.
- `ponytail: sem autofix de mismatch de saldo no reconcile` — reporta; humano decide.
- `ponytail: sem dagnats/workflow engine` — billing v1 é operações single-step
  idempotentes (webhook→Grant, Reserve→Settle, grant mensal preguiçoso); não há fluxo
  multi-step que justifique NATS. Considerado e descartado (violaria zero-deps).
  Reavaliar só se surgir orquestração de ciclo de vida de subscription (app-level).
- Streaming: uso é registrado ao fim do stream (o `TotalUsage` chega no chunk final do
  goai); sem pré-cobrança incremental.

---

## 15. Fora de escopo (v1) / futuro

- OpenMeter/ClickHouse/Kafka (quando volume analítico justificar).
- Double-entry completo, wallets múltiplas, transferências entre usuários.
- Buckets com expiração (ver §14).
- Rate limiting por usuário (app-level).
- Provider adapters BYOK não-OpenAI.
- Relatório de analytics além de SQL simples.

---

## 16. Definition of Done (geral)

1. `go test -race ./...` e `golangci-lint run ./...` verdes na lib.
2. Invariante `balance == SUM(ledger)` testado após sequências arbitrárias.
3. Nenhuma dependência além de `golang.org/x/crypto`.
4. Integração gogogo: `make ci-local` verde; SCOPE ok; feature removível deletando
   `features/credits/` + 2 pontos de registro.
5. Integração treinamento: `go test ./...` verde; chat real consome créditos.
6. README da lib com exemplo de 20 linhas (New + Grant + Reserve/Settle + BYOK mount).
