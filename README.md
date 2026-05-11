# llm-agent-customer-support

Reference customer-support service built on [`github.com/costa92/llm-agent`](https://github.com/costa92/llm-agent) + [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) + [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel). Multi-agent flow: RAG knowledge lookup + StateGraph triage + native tool calling, end-to-end traces in Grafana.

> **Demo only — production deployment requires hardening.** Single-container `grafana/otel-lgtm`, no auth on `/chat`, dev secrets, hard caps tuned for local demo. The shipped `compose.yaml` brings the stack up in <60s; what it does NOT include: TLS termination, authentication, secret management, multi-tenant isolation, regional sharding.

> **v0.1.0-pre / Phase 6 durable sessions online.** The service now supports independent chat-provider and embedding-provider selection, a real support orchestration path built from StateGraph triage + RAG lookup + native tool calling, and durable conversation state through a shared SQLite/Postgres session-store contract. Hard caps, prompt-injection guardrails, and compose/demo assets still land in later Phase 6 plans.

> **Current local-dev note:** this repo currently uses local `replace` directives during cross-repo execution so the service can build against sibling checkouts of `llm-agent`, `llm-agent-providers`, and `llm-agent-otel` before coordinated tags exist. Those `replace` lines are a temporary development escape hatch and must not ship on release branches.

> **K8s manifests are NOT part of v0.3.** See [PITFALLS Pitfall 16](https://github.com/costa92/llm-agent/blob/main/.planning/research/PITFALLS.md) for rationale. Half-shipped K8s is worse than no K8s; the v0.4 deferral note is on the roadmap. Do NOT submit Helm charts / kustomize / kind config in PRs against this repo until v0.4 milestone-planning explicitly includes them.

## Install

```bash
git clone https://github.com/costa92/llm-agent-customer-support.git
cd llm-agent-customer-support
docker compose up    # available after Phase 6
```

## Current bootstrap surface

- `cmd/server/main.go` loads config, installs signal handling, builds the app, and runs until SIGINT/SIGTERM.
- `internal/config` owns env parsing and provider-aware defaults for `openai`, `anthropic`, and `ollama`.
- `internal/app` owns model construction, session-store bootstrap, embedding bootstrap, seeded knowledge-base setup, wrapped agent construction, transport mux wiring, and `http.Server` startup/shutdown.
- `internal/providers` owns the split chat/embedding factory seam so provider selection stays centralized and truthful.
- `internal/httpapi` owns the first transport surface: JSON chat, SSE chat streaming, session ID propagation, health probes, readiness checks, and `X-Trace-Id` response headers.
- `internal/supportflow` owns typed support triage, refund-policy lookup, human handoff routing, and transcript persistence.
- `internal/sessionstore` owns the durable session contract plus SQLite and Postgres-backed implementations.

## Architecture (Phase 6 preview)

- HTTP API: `POST /chat`, `POST /chat/stream` (SSE), `GET /healthz`, `GET /readyz`, `X-Trace-Id` response header.
- Provider switch: `LLM_PROVIDER=openai|anthropic|ollama` + `EMBEDDING_PROVIDER=openai|ollama`. Chat and embeddings are now selected independently, so `LLM_PROVIDER=anthropic` can run with `EMBEDDING_PROVIDER=openai|ollama`.
- Session backend: `SESSION_BACKEND=sqlite|postgres` with `SESSION_DSN` selecting the concrete database. Requests accept `session_id`; if absent the service creates one and returns it in `X-Session-Id`.
- Support flow: chargeback/fraud routes to human handoff, missing order IDs request clarification, refund/order questions use a tool-backed RAG lookup path.
- Hard caps from Day 1 (K7): `MAX_TOKENS_PER_REQUEST`, `MAX_TOOL_CALLS_PER_AGENT_LOOP`, `MAX_REQUESTS_PER_IP_PER_MINUTE`, `RETRY_MAX_ATTEMPTS`, `DAILY_TOKEN_BUDGET` + `DISABLE_LLM=1` panic switch.
- Prompt-injection guardrails Day 1: input filter, tool allowlist with server-side `user_id` enforcement, retrieved RAG content marked untrusted in system prompt.
- OTel collector tail-sampling: 100% errors, 100% latency >5s, 10% otherwise.

## Cross-repo iteration pattern (INFRA-06)

This repo lives in a 4-repo umbrella alongside [`llm-agent`](https://github.com/costa92/llm-agent), [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers), and [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel). For local development across repos:

**Recommended:** clone all 4 repos as siblings, run `./scripts/workspace.sh` from any of them, then develop with a `go.work` file. The workspace file is `.gitignore`d in every repo:

```bash
cd <parent>
git clone https://github.com/costa92/llm-agent.git
git clone https://github.com/costa92/llm-agent-providers.git
git clone https://github.com/costa92/llm-agent-otel.git
git clone https://github.com/costa92/llm-agent-customer-support.git
cd llm-agent-customer-support
./scripts/workspace.sh    # writes ../go.work pointing at all 4 sibling clones
go build ./...            # now resolves llm-agent against the local sibling
```

**Escape hatch (NEVER on tagged-release branches):** for one-off iteration without `go.work`, you can use `replace`:

```bash
go mod edit -replace=github.com/costa92/llm-agent=../llm-agent
```

The `release-precheck` CI workflow rejects any non-empty `replace` block on branches matching `release/**`. Don't tag from a branch with `replace` directives — INFRA-04.

## Versioning

This repo tracks `v0.1.x` for the `llm-agent v0.3.x` cycle. Sister-repo bumps coordinate with core breaking changes; coordinated tags (Phase 7) advance both repos in lockstep.

## See also

- [`llm-agent` CLAUDE.md](https://github.com/costa92/llm-agent/blob/main/CLAUDE.md) — project hard rules (stdlib-only core, no K8s, capability per-(provider x model)).
- [`llm-agent` ROADMAP](https://github.com/costa92/llm-agent/blob/main/.planning/ROADMAP.md) — 8-phase v0.3 milestone plan.
- [`DEPRECATIONS.md`](https://github.com/costa92/llm-agent/blob/main/DEPRECATIONS.md) — symbols on the v0.4 removal track.
- [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) — provider adapters this service composes.
- [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel) — OTel decorator wrappers used by this service.

## License

MIT — see [LICENSE](LICENSE).
