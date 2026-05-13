# llm-agent-customer-support

Reference customer-support service built on [`github.com/costa92/llm-agent`](https://github.com/costa92/llm-agent) + [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) + [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel). Multi-agent flow: RAG knowledge lookup + StateGraph triage + native tool calling, end-to-end traces in Grafana.

> **Demo only — production deployment requires hardening.** Single-container `grafana/otel-lgtm`, no auth on `/chat`, dev secrets, hard caps tuned for local demo. The shipped `compose.yaml` brings the stack up in <60s; what it does NOT include: TLS termination, authentication, secret management, multi-tenant isolation, regional sharding.

> **v0.1.0-pre / Phase 6 day-one guardrails online.** The service now supports independent chat-provider and embedding-provider selection, a real support orchestration path built from StateGraph triage + RAG lookup + native tool calling, durable conversation state through a shared SQLite/Postgres session-store contract, config-driven hard caps with a live `DISABLE_LLM` panic switch, and layered day-one prompt-injection defenses. Compose/demo polish still lands in the final Phase 6 plan.

> **Current local-dev note:** this repo currently uses local `replace` directives during cross-repo execution so the service can build against sibling checkouts of `llm-agent`, `llm-agent-providers`, and `llm-agent-otel` before coordinated tags exist. Those `replace` lines are a temporary development escape hatch and must not ship on release branches.

> **K8s manifests are NOT part of v0.3.** See [PITFALLS Pitfall 16](https://github.com/costa92/llm-agent/blob/main/.planning/research/PITFALLS.md) for rationale. Half-shipped K8s is worse than no K8s; the v0.4 deferral note is on the roadmap. Do NOT submit Helm charts / kustomize / kind config in PRs against this repo until v0.4 milestone-planning explicitly includes them.

## Install

```bash
git clone https://github.com/costa92/llm-agent-customer-support.git
cd llm-agent-customer-support
docker compose -f compose/compose.yaml up --build
```

## Demo stack

The local demo stack now lives at [compose/compose.yaml](./compose/compose.yaml). It starts:

- the customer-support app on `http://localhost:8080`
- Ollama for local chat + embeddings
- an OpenTelemetry Collector with tail-sampling
- Grafana on `http://localhost:3000`

First boot is slower because `ollama-init` pulls both `llama3.1:8b` and `nomic-embed-text` before the app starts. On a cold machine that model download dominates startup time; after the volume is warm, subsequent boots are much faster.

Start the stack:

```bash
docker compose -f compose/compose.yaml up --build
```

Verify the service surface:

```bash
curl http://localhost:8080/readyz
curl -X POST http://localhost:8080/chat -H 'Content-Type: application/json' -d '{"message":"hello"}'
```

Then open Grafana at `http://localhost:3000` and confirm the pre-provisioned `Customer Support Observability` dashboard is present. The shipped dashboard includes panels for `Request Latency`, `Token Usage`, `Estimated Cost`, `Error Rate`, and `Tool Success Ratio`.

Tail-sampling policy is configured in [compose/otel-collector.yaml](./compose/otel-collector.yaml):

- keep 100% of error traces
- keep 100% of traces slower than 5 seconds
- keep 10% baseline traffic otherwise

Observability caveat: this is a demo stack, not a billing or SLO source of truth. `Token Usage`, `Estimated Cost`, and `Tool Success Ratio` are span-derived demo views intended to make traces explorable quickly on a fresh local stack; production reporting would need dedicated metrics, authenticated backends, and provider-billed token reconciliation.

## Current bootstrap surface

- `cmd/server/main.go` loads config, installs signal handling, builds the app, and runs until SIGINT/SIGTERM.
- `internal/config` owns env parsing and provider-aware defaults for `openai`, `anthropic`, and `ollama`.
- `internal/app` owns model construction, session-store bootstrap, embedding bootstrap, seeded knowledge-base setup, wrapped agent construction, transport mux wiring, and `http.Server` startup/shutdown.
- `internal/providers` owns the split chat/embedding factory seam so provider selection stays centralized and truthful.
- `internal/httpapi` owns the first transport surface: JSON chat, SSE chat streaming, session ID propagation, hard-cap rejection paths, health probes, readiness checks, and `X-Trace-Id` response headers.
- `internal/limits` owns config-driven runtime guardrails: rate limits, request token caps, retry/tool-loop checks, daily token budget, and the live panic switch.
- `internal/guardrails` owns prompt-injection defenses: suspicious-input filtering, safe fallback policy, and untrusted retrieved-content marking.
- `internal/supportflow` owns typed support triage, refund-policy lookup, human handoff routing, and transcript persistence.
- `internal/sessionstore` owns the durable session contract plus SQLite and Postgres-backed implementations.

## Architecture (Phase 6 preview)

- HTTP API: `POST /chat`, `POST /chat/stream` (SSE), `GET /healthz`, `GET /readyz`, `X-Trace-Id` response header.
- Provider switch: `LLM_PROVIDER=openai|anthropic|ollama` + `EMBEDDING_PROVIDER=openai|ollama`. Chat and embeddings are now selected independently, so `LLM_PROVIDER=anthropic` can run with `EMBEDDING_PROVIDER=openai|ollama`.
- Session backend: `SESSION_BACKEND=sqlite|postgres` with `SESSION_DSN` selecting the concrete database. Requests accept `session_id`; if absent the service creates one and returns it in `X-Session-Id`.
- Support flow: chargeback/fraud routes to human handoff, missing order IDs request clarification, refund/order questions use a tool-backed RAG lookup path.
- Hard caps from Day 1 (K7): `MAX_TOKENS_PER_REQUEST`, `MAX_TOOL_CALLS_PER_AGENT_LOOP`, `MAX_REQUESTS_PER_IP_PER_MINUTE`, `RETRY_MAX_ATTEMPTS`, `DAILY_TOKEN_BUDGET` + `DISABLE_LLM=1` panic switch. Preflight cap failures return `429`; the panic switch returns `503` without restart.
- Prompt-injection guardrails Day 1: suspicious-input filter with safe fallback, tool allowlist with server-side `user_id` enforcement, and retrieved RAG content marked untrusted in the system prompt path.
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

This repo is already code-compatible with the `llm-agent v0.4` core surface.
Its local release-prep state now targets `github.com/costa92/llm-agent v0.4.0`.
The only remaining Phase 7 follow-up is publishing the final coordinated tags.

## PR automation

This repo now expects `.github/workflows/pr-governance.yml` to enforce a simple policy:

- PRs authored by `costa92` should pass governance automatically and enable auto-merge after required checks pass.
- PRs authored by anyone else should request review from `costa92` and stay blocked until `costa92` approves the current PR head.

This policy is designed to work with branch protection that requires the `go` and `governance` status checks, instead of GitHub's built-in required-approval gate.

The full multi-repo governance design, including the relationship between
`llm-agent`, `llm-agent-providers`, `llm-agent-otel`, and
`llm-agent-customer-support`, lives in the core repo docs:

- [`PR-GOVERNANCE-OVERVIEW.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-OVERVIEW.md)
- [`PR-GOVERNANCE-PROJECTS.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-PROJECTS.md)
- [`PR-GOVERNANCE-RULES.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-RULES.md)
- [`PR-GOVERNANCE-OPERATIONS.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-OPERATIONS.md)

## See also

- [`llm-agent` CLAUDE.md](https://github.com/costa92/llm-agent/blob/main/CLAUDE.md) — project hard rules (stdlib-only core, no K8s, capability per-(provider x model)).
- [`llm-agent` ROADMAP](https://github.com/costa92/llm-agent/blob/main/.planning/ROADMAP.md) — 8-phase v0.3 milestone plan.
- [`DEPRECATIONS.md`](https://github.com/costa92/llm-agent/blob/main/DEPRECATIONS.md) — symbols on the v0.4 removal track.
- [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) — provider adapters this service composes.
- [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel) — OTel decorator wrappers used by this service.

## License

MIT — see [LICENSE](LICENSE).
