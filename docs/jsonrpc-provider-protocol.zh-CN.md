# paxm JSON-RPC memory provider 中文接入指南

JSON-RPC provider 允许用任意语言编写一个独立可执行文件，再通过 paxm 的统一
memory contract 接入私有部署、内部数据库或已有记忆服务。paxm 不需要把 provider
编译进主程序，也不要求 provider 了解 CLI、hook 或 MCP。

协议版本是 paxm-jsonrpc-provider-v1，传输使用 JSON-RPC 2.0 over stdio。英文的
逐字段定义见 [JSON-RPC Provider Protocol v1](jsonrpc-provider-protocol.md)；本文
重点说明实现步骤、来源 metadata 和一致性验证。

## 最短路径

1. 写一个可执行的 provider，并把数据持久化到进程外部。
2. 实现 paxm.health、paxm.put、paxm.search 三个方法。
3. 每次写入返回稳定且非空的 memory ref。
4. 在 YAML 中配置 type: jsonrpc 和可执行文件路径。
5. 用 conformance kit 验证，再把 provider 加入 recall/write profile。

仓库的 [JSON-RPC 示例](../examples/jsonrpc-provider/) 是完整 Go 实现，支持
attribution、batch 和 delete；另有[示例中文说明](../examples/jsonrpc-provider/README.zh-CN.md)。
它适合学习协议，不建议直接当生产数据库。

## 1. 进程与 stdio 约定

paxm 对每一次 RPC 请求启动一个新的 provider 进程：写入一行 JSON 请求、关闭
stdin、读取一条 stdout 响应，然后结束进程。因此 provider 不能只把状态放在内存
里，必须使用 SQLite、文件、服务端或其他外部持久化存储。

必须遵守：

- 请求是一个以换行结尾的 JSON-RPC 2.0 对象。
- stdout 只能输出 JSON-RPC 响应，不能混入日志、进度条或 banner。
- 调试信息写 stderr；paxm 会在错误中保留少量 stderr 尾部。
- id 是字符串，响应必须原样返回请求的 id。
- 不支持的方法返回 -32601；参数非法返回 -32602。
- timeout 覆盖进程启动、stdin、stdout 和退出等待。

最小健康检查：

```json
{"jsonrpc":"2.0","id":"1","method":"paxm.health","params":{}}
```

```json
{"jsonrpc":"2.0","id":"1","result":{"ok":true}}
```

只要 result 成功，paxm 就认为 provider 健康；推荐仍返回 {"ok":true} 方便排查。

## 2. 在 paxm 中配置

在 ~/.config/paxm/config.yaml 增加 provider 实例：

```yaml
providers:
  private_memory:
    type: jsonrpc
    enabled: true
    transport: stdio
    command: /opt/paxm/providers/private-memory
    args: ["--config", "/etc/private-memory.yaml"]
    env:
      PRIVATE_MEMORY_DB: /var/lib/private-memory/memory.sqlite
    timeout: 30s

recall_profiles:
  default:
    providers:
      - name: private_memory
        required: true
        weight: 1.0
    max_results: 3

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

command 必须是 paxm 进程可执行的路径；args 按原样传给进程；env 用于传递数据库
路径或服务端配置，不要把密钥硬编码进 provider 源码。配置完成后运行：

```bash
paxm config doctor
```

profile 决定 provider 是否参与读取或写入、required、超时、权重、tier 和结果阈值。
provider 本身只实现数据协议，不负责决定 CLI 或 agent 的路由策略。

## 3. 必须实现的方法

### paxm.put

参数是一个 MemoryItem。最重要的字段是 text、metadata、created_at、tier、origin
和 scope；未知字段必须忽略，以便协议向前兼容。

```json
{
  "jsonrpc":"2.0",
  "id":"7",
  "method":"paxm.put",
  "params":{
    "text":"生产发布必须走 GitHub Actions",
    "source":"codex",
    "tier":"ltm",
    "metadata":{"project":"paxm"},
    "origin":{
      "user_id":"todd",
      "agent_id":"codex-todd",
      "session_id":"session-abc",
      "turn_id":"turn-42"
    },
    "scope":{"type":"personal","id":"todd"}
  }
}
```

成功响应至少返回一个稳定 ref：

```json
{"jsonrpc":"2.0","id":"7","result":{"ref":{"id":"memory-123"}}}
```

如果客户端提供了 id，可以沿用；否则由 provider 生成。ID 必须非空、稳定、可用于
后续 delete 和 search 对比。

### paxm.search

参数包含查询文本，以及可选的 limit、metadata 精确匹配过滤和 tiers 过滤：

```json
{
  "jsonrpc":"2.0",
  "id":"8",
  "method":"paxm.search",
  "params":{
    "text":"生产怎么发布",
    "limit":5,
    "metadata":{"project":"paxm"},
    "tiers":["ltm"]
  }
}
```

响应格式：

```json
{
  "jsonrpc":"2.0",
  "id":"8",
  "result":{
    "hits":[
      {
        "id":"memory-123",
        "text":"生产发布必须走 GitHub Actions",
        "relevance":0.92,
        "score":0.92,
        "metadata":{"project":"paxm"},
        "tier":"ltm",
        "origin":{
          "user_id":"todd",
          "agent_id":"codex-todd",
          "session_id":"session-abc",
          "turn_id":"turn-42"
        },
        "scope":{"type":"personal","id":"todd"}
      }
    ]
  }
}
```

id 和 text 必须忠实对应存储内容。relevance、score 必须是 [0,1] 内的
adapter-neutral 分数；后端 BM25、距离等原生分数放入 raw_score，并同时提供
raw_score_kind。metadata filter 不能静默丢弃，否则召回范围会悄悄扩大。

### paxm.health

参数为 {}。任何成功的 JSON-RPC result 都可作为健康响应，推荐返回：

```json
{"ok":true}
```

## 4. 可选能力与生命周期

### paxm.capabilities

这是可选方法。未实现时返回 -32601，paxm 会按 legacy provider 处理。实现时返回：

```json
{"put_batch":true,"delete":true,"attribution":true}
```

- put_batch：支持 paxm.putBatch。
- delete：支持 paxm.delete，删除成功后 ref 不再可搜索。
- attribution：能对 origin 和 scope 做逐条、无损的写入和召回 round-trip。

不要因为 metadata 里看到了 paxm_session_id 就宣称 attribution:true。只有 provider
能保留结构化来源，并在 raw search hit 中返回相同值时，才可以宣称这个能力。

### paxm.putBatch

参数是 {"items":[MemoryItem,...]}，响应必须按输入顺序返回 refs：

```json
{"refs":[{"id":"memory-1"},{"id":"memory-2"}]}
```

不支持时返回 -32601，paxm 会退化为逐条 paxm.put。声明 put_batch:true 后，必须
真正支持该方法，并返回与输入数量一致的非空 refs。

### paxm.delete

参数是 memory ref，例如：

```json
{"provider":"private_memory","id":"memory-123"}
```

成功响应可以为空 result，也可以返回 {"deleted":true}。声明 delete:true 后，
conformance kit 会确认删除的 ref 不再出现在 search 结果里。

## 5. provenance：origin、scope 和 metadata

- origin 表示记忆由谁、哪个 agent、哪个 session/turn 产生。
- scope 表示写入时指定的可见边界，例如 personal/todd 或 team/pax。
- metadata 是 provider 可扩展的字符串键值；paxm 会把结构化来源同步成兼容键。

paxm 发送的规范 metadata key：

| key | 来源 |
| --- | --- |
| paxm_user_id | origin.user_id |
| paxm_agent_id | origin.agent_id |
| paxm_session_id | origin.session_id |
| paxm_turn_id | origin.turn_id |
| paxm_scope_type | scope.type |
| paxm_scope_id | scope.id |

支持结构化字段的 provider 应优先存储 origin 和 scope，metadata 只作为兼容层。旧
provider 也可以只回传 metadata 或 legacy provenance；paxm 会尽量恢复统一结构，
但这不等于精确 attribution。

origin 是随 memory 存储的来源数据，不是认证信息。session_id 必须是 agent runtime
的原始会话 ID，不能替换成 provider ingestion session、Mem0 run_id、OpenViking
session、conversation ID、进程 ID 或 JSON-RPC request ID。授权仍由 paxm profile
和 provider 原生 ACL 单独完成。

## 6. 实现建议

1. 先设计 id、text、metadata、tier、created_at、origin、scope 的持久化结构。
2. 读取 stdin 的一行 JSON，校验 jsonrpc、id、method，统一封装成功和错误响应。
3. 实现 put，返回稳定 ref；保留未知 metadata key。
4. 实现 search，应用 metadata 和 tiers 过滤，返回忠实的 id/text 和归一化分数。
5. 只声明已经验证的 capabilities。
6. 若声明 attribution，逐条保存和返回完整 origin/scope，不要猜测来源。
7. 实现 delete 后再声明 delete:true。

主循环可以遵循下面的伪代码：

```text
request = read_one_json_line(stdin)
try:
    params = request.get("params", {})
    result = dispatch(request["method"], params)
    write_json({"jsonrpc": "2.0", "id": request["id"], "result": result}, stdout)
except UnknownMethod:
    write_json({"jsonrpc": "2.0", "id": request["id"],
                "error": {"code": -32601, "message": "method not found"}}, stdout)
except InvalidParams:
    write_json({"jsonrpc": "2.0", "id": request["id"],
                "error": {"code": -32602, "message": "invalid params"}}, stdout)
```

因为 paxm 每次调用都会新建进程，dispatch 的结果必须写入外部存储；不要依赖下一
次进程还能看到上一次的全局变量。

## 7. 一致性验证

先构建 provider，再运行黑盒 conformance kit：

```bash
go build -o /tmp/private-memory-provider ./path/to/provider
paxm eval provider jsonrpc \
  --command /tmp/private-memory-provider \
  --arg --config \
  --arg /etc/private-memory.yaml \
  --json
```

仓库示例：

```bash
go build -o /tmp/paxm-sample-provider ./examples/jsonrpc-provider
PAXM_SAMPLE_PROVIDER_STORE=/tmp/paxm-sample-store.json \
  paxm eval provider jsonrpc --command /tmp/paxm-sample-provider --json
```

验证包括 health、写入确认、稳定 ref、search 的 id/text/metadata fidelity、
batch fallback/实现，以及声明 delete 时的删除生命周期。声明 attribution:true
时，还会写入不同的 user、agent、session、turn 和 scope，检查 raw hit 是否逐字段
准确返回。

## 8. 常见错误

| 现象 | 原因与修复 |
| --- | --- |
| decode jsonrpc response | stdout 混入日志或没有输出 JSON；把日志移到 stderr。 |
| response id mismatch | 响应没有原样返回字符串 ID；不要把字符串 ID 转成数字。 |
| put did not return a memory ref | result.ref.id 为空；生成稳定的非空 ID。 |
| 写入成功但下一次 search 找不到 | 把记录存在进程内存；改成外部持久化。 |
| metadata filter 没效果 | provider 丢弃 params.metadata；实现精确匹配或明确报错。 |
| attribution conformance 失败 | origin/scope 被丢弃、改写，或 raw hit 没结构化字段。 |
| provider 偶发超时 | 检查 timeout、启动开销、数据库锁和 stdout 是否及时 flush。 |
| delete 后仍能召回 | 声明 delete:true 但 search 仍返回已删除 ref；修复索引/过滤。 |

兼容性原则是“字段可增加、语义不能悄悄改变”：未列出的字段应忽略，旧 provider
可以不实现 capabilities、putBatch、delete 和 attribution。返回未知的来源，比凭
provider-native session 伪造来源更安全。
