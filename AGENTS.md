# paxm 开发要求

## Git 与协作

- `main` 受保护，禁止直接开发、强推或删除；每项工作从最新 `main` 创建
  `codex/<topic>` 分支，提交 PR，等待 CI 与至少一次 review 后合入。
- PR 只包含任务相关改动；勿覆盖或提交他人的未提交文件。
- 提交小而可解释；不要顺手 release、改配置或上传私密数据。

## 架构边界

- CLI 只负责参数解析和输出；运行时通过 facade/tools、memory router、provider
  adapter 分层调用。不要让 CLI 直接依赖具体 provider。
- setup、配置、安装、日志/观测属于 operator；recall/remember 等 agent-facing
  tools 保持窄接口；provider 新增走现有 registry/config/adapter seam。
- 用户负责 key、hook 信任和安装；agent 只调用明确的 recall/write 能力。被动 hook
  必须 fail-open，不能阻塞 agent。
- 新 agent 集成必须验证真实 runtime 的被动写入、全新会话召回、MCP 召回、
  session/workspace/event 顺序，并提供禁用、升级和回滚路径。

## 代码与验证

- 使用 Go 1.25；改 Go 文件后运行 `gofmt`。生产逻辑要清晰拆分，避免为拆而拆。
- 提交前至少运行：
  `go test ./... -count=1`、`go vet ./...`、
  `golangci-lint fmt --diff`、`golangci-lint run`。
- 复杂度门禁：`scripts/check-production-complexity.sh`，圈复杂度 <=20、
  认知复杂度 <=25。覆盖率门禁：
  `go test ./... -covermode=atomic -coverprofile=coverage.out` 后运行
  `scripts/check-production-coverage.sh coverage.out 75`；产品代码覆盖率必须 >=75%。
- 变更行为就补单元/集成测试。eval、bench、conformance 不替代单元测试；付费的
  cross-agent eval 只显式运行，不放入 CI。

## 文档与发布

- 行为、配置、架构或路线变化同步更新 `README.md`、`docs/`。
- 发布遵循 `docs/release.md`：先 PR/CI，再合入、打 tag 和发布；不要把发布动作
  混入普通开发 PR。
