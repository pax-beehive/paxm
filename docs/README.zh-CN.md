# paxm 中文使用指南

paxm（PAX Memory）是一个本地优先的 memory adaptor。它把 Codex、Claude
Code、OpenCode、Pi、Cursor、TRAE、TRAE CN、Kimi Code、ZCode、Kiro、Cline
和 MCP 客户端的记忆请求统一路由到 SQLite、Zep、Mem0、MemOS、OpenViking
或自定义 provider。默认使用本地 SQLite，不要求先申请账号、API key 或额外的
embedding/LLM 服务。

本页是中文入口；完整的字段说明仍以英文的
[配置参考](config.md)、[架构说明](architecture.md) 和
[provider contract](provider-adapter-contract.md) 为准。自定义 JSON-RPC
provider 请直接看[中文接入指南](jsonrpc-provider-protocol.zh-CN.md)。

## 1. 安装与第一次运行

安装最新发布版本：

```bash
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
paxm version
```

需要固定版本或回滚时，在安装前设置 `PAXM_VERSION`，例如：

```bash
export PAXM_VERSION=v0.1.27
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
```

执行交互式 setup：

```bash
paxm setup
paxm config doctor
```

默认配置文件为 `~/.config/paxm/config.yaml`。setup 负责建立稳定的
`user_id`、配置 agent、选择 provider 和安装被动集成；用户自己负责 provider
密钥、hook 信任以及是否启用集成。不要把真实密钥提交到仓库或复制到文档中。

## 2. 先跑通一条记忆链路

显式写入、召回和检查历史：

```bash
paxm remember --profile ltm --text "生产发布必须走 GitHub Actions，不能从笔记本直接发布"
paxm recall --query "生产环境怎么发布？"
paxm history --days 7
```

常用 profile：

- `ltm`：长期记忆；适合项目决策、约定和稳定偏好。
- `stm`：短期记忆；默认会设置过期时间，适合当前任务上下文。
- `default`：由当前配置决定，适合脚本或不想指定 profile 的调用。

CLI 只负责参数解析和输出，真正的路由、超时、排序和 provider 调用由 paxm
runtime 处理。需要脚本消费结果时可以使用各命令的 `--json` 输出。

## 3. 接入 agent

### Codex plugin

```bash
codex plugin marketplace add pax-beehive/paxm --ref paxm-memory-v0.1.4
codex plugin add paxm-memory@pax-agent-nexus
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
paxm setup --integration codex-plugin
```

新开一个 Codex task，并在 `/hooks` 提示出现时信任 Pax Agent neXus hooks。插件
负责 active-memory skill 和 Codex 被动 hook 的安装，但不会替用户写入密钥或绕过
hook 信任。

### Claude Code plugin

```bash
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
claude plugin marketplace add pax-beehive/paxm
claude plugin install paxm-claude@pax-memory
paxm setup --integration claude-plugin
```

Claude plugin 提供 active-memory skill、paxm MCP server 以及
`SessionStart`、`UserPromptSubmit`、`PostToolUse`、`PostToolUseFailure`、
`Stop` 五个生命周期 hook。

### OpenCode、Pi、CLI 和 MCP

OpenCode 或 Pi 选择对应 agent 后，setup 会安装本地被动插件；也可以只使用 CLI：

```bash
paxm setup
paxm remember --profile ltm --text "SQLite 是本项目的默认本地 memory provider"
paxm recall --query "默认 memory provider"
```

MCP 客户端可以直接启动：

```bash
paxm mcp serve --agent codex
```

把 `codex` 换成配置中的 agent 名称。MCP 复用同一个 runtime 和 provider 路由，
不需要再写一套 provider 集成。

Cursor、TRAE、TRAE CN、Kimi Code、ZCode、Kiro 和 Cline 也可以直接在
`paxm setup` 中选择。setup 会合并它自己的 hook 和 MCP 条目，不覆盖客户端里
已有的其他配置；具体事件、文件路径、Cursor/Cline 的宿主限制以及卸载/回滚方法
见 [Agent 集成矩阵](agent-integrations.md)。

## 4. user、agent、session 身份

每个 agent 的 `session_start` 会收到一段 paxm 注入的身份上下文，包含当前的：

- `user_id`：稳定的用户身份，例如 `todd`；
- `agent_id`：当前 agent 身份，例如 `codex-todd`；
- `session_id`：本次 agent/runtime 会话身份。

同一次 `session_start` 还会注入当前本地时间和时区。之后每次 `user_input` 都会
与上一 turn 的最近活动时间比较；间隔严格超过 12 小时时，paxm 会在处理该输入
前重新注入本地时间。刚好 12 小时不会重复注入。Codex、Claude Code 和 Pi 使用
各自的 session-start 事件；OpenCode 会在该 session 的第一条消息前完成同样的
bootstrap。

写入 provider 的 memory 还会带有 `turn_id`（如果当前事件有 turn）以及写入
profile 的 `scope`。因此一次记忆可以同时回答“谁产生了它”和“它属于哪个可见
范围”。召回结果会尽量保留这些 metadata；provider adapter 负责把 provider 的
原生响应映射回统一结构。

这些字段是 provenance（来源数据），不是认证或 ACL。不要把模型输出里的 user
ID 当作可信身份，也不要把 provider 的 run ID、OpenViking session 或 JSON-RPC
request ID 当作 paxm 的 `session_id`。访问控制仍由 paxm profile 和 provider
原生策略负责。

## 5. 配置 provider

provider、recall profile 和 write profile 都在
`~/.config/paxm/config.yaml` 中配置。SQLite 是默认 provider；远程 provider
需要额外的连接地址和凭据。配置改完后先检查：

```bash
paxm config doctor
```

自定义 JSON-RPC provider 的最小配置如下：

```yaml
providers:
  private_memory:
    type: jsonrpc
    enabled: true
    transport: stdio
    command: /opt/paxm/providers/private-memory
    args: ["--config", "/etc/private-memory.yaml"]
    env:
      PRIVATE_MEMORY_DB: /var/lib/private-memory/memory.json
    timeout: 30s

recall_profiles:
  default:
    providers:
      - name: private_memory
        required: true

write_profiles:
  ltm:
    tier: ltm
    scope:
      type: personal
      id: todd
    providers:
      - name: private_memory
        required: true
        timeout: 30s
```

一个 profile 可以路由到多个 provider。`required`、`weight`、单 provider
`timeout`、结果阈值和 tiers 决定失败策略与排序；这些策略不应该复制到 CLI 或
provider 可执行文件里。

更完整的 YAML 示例见[配置参考](config.md)。

### Mem0 分数方向

不同 Mem0 部署或 vector store 对返回字段的语义可能不同，不能看到字段名是
`score` 或 `similarity` 就自动判断方向。`score_semantics` 默认是
`similarity`，用于兼容原有配置；如果自托管 Mem0 使用 pgvector 的 cosine
distance（`vector <=> query`，范围通常为 `[0,2]`），应显式配置：

```yaml
providers:
  mem0_pgvector:
    type: mem0
    enabled: true
    base_url: http://localhost:8888
    user_id: todd
    score_semantics: distance
```

paxm 会在阈值、排序和 hook 注入之前把 distance 转换为
`1 - distance/2` 的 `[0,1]` relevance；原始值仍保留在 `raw_score`，并以
`mem0_distance` 或 `mem0_cloud_distance` 标明语义。错误的 `score_semantics`
会在配置校验阶段失败。现有 recall telemetry 还会记录 provider 的
`raw_score_kinds`、`candidate_count` 和 `eligible_count`，用于区分分数方向
错误与阈值过滤，不新增公共命令。

### Mem0 搜索 scope payload

不同 self-hosted Mem0 版本对 `/search` 中 `user_id`、`agent_id`、`run_id`
的位置要求不一致。`search_scope_payload` 支持：

- `auto`：默认值。先放入 nested `filters`；只有服务端明确返回缺少 scope 的
  兼容性错误时，才用顶层字段重试一次，并缓存成功形态。
- `filters`：只发送 nested filters，适合严格的新式部署。
- `top_level`：只把 entity scope 放在请求顶层；其他 metadata filter 仍放在
  `filters`。

Mem0 0.1.117 的 OpenAPI schema 虽然包含 `filters`，其运行时仍要求至少一个
顶层 entity ID，因此不能只根据 OpenAPI 字段是否存在自动判断。team-memory
当前固定的 0.1.117 eval 镜像推荐显式配置：

```yaml
providers:
  memory:
    type: mem0
    enabled: true
    base_url: http://mem0:8000
    user_id: eval-user
    run_id: finance-r6
    score_semantics: distance
    search_scope_payload: top_level
```

认证失败、超时、embedding/vector store 错误或其他普通 4xx/5xx 不会触发
fallback，也不会被吞掉。

## 6. 可靠性和排障

- 被动写入先进入本地 durable queue，再交给 provider；provider 慢或暂时不可用
  时会重试，不应阻塞 agent。
- 被动召回默认有 `800ms` 总预算和 `250ms` 单 provider 预算；健康 provider 的
  部分结果仍可返回。
- SQLite 的数据库父目录必须可写，因为 WAL/SHM 文件会创建在数据库旁边。只读
  sandbox 可能报 SQLite error 14；请为评测使用单独的可写路径。
- 首次安装后建议依次运行 `paxm config doctor`、`remember`、`recall` 和
  `history`，先确认显式链路，再依赖被动 hook。
- JSON-RPC provider 的 stdout 只能输出一条 JSON-RPC 响应；调试日志写 stderr。
  看到“无响应”“JSON 解析失败”时，先检查这一点。

## 7. 相关文档

- [中文 JSON-RPC provider 接入指南](jsonrpc-provider-protocol.zh-CN.md)
- [英文 JSON-RPC v1 协议](jsonrpc-provider-protocol.md)
- [配置参考](config.md)
- [架构说明](architecture.md)
- [Provider adapter contract](provider-adapter-contract.md)
- [发布指南](release.md)
- [完整 JSON-RPC 示例](../examples/jsonrpc-provider/)
- [JSON-RPC 示例中文说明](../examples/jsonrpc-provider/README.zh-CN.md)
