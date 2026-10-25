# 0003：会话内 Workspace 文档引用后端

- 状态：Implemented（2026-10-25；原始 item 证明、workspace 收敛、受控读取与引用复验已完成）
- 日期：2026-10-17
- 影响范围：`file-context` contract、Codex App Server 只读 source、文件读取校验
- 对应前端规格：Ariel 0094
- 前置规格：0002

## 1. 目标

在不开放任意 workspace 文件读取的前提下，让 Chilling 为 Ariel 验证并读取 Codex assistant message 中明确引用的本地文档。引用既可以是 Markdown link destination，也可以是边界明确的裸文件名／相对路径；两者都必须回读原始 item 证明，不能仅凭 Web 传入路径授权。

## 2. Contract

新增独立 endpoint：

```json
POST /v1/file-context/reference/read
{
  "threadId": "...",
  "turnId": "...",
  "itemId": "...",
  "referenceKind": "markdown_link",
  "rawReference": "docs/design.md:37",
  "startLine": 37,
  "endLine": 37
}
```

- `referenceKind` 只能是 `markdown_link` 或 `plain_path`；`rawReference` 必须分别是原始 assistant Markdown link destination 或普通文本中的完整裸 token；可选行号只是 UI hint，服务端必须重新解析；
- 现有 `POST /v1/file-context/read` 保持 0002 行为及相对路径 contract，不增加授权分支；
- capability 返回新增布尔能力 `messageReferences`，旧 client 忽略该字段，旧 server 则由 Ariel 降级。

## 3. 授权验证

对于 reference read：

1. 经官方 App Server 只读方法取得 thread，确认 ID 与 cwd；
2. 读取指定 turn 的 items，找到精确 itemId；
3. item type 必须为 `agentMessage`；
4. 按 `referenceKind` 使用有界 parser：Markdown parser 提取 link destination；plain parser 只在非 link、非 code、非 URL、非 email、非 HTML、非 diff fenced block 的普通文本中提取边界明确的文件 token；不执行多轮 URI decode；
5. 请求 `rawReference` 与 item 中某一完整 parser token 必须先逐字节匹配，再对该匹配结果执行一次规范化；
6. 将绝对路径收敛为 cwd 内相对路径，或以 cwd 解析相对路径；
7. 继续执行 0002 的 no-symlink、regular file、UTF-8、1 MiB、digest 与 metadata 校验。

只知道 `threadId + workspace path`、只匹配 link label、近似匹配 basename、或在另一 item 中出现该路径，均不得授权。裸 basename 只按 thread cwd 根目录解析，不递归搜索；不存在或有歧义时 fail closed。

## 4. 输出

沿用 0002 `ReadResponse`：项目相对 `path`、`content`、`sha256`、`modifiedAtMs`、`lineCount`、`language` 和 `version=current-workspace`。普通文档引用不返回 diff。

不得返回 thread cwd、原始绝对路径、完整 assistant message 或其他 link。

## 5. 边界与错误

- 单 item 最多解析 256 KiB 文本，Markdown 与裸引用合计最多 24 个；超出部分不参与授权；raw reference 最大 4096 UTF-8 bytes，拒绝控制字符；
- 不接受 `file://`、HTTP URL、UNC／网络 share、空路径、目录或 traversal；
- item／reference／path／file 类型错误使用 Ariel 0094 规定的明确 error code；
- App Server read 超时或 shape 不兼容时 fail closed，不直接调用 os.ReadFile 猜测。

## 6. TDD

先写失败测试覆盖：

- agentMessage 中声明的绝对和相对 Markdown link 可读；
- agentMessage 普通文本中声明的 basename、相对路径和 cwd 内绝对路径可读；
- 未声明路径、其他 item、code／URL／email／diff 中的伪路径、只匹配 label 均拒绝；
- workspace 外绝对路径、`../`、symlink、URL、binary 和超限文件拒绝；
- 0002 fileChange read 不回归；
- capability 的新字段对旧 client/server 保持兼容；
- reference endpoint 与原 file-change endpoint 分离，绝对 destination 不能借机进入旧 `path` contract；
- 真实隔离 Codex fixture 产生带本地文件 Markdown link 与裸文件名的 assistant reply，验证 App Server 历史读取 shape 与 Chilling read；另记录普通与 subagent 文件修改的 native shape，确认 `file.context.changes` 能否生成 Codex 同类“已编辑”卡。

## 7. 验收标准

1. 只有原始 `agentMessage.text` 中真实存在并通过对应 parser 的本地 Markdown destination／裸文件 token 能授权读取。
2. 读取结果只暴露项目相对路径和有界当前文本。
3. 现有 file-change、prepare、任务模块开关与 MySQL 零依赖边界不回归。
4. Go unit、race、HTTP contract、跨仓 fixture 和真实只读 Codex 集成测试通过。

## 8. 实现与验证记录（2026-10-25）

- 新增 `/v1/file-context/reference/read` 与 `messageReferences` capability；读取前精确回读 `threadId + turnId + itemId`，只接受 `agentMessage`。
- Markdown destination 与裸路径均在原始文本中逐字节证明；服务端与 Web 同样只承认按文本顺序出现的前 24 个引用；code fence、inline code、URL、email、raw HTML、其他 item 与 workspace escape 均 fail closed。
- 引用选区进入 `prepare` 时重新执行 reference read，并继续校验 digest、mtime、行范围和精确文本，不能把一次读取回执改造成任意 workspace 文件引用。
- `go test ./...`、目标 race suite 与 `go vet ./...` 通过。
- 使用本机 `codex-cli 0.145.0-alpha.27` 对当前真实 thread 运行两个 opt-in 只读 probe，native `fileChange` 与 `agentMessage` reference 均可经 App Server 历史 shape 读取；probe 不启动或修改 turn。
- 当前版本尚未独立取得 subagent 修改的 native item fixture，故不扩大 `file.context.changes` 的 shape 声明。
