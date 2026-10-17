# chilling 项目协作约定

## 项目定位与边界

- chilling 是独立、本地部署的服务。目前包含两类彼此可独立启用的能力：本地任务看板，以及供 Ariel 调用的 Codex 文件上下文后端。
- Ariel 保留现有远程会话用途，不承担 chilling 的任务数据库、调度、摘要或反馈存储。
- Ariel 文件上下文功能采用前后端分离：UI、feature flag、能力发现与受限转发在 Ariel；Codex file-change 查询、文件读取、路径／选区校验和 context 准备在 chilling。chilling 不为此建立第二个 Codex mutation owner。
- 负责人使用本机 Codex 原生 thread 和可核验的官方模型；任务与同一 thread 的绑定必须可追踪，用户可在 Codex App 中查看并继续对话。
- ahsir 只作按需协作，Hetairoi 只作按需外部事件入口；不要求任务必须经过其中任一系统。
- 沿用用户明确要求的 `--enable-tasks` 显式开关。默认关闭，关闭时不连接 MySQL、不启动任务派发和任务专用订阅。
- 文件上下文使用独立显式开关，默认关闭；单独开启时不得读取任务 DSN、连接 MySQL 或启动任务 worker。
- 未经明确要求，不部署公网入口、不把 TaskStore／任务消息同步到 Ariel、远端数据库或云备份，也不启动或重启用户已有服务；用户已选择的官方模型推理与受控外部工具调用按原有授权处理。

## 代码与文档

- 中文为主，保留英文技术术语；后端优先 Go，Web 使用 TypeScript。
- 不硬编码用户名、设备 ID、thread ID、本机路径、模型版本或凭据。
- Codex 配置只维护本项目的 `AGENTS.md`／Codex-native 位置，不创建 Claude Code 对应配置。
- API 或私有 IPC 的能力必须以当前版本的隔离实测证明；接口名称存在不等于已经支持。
- 借鉴 Ariel 的接入证据时保留版本与验证范围，不直接跨 module import 其 `internal` 包，不复制未验证的私有集成绕过 owner 边界。

## SDD 与 TDD

1. 每项组件／功能先在 `docs/specs/` 写输入、输出、行为、不变量、边界、错误处理和验收。
2. Draft 不开始实现；完整规格经用户评审确认后再进入 red → green → refactor。
3. 实际先运行会失败的测试，再写最小实现；MySQL 事务用隔离真实数据库验证，native 会话与 UI 分别保留相应证据。
4. 改变部署方式、状态归属、通信语义或权限范围，先更新规格。不能用旧规格的批准覆盖新范围。

## 验证与数据

- 原生写操作、停止、审批和恢复实验只在隔离目录／fixture thread 中执行，不作用于用户真实会话。
- Codex 是完整会话与实际执行状态的权威来源；MySQL 只保存任务事实、反馈、必要摘要、投递记录和已读水位。
- 不把模型输出、CLI 退出、transport receipt 或连接存活当成任务完成或消息已受理的证据。
- 保持原生唯一 mutation owner；App 手工输入、Ariel 与 chilling 对同一 thread 的竞争需要单独验证。
- 私人知识库和本机凭据不自动上传或推送。chilling 本地数据库不意味着允许把数据传入 Ariel 云端。
- 新待办保持未完成，只有用户确认后才标记完成；Git commit 与 push 遵循用户明确授权。
