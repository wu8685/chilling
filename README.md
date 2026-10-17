# chilling

本地运行的 Codex companion service。目前规划两类可独立启用的能力：个人任务看板，以及供 Ariel Web 使用的文件上下文后端。

- 每张任务卡关联一个 Codex 原生会话，可以在 Codex App 中查看并继续对话。
- 在任务中提交反馈，交给同一位负责 Agent；需要时由它调用 ahsir 协作。
- 后台汇总有效进展，展示阻塞、待决事项和交付物，并通过未读标记提醒查看。
- 任务数据与 MySQL 保存在本地，关闭浏览器不影响后台跟踪。
- Ariel 可以按 feature flag 接入本机文件上下文能力：查看 turn 修改文件、打开当前源码，并把选区带回同一会话；Chilling 不建立第二个 Codex mutation owner。

文件上下文模块已经提供可运行的本地服务；任务看板仍处于规格阶段。默认关闭，显式启动示例：

```bash
go run ./cmd/chilling serve \
  --enable-ariel-file-context \
  --socket "$TMPDIR/chilling/file-context.sock" \
  --codex-binary /path/to/codex \
  --cwd /path/to/workspace
```

socket 必须位于当前用户私有目录；Ariel Desktop Agent 使用同一路径的 `unix://` endpoint 接入。该模块只调用 Codex App Server 的只读 thread／file API，不启动 turn。

- [第一版规格](docs/specs/0001-local-task-kanban.md)
- [Ariel 文件上下文后端规格](docs/specs/0002-ariel-file-context-backend.md)
- [规格索引](docs/specs/README.md)
- [待办](docs/TODO.md)
