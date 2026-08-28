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

## [Unreleased]

### Added
- `OpenSQLite(driverName, path)` — abre uma conexão SQLite com os pragmas
  que um ledger de billing precisa em uma segunda conexão ao DB de um app
  (WAL + busy_timeout + foreign keys + NORMAL). Elimina a duplicação de
  "abrir a segunda conexão do ledger" que existia em gogogo e ensaiter.
- `NewRequestID()` — ID aleatório (crypto/rand, 32 hex) para chaves de
  reserva/uso/ledger, sem depender de `google/uuid` nos apps.
- `EstimateTokens(string)` — estimativa grosseira de tokens de entrada
  (4 chars/token) só para dimensionar a reserva conservadora.

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

[Unreleased]: https://github.com/calionauta/ai-credits/compare/v0.1.1...HEAD
[v0.1.1]: https://github.com/calionauta/ai-credits/releases/tag/v0.1.1
[v0.1.0]: https://github.com/calionauta/ai-credits/releases/tag/v0.1.0