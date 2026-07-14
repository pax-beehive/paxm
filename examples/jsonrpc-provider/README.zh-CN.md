# JSON-RPC provider 示例（中文）

这个示例是一个通过 stdio 提供 paxm JSON-RPC v1 协议的 Go 可执行文件。paxm
每次请求都会启动新进程，所以示例把数据保存到 JSON 文件，而不是进程内存。

## 构建与验证

在仓库根目录运行：

```bash
go build -o /tmp/paxm-sample-provider ./examples/jsonrpc-provider
PAXM_SAMPLE_PROVIDER_STORE=/tmp/paxm-sample-store.json \
  paxm eval provider jsonrpc --command /tmp/paxm-sample-provider --json
```

conformance kit 会检查 health、写入、稳定 ref、召回 fidelity、batch、delete，
以及示例声明的 origin/scope attribution round-trip。

## 配置到 paxm

在 ~/.config/paxm/config.yaml 中配置：

```yaml
providers:
  sample_jsonrpc:
    type: jsonrpc
    enabled: true
    transport: stdio
    command: /tmp/paxm-sample-provider
    env:
      PAXM_SAMPLE_PROVIDER_STORE: /tmp/paxm-sample-store.json
    timeout: 30s
```

生产环境应把 command 指向稳定安装路径，把 env 指向受权限保护的持久化目录。
JSON 文件只是教学用途，不提供生产级并发、备份或访问控制。

完整的字段定义、实现步骤、来源语义和排障见仓库根目录的
[中文 JSON-RPC 接入指南](../../docs/jsonrpc-provider-protocol.zh-CN.md)。
