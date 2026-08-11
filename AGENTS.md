# AGENTS.md

Guidance for coding agents operating in `beancount-autoupdate/go`.

## 1) Project Snapshot

- Language: Go (`go 1.24.0`, toolchain `go1.24.2`).
- Entry point: `cmd/main.go`.
- Core packages live in `internal/` (`beancount`, `config`, `git`, `httpingest`, `llm`, `logger`, `telegram`, `webdav`, `embed`).
- Build orchestration is Makefile-first; direct `go` commands also work.
- Logging is centralized through `internal/logger` (logrus wrapper).
- Config is TOML + env override (`config.toml`, `.env`, env vars).

## 2) Build, Lint, Test Commands

Use these from repository root (`/Users/rain/Desktop/code/beancount-autoupdate/go`).

### Daily commands

- Install/sync deps: `make deps`
- Run app: `make run`
- Build binary: `make build`
- Build all platforms: `make build-all`
- Clean artifacts: `make clean`

### Formatting and lint

- Format code: `make fmt` (runs `go fmt ./...`).
- Lint code: `make lint` (runs `golangci-lint run ./...` when installed).
- Recommended local quality gate: `make fmt && make lint && make test`.

### Testing

- Run all tests: `make test` or `go test -v ./...`.
- Coverage profile: `make test-coverage`.
- Run one package: `go test -v ./internal/telegram`.
- Run one test by name: `go test -v ./internal/telegram -run '^TestHandlePendingCommand$'`.
- Run subtests by regex: `go test -v ./internal/telegram -run 'TestBot/CancelFlow'`.
- Run tests repeatedly: `go test -count=1 ./...`.

### Current repository reality

- There are baseline unit tests under `internal/beancount`, `internal/config`, and `internal/llm`.
- Add new tests package-locally under corresponding `internal/<pkg>/` directories.
- HTTP upload ingest is available under `internal/httpingest` and is wired from `cmd/main.go` when `[http_server].enabled = true`.

### CI checks

- GitHub Actions CI runs on pull requests and `main` pushes.
- CI currently validates formatting (`gofmt`) and runs `go test -v ./...`.

### Additional dev commands in Makefile

- Docker build/run helpers: `make docker-build`, `make docker-run`, `make docker-stop`.

## 3) Agent Workflow Expectations

- Prefer minimal, surgical changes over broad refactors.
- Preserve package boundaries under `internal/`.
- Avoid introducing new dependencies unless clearly justified.
- Keep behavior stable unless task explicitly asks for behavior change.
- After code edits, run at least: `make fmt` and `make test`.
- If lint tooling exists locally, also run: `make lint`.

## 4) Code Style and Conventions

Follow existing patterns in this repo first; these rules codify observed conventions.

### 4.1 Imports

- Use standard Go import grouping/order (managed by `gofmt`):
  1. standard library
  2. module internal imports (`beancount-autoupdate/internal/...`)
  3. third-party imports
- Keep imports explicit; do not use dot imports.
- Use import aliases only when necessary (e.g., `tgbotapi`, `gossh`).

### 4.2 Formatting

- Always run `gofmt` (`make fmt`) after edits.
- Keep lines readable; do not optimize for ultra-compact one-liners.
- Use tabs/formatting produced by Go tooling, not manual alignment.
- Keep comments concise and meaningful; avoid restating obvious code.

### 4.3 Naming

- Exported identifiers: PascalCase with Go idioms (`NewManager`, `LoadConfig`).
- Unexported identifiers: camelCase (`initRepo`, `buildPrompt`).
- Receiver names are short and stable (`m *Manager`, `b *Bot`, `p *Parser`).
- Prefer descriptive names over abbreviations except conventional short names (`cfg`, `err`, `msg`).

### 4.4 Types and structs

- Keep config/data structs strongly typed with explicit field tags where needed (`toml:"..."`, `xml:"..."`).
- Use dedicated domain structs for payloads rather than `map[string]interface{}` when practical.
- Prefer zero-value-friendly struct design where possible.
- Add methods on domain managers (`Manager`, `Bot`, `Parser`) instead of free functions when behavior is stateful.

### 4.5 Error handling

- Return errors to caller unless process must terminate.
- Wrap errors with context using `%w` (repo already follows this heavily).
- Log at boundary layers (CLI entrypoint, network/IO edges), not at every stack frame.
- Use `logger.Fatalf` only for truly unrecoverable startup/runtime fatal states.
- In deferred cleanup, log close/remove failures without masking primary error.

### 4.6 Logging

- Use `internal/logger` helpers (`Infof`, `Warnf`, `Errorf`, etc.), not raw `fmt.Println` for operational logs.
- Keep log messages actionable and include key identifiers (userID, transactionID, path, status).
- Avoid logging secrets (API keys, passwords, full tokens).
- Debug-heavy logs are acceptable behind debug log level.

### 4.7 Concurrency and state

- Shared mutable maps/slices in long-lived services must be mutex-protected.
- Use `sync.RWMutex` when read-heavy access pattern is clear.
- Keep lock scope tight; avoid long network/IO while holding locks.
- Channels should have explicit buffering rationale (e.g., worker queues, semaphores).

### 4.8 File and path handling

- Use `filepath` for local filesystem paths.
- Use `path` for URL-style WebDAV remote paths.
- Ensure directories exist before file write (`os.MkdirAll`).
- Use explicit file modes already used in project (`0o755` dirs, `0o644` files).

### 4.9 Configuration and secrets

- Maintain current precedence: config file first, env overrides second.
- Keep secret values in env (`TELEGRAM_BOT_TOKEN`, `LLM_API_KEY`, etc.), never hardcode.
- Validate critical config early (`cfg.Validate()` style).

### 4.10 API/IO boundaries

- For external APIs (Telegram, OpenAI, WebDAV, Git remotes), keep timeouts explicit.
- Handle graceful fallbacks where established (e.g., Structured Output -> JSON mode).
- Preserve compatibility behavior unless task explicitly removes it.

### 4.11 Modern Go syntax

- Prefer `any` over `interface{}`.
- Use `math/rand/v2` (`rand.IntN`, etc.), not `math/rand`.
- Prefer `os.ReadFile`/`os.WriteFile` over deprecated `ioutil` APIs.

## 5) Testing Guidance for New Code

- Prefer table-driven tests for parsing/formatting logic.
- For concurrent behavior, add deterministic tests around state transitions.
- For network integrations, isolate pure logic and mock transport/client edges.
- Add regression tests for bug fixes before or with the fix.

## 6) Cursor/Copilot Rule Files

Checked in this repository:

- `.cursor/rules/`: not present
- `.cursorrules`: not present
- `.github/copilot-instructions.md`: not present

Therefore, no additional Cursor/Copilot instruction files apply right now.
If any of these files are added later, treat them as higher-priority repository guidance and update this file.
