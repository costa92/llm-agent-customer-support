[English](./README.md) | [简体中文](./README.zh-CN.md)

# llm-agent-customer-support

基于 [`github.com/costa92/llm-agent`](https://github.com/costa92/llm-agent) + [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) + [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel)构建的参考客服服务。多智能体流程：RAG 知识查询 + StateGraph 分诊 + 原生工具调用，并在 Grafana 中提供端到端链路。

> **仅供演示——生产部署需要加固。** 单容器 `grafana/otel-lgtm`、`/chat` 无鉴权、开发密钥、为本地演示调优的硬上限。随仓库交付的 `compose.yaml` 可在 60 秒内拉起整个栈；它不包含：TLS 终结、认证、密钥管理、多租户隔离、区域分片。

> **当前代码快照。** 该服务支持独立选择 chat-provider 与 embedding-provider，一条由 StateGraph 分诊 + RAG 查询 + 原生工具调用构成的客服编排路径，通过共享的 SQLite/Postgres session-store 契约实现的持久会话状态，配置驱动的硬上限并带有实时的 `DISABLE_LLM` panic 开关，以及分层的提示词注入防御。

> **本地开发说明。** `go.mod` 目前不包含任何 `replace` 指令。对于跨仓迭代，你仍可通过 `./scripts/workspace.sh` 使用兄弟仓的 `go.work`，或临时添加本地 `replace` 行作为一次性逃生舱。`release-precheck` 会拒绝 `release/**` 上的非空 `replace` 块。

> **K8s 清单不属于 v0.3。** 理由参见 [PITFALLS Pitfall 16](https://github.com/costa92/llm-agent/blob/main/.planning/research/PITFALLS.md)。半成品的 K8s 比没有 K8s 更糟；v0.4 的延期说明已记录在路线图上。在 v0.4 里程碑规划明确纳入之前，请勿在针对本仓库的 PR 中提交 Helm charts / kustomize / kind config。

## Install

```bash
git clone https://github.com/costa92/llm-agent-customer-support.git
cd llm-agent-customer-support
docker compose -f compose/compose.yaml up --build
```

## Demo stack

本地演示栈现位于 [compose/compose.yaml](./compose/compose.yaml)。它启动：

- 客服应用，监听 `http://localhost:8080`
- Ollama，用于本地 chat + embeddings
- 一个带 tail-sampling 的 OpenTelemetry Collector
- Grafana，监听 `http://localhost:3000`

首次启动较慢，因为 `ollama-init` 会在应用启动前同时拉取 `llama3.1:8b` 和 `nomic-embed-text`。在冷启动机器上，模型下载会主导启动时间；待卷预热后，后续启动会快得多。

启动该栈：

```bash
docker compose -f compose/compose.yaml up --build
```

验证服务接口：

```bash
curl http://localhost:8080/readyz
curl -X POST http://localhost:8080/chat -H 'Content-Type: application/json' -d '{"message":"hello"}'
```

然后在 `http://localhost:3000` 打开 Grafana，确认预置的 `Customer Support Observability` 仪表盘已存在。随仓库交付的仪表盘包含 `Request Latency`、`Token Usage`、`Estimated Cost`、`Error Rate` 和 `Tool Success Ratio` 等面板。

Tail-sampling 策略配置于 [compose/otel-collector.yaml](./compose/otel-collector.yaml)：

- 保留 100% 的错误链路
- 保留 100% 慢于 5 秒的链路
- 其余情况下保留 10% 的基线流量

可观测性提示：这是一个演示栈，并非计费或 SLO 的可信源。`Token Usage`、`Estimated Cost` 和 `Tool Success Ratio` 是从 span 派生的演示视图，目的是让链路能在全新的本地栈上快速可探索；生产报表需要专用指标、带认证的后端，以及与提供方计费 token 的对账。

## Current bootstrap surface

- `cmd/server/main.go` 加载配置、安装信号处理、构建应用，并持续运行直到收到 SIGINT/SIGTERM。
- `internal/config` 负责环境变量解析以及面向 `openai`、`anthropic` 和 `ollama` 的提供方感知默认值。
- `internal/app` 负责模型构建、session-store 引导、embedding 引导、预置知识库的搭建、包装后的智能体构建、传输 mux 接线，以及 `http.Server` 的启动/关闭。
- `internal/providers` 负责拆分的 chat/embedding 工厂接缝，使提供方选择保持集中且真实。
- `internal/httpapi` 负责首个传输接口：JSON chat、SSE chat 流式、session ID 传播、硬上限拒绝路径、健康探针、就绪检查，以及 `X-Trace-Id` 响应头。
- `internal/limits` 负责配置驱动的运行时护栏：速率限制、请求 token 上限、retry/tool-loop 检查、每日 token 预算，以及实时 panic 开关。
- `internal/guardrails` 负责提示词注入防御：可疑输入过滤、安全回退策略，以及不可信检索内容的标记。
- `internal/supportflow` 负责类型化的客服分诊、退款策略查询、人工交接路由，以及对话记录持久化。
- `internal/sessionstore` 负责持久会话契约，以及基于 SQLite 和 Postgres 的实现。

## Architecture (Phase 6 preview)

- HTTP API：`POST /chat`、`POST /chat/stream`（SSE）、`GET /healthz`、`GET /readyz`、`X-Trace-Id` 响应头。`/readyz` 会主动探测 session store（`PingContext`），并且——当 `READINESS_PROBE_EMBEDDER=true`（默认值）时——发起一次 1 秒的 `embedder.Embed("ok")` 调用；失败时返回 `503`，并在响应体中带上上游错误，使 K8s/compose 的就绪状态反映真实的服务状态。
- 提供方切换：`LLM_PROVIDER=openai|anthropic|ollama` + `EMBEDDING_PROVIDER=openai|ollama`。chat 与 embeddings 现已可独立选择，因此 `LLM_PROVIDER=anthropic` 可以与 `EMBEDDING_PROVIDER=openai|ollama` 一起运行。
- 会话后端：`SESSION_BACKEND=sqlite|postgres`，由 `SESSION_DSN` 选择具体数据库。请求可携带 `session_id`；若缺失，服务会创建一个并在 `X-Session-Id` 中返回。
- 客服流程：chargeback/fraud 路由至人工交接，缺失订单 ID 时请求澄清，退款/订单类问题使用基于工具的 RAG 查询路径。
- 从第一天起的硬上限（K7）：`MAX_TOKENS_PER_REQUEST`、`MAX_TOOL_CALLS_PER_AGENT_LOOP`、`MAX_REQUESTS_PER_IP_PER_MINUTE`、`RETRY_MAX_ATTEMPTS`、`DAILY_TOKEN_BUDGET` + `DISABLE_LLM=1` panic 开关。预检上限失败返回 `429`；panic 开关无需重启即返回 `503`。
- 从第一天起的提示词注入护栏：带安全回退的可疑输入过滤、带服务端 `user_id` 强制的工具白名单，以及在系统提示词路径中将检索到的 RAG 内容标记为不可信。
- OTel collector 的 tail-sampling：100% 错误、100% 延迟 >5s、其余 10%。

## Cross-repo iteration pattern (INFRA-06)

本仓库是更大的 `llm-agent-ecosystem` 的一部分。本仓库中的本地辅助脚本面向一个常见的 4 仓开发子集，与 [`llm-agent`](https://github.com/costa92/llm-agent)、[`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) 和 [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel) 一起使用。在该子集上进行本地开发：

**该子集推荐方式：** 将所有 4 个仓库作为兄弟仓克隆，从其中任意一个运行 `./scripts/workspace.sh`，然后使用 `go.work` 文件开发。该工作区文件在每个仓库中都被 `.gitignore` 忽略：

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

**逃生舱（绝不在打标签的发布分支上使用）：** 对于不依赖 `go.work` 的一次性迭代，你可以使用 `replace`：

```bash
go mod edit -replace=github.com/costa92/llm-agent=../llm-agent
```

`release-precheck` CI 工作流会拒绝任何匹配 `release/**` 分支上的非空 `replace` 块。不要从带有 `replace` 指令的分支打标签——INFRA-04。

## Versioning

本仓库通过协调的版本提升波次跟踪更大的生态。当前确切的兄弟仓锚定请查看
`go.mod`；在当前代码快照中，它依赖
`github.com/costa92/llm-agent v0.5.1`、
`github.com/costa92/llm-agent-otel v0.2.2`、
`github.com/costa92/llm-agent-providers v0.2.1`、
`github.com/costa92/llm-agent-flow v0.1.1` 和
`github.com/costa92/llm-agent-rag v1.9.0`。

## PR automation

本仓库现在期望 `.github/workflows/pr-governance.yml` 执行一条简单的策略：

- 由 `costa92` 创建的 PR 应自动通过治理，并在必需检查通过后启用自动合并。
- 同仓库的属主分支应在 PR 确认合并后由该工作流显式删除。
- 由其他任何人创建的 PR 应请求 `costa92` 评审，并保持阻塞，直到 `costa92` 批准当前的 PR head。

该策略设计为配合要求 `go` 和 `governance` 状态检查的分支保护工作，而非 GitHub 内置的必需批准门禁。

仓库级的 `deleteBranchOnMerge` 设置仍保持启用作为安全网，但现在主要的已测试路径在 `pr-governance.yml` 自身内部：启用自动合并、等待 PR 可见地被合并，然后用 GitHub API 删除同仓库的 head ref。独立的下游清理工作流在推广期间经过测试，但已不再是文档记录的主要机制。

完整的多仓治理设计，包括
`llm-agent`、`llm-agent-rag`、`llm-agent-flow`、`llm-agent-providers`、
`llm-agent-otel` 与 `llm-agent-customer-support` 之间的关系，记录在核心仓库的文档中：

- [`PR-GOVERNANCE-OVERVIEW.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-OVERVIEW.md)
- [`PR-GOVERNANCE-PROJECTS.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-PROJECTS.md)
- [`PR-GOVERNANCE-RULES.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-RULES.md)
- [`PR-GOVERNANCE-OPERATIONS.md`](https://github.com/costa92/llm-agent/blob/main/docs/PR-GOVERNANCE-OPERATIONS.md)

## See also

- [`llm-agent` CLAUDE.md](https://github.com/costa92/llm-agent/blob/main/CLAUDE.md) —— 项目硬性规则（仅标准库的核心、无 K8s、能力按 (provider x model) 区分）。
- [`llm-agent` ROADMAP](https://github.com/costa92/llm-agent/blob/main/.planning/ROADMAP.md) —— 8 阶段的 v0.3 里程碑计划。
- [`DEPRECATIONS.md`](https://github.com/costa92/llm-agent/blob/main/DEPRECATIONS.md) —— 处于 v0.4 移除轨道上的符号。
- [`llm-agent-providers`](https://github.com/costa92/llm-agent-providers) —— 本服务所组合的提供方适配器。
- [`llm-agent-otel`](https://github.com/costa92/llm-agent-otel) —— 本服务所使用的 OTel 装饰器包装器。

## License

MIT —— 参见 [LICENSE](LICENSE)。
