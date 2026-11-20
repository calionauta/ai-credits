# Changelog

Todas as mudanças notáveis em `ai-credits` são documentadas neste arquivo.

O formato segue [Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/),
e o projeto adere ao [Versionamento Semântico](https://semver.org/lang/pt-BR/).

## [Unreleased]

## [v0.7.0] - 2026-11-20

### Added
- **`stripe.Config.BackendURL`.** Sobrescreve a base URL que o stripe-go usa no
  `CreateCheckout`. Vazio = `api.stripe.com`, como antes. Com ele o caminho de
  saída do adapter passa a ser testável offline: antes, `CreateCheckout` criava
  a linha de purchase, montava a sessão e só então batia na Stripe de verdade,
  então a única forma de exercitá-lo era uma conta test-mode — e por isso ele
  era o único caminho exportado do pacote sem teste (a suíte só cobre webhook
  de entrada). `stripe/stripe_checkout_test.go` agora aponta o adapter para um
  `httptest.Server`, prova o path `/v1/checkout/sessions`, a
  `Idempotency-Key` derivada do purchase e o line item precificado pelo catálogo
  local. A resolução é por chamada (`backend()`), não um `stripe.SetBackend`
  global: estado de pacote em teste vaza para todo chamador do processo.

### Docs
- **`docs/testing.md`** separa as duas metades do adapter (webhooks sintéticos,
  checkout sintético) e diz que a suíte E2E não é necessária para cobrir
  checkout. **`docs/payments.md`** documenta o campo ao lado do exemplo de
  configuração.

## [v0.6.1] - 2026-11-18

### Fixed
- **Uma cache de native library truncada se cura sozinha.** Um processo morto no
  meio da extração (SIGKILL, OOM, reboot) deixava uma library pela metade que
  todo run posterior rejeitava com `cached library file hash sum mismatch …
  expected=<embedded>, got=<garbage>`, e o único remédio era um humano apagando
  `~/Library/Caches/turso-go`. A warm-up agora reconhece o mismatch, descarta a
  cache e carrega de novo. A retentativa é limitada — um erro reparável, um
  rebuild — então uma library genuinamente ruim reporta em vez de girar, e um
  erro de load não relacionado nunca apaga uma cache que funciona.
- **O lock morava dentro do diretório que ele purga.** Purgar a cache segurando
  o lock unlinks um arquivo travado; o próximo processo cria um arquivo novo no
  mesmo path e pega um lock que ninguém mais segura — dois extractors, exatamente
  a corrida que esse pacote existe para parar. Ele passa a viver ao lado da
  árvore, como o `internal/dbload` do gogogo já fazia.
- **`purge` apagava só os arquivos do topo.** O loader extrai para um
  subdiretório com nome de hash (`turso-go/<hash8>/lib…`), então aquele formato
  não reparava nada enquanto reportava sucesso. Agora remove a árvore.
- **CI: a extração é serializada, e a suíte deixou de mentir sobre o motivo.**
  Três runs seguidos morreram com o mismatch acima, reportado a partir de
  `TestCredentialStoreRoundTrip` — que lê como teste de credencial flaky e não
  é. O `tursogo` extrai a native library de uma cópia embutida para uma cache
  compartilhada entre processos, e a extração não é atômica: `os.Create`
  (trunca in place), copia, e só então compara o hash que acabou de escrever.
  Dois inicializadores que se sobrepõem veem um arquivo que existe e não está
  vazio, ambos o leem no meio da cópia, e ambos falham — o `got` é um snapshot
  de dois escritores intercalados, o que explica a diferença a cada run.
- **Windows voltou a ter `TestMain`.** A warm-up nasceu com `//go:build
  !windows` no arquivo compartilhado, então pacotes Windows compilavam com
  nenhum `TestMain` — silenciosamente, porque a ausência de um `TestMain` não é
  erro. O módulo embute `libs/windows_amd64`, então a corrida existe lá também.
  Passa a ser um arquivo posix (`flock`) e um windows (`LockFileEx`: não há
  `flock` no Windows), com as constantes de modo compartilhadas num terceiro; os
  arquivos de teste ficam sem tag para que as duas plataformas ganhem um.

## [v0.6.0] - 2026-11-18

### Added
- **`MarkOutboxSettled(ctx, requestID)`.** Fecha a linha de `settlement_outbox`
  de uma cobrança que foi liquidada por outro caminho. Antes desta função não
  havia como fechar a linha que `EnqueueSettlement` escreve: quem liquidava
  direto (`Settle` + `RecordUsage`, o caminho do biller quando a usage real já
  está em mãos) deixava a linha em `pending`, o drainer a selecionava,
  re-liquidava uma reserva já fechada, recebia erro e gravava
  `status='failed'` numa cobrança que tinha sido capturada corretamente. Cada
  request bem-sucedido deixava uma falha fantasma permanente, re-selecionada a
  cada tick. A atualização é condicional a `status='pending'`, então um fecho
  atrasado não sobrescreve o veredito do drainer.

### Fixed
- **`ProcessSettlementOutbox` liquidava? Não.** O drainer selecionava os
  request_ids pendentes, re-selecionava as mesmas linhas e expirava o que
  tivesse mais de uma hora — nunca chamava `SettleViaOutbox` nem lia usage. O
  loop era um no-op que deixava a cobrança sem captura até a expiração
  devolver a reserva: um mecanismo de recuperação que não recuperava nada, o que
  é pior do que não ter nenhum, porque há chamador confiando nele. Agora a
  usage registrada é a evidência de que uma cobrança é devida: com ela,
  liquida (via `settleOnce`, para dois drainers não cobrarem duas vezes); sem
  ela, a linha fica `pending`.
- **Clock injetado no drainer.** Todos os carimbos vêm de `s.cfg.Now()`. Antes,
  a janela de graça misturava `time.Now()` com o clock injetado, e um teste com
  o clock fixo em 2026 via toda linha nova como expirada.
- **`payments`: o teste de rollback atômico não compilava.** Chamava
  `sql.Open("sqlite", …mode=memory…)` sem importar `sql`; o pacote construía e
  os testes não. Passa a abrir por `credits.OpenSQLite("turso", …)`, que é o
  driver do módulo.

### Changed
- **Driver: tursogo, não modernc.** `modernc.org/sqlite` sai do `go.mod`; o
  lib depende de `turso.tech/database/tursogo`. `OpenSQLite` aceita
  `"turso"` como driverName (pragmas via post-open, pois o tursogo só
  entende `_busy_timeout` no DSN); `"sqlite3"` (ncruces) continua igual.
  Testes usam `testFileDB` (arquivo isolado por teste) em vez do
  `file:…?mode=memory&cache=shared` que o tursogo lia como nome literal
  de arquivo.
- **Write-lock à prova de driver.** `immediateTx` força o upgrade do
  write lock com um `UPDATE` de zero linhas antes de qualquer leitura de
  negócio: no tursogo (tx sempre DEFERRED) isso troca o `snapshot is
  stale` no commit por `BUSY` retryável no upgrade; nos drivers que já
  abrem immediate é no-op. `TestReserveConcurrency` (100 reservas
  concorrentes) passa sem erro inesperado.

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