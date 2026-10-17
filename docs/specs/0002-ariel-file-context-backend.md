# 0002：Ariel 文件上下文本地后端

- 状态：Approved（用户于 2026-10-17 确认）
- 归属：Chilling 本地服务。
- 调用方：Ariel Desktop Agent 的受限 extension gateway；前端行为见 Ariel `docs/specs/0089-turn-file-links-and-selection-context.md`。
- 不涉及：任务看板／MySQL、Chilling 自带文件 UI、直接公网入口、替代 Ariel 的 Codex mutation owner。

## 1. 目标与边界

Chilling 为 Ariel 提供一个本机 `file-context/v1` contract：读取 Codex turn 的结构化文件变更、按需读取受限文本文件、验证用户选择的片段，并生成可交给 Ariel 既有 owner 发送的 canonical context。

该模块与 0001 的任务功能独立：

- 使用独立 flag `--enable-ariel-file-context`，默认关闭；
- 不要求 `--enable-tasks`；
- 不读取 `CHILLING_MYSQL_DSN`，不连接 MySQL，不启动 task scheduler／worker；
- 关闭 task 功能时仍可单独提供文件上下文后端。

Chilling 不通过本模块调用 `turn/start`、queue、steer、interrupt 或审批接口。Ariel 保持当前 thread 的唯一远程 mutation owner；Chilling 只读 native 状态并准备上下文，避免并发 owner。

## 2. 本地部署

建议入口：

```text
chilling serve --enable-ariel-file-context --socket <configured-path>
```

- 默认仅监听 Unix domain socket；socket 目录与文件只允许当前 OS 用户访问，创建权限不宽于 `0700/0600`。
- socket 路径来自配置或 OS app-data 目录，不硬编码用户名和绝对 home。
- 首版不监听公网。若未来支持 loopback TCP，必须另有随机 bearer credential、Origin 不参与信任判断，并保持默认关闭。
- 停止 Chilling 只使 Ariel capability 消失，不停止 Codex turn、不修改文件、不重启 Ariel。

## 3. Contract 与 handshake

Agent 连接后先调用 capability handshake；响应至少包含：

- contract：`file-context/v1`
- enabled／ready
- Chilling build version
- Codex adapter profile/version
- limits：turn batch、文件大小、选区数量和总字节数

major version 不一致必须 fail closed；minor 新字段向后兼容。ready 只有在 flag 开启、Codex binary 可用、只读 App Server 初始化成功且必要方法通过 profile gate 后为 true。

提供三个业务操作：

| 操作 | 输入 | 输出 |
| --- | --- | --- |
| `changes` | `threadId`, `turnIds[]` | 每个 turn 的有序 `fileChanges[]` |
| `read` | `threadId`, `turnId`, `itemId`, 相对 `path`, 可选 revalidate metadata | 当前 UTF-8 文件、行数、语言提示、digest、mtime 与 turn diff |
| `prepare` | 来源身份、相对路径、digest、1-based 行号、选中文本 | canonical context envelope 与可展示 quote metadata |

所有操作使用有界 request ID 和 deadline；不得接受调用方给出的 Codex method、绝对路径、socket、URL 或 shell command。

## 4. Codex 能力与实测依据

本机 ChatGPT App 内置 `codex-cli 0.160.0` 已提供：

1. `ThreadItem.type = "fileChange"`，包含 `{path, kind, diff}` 的 `changes[]`；
2. `turn/diff/updated` 的 turn 聚合 diff；
3. `fs/getMetadata` 和 `fs/readFile`；
4. `turn/start` 的普通文本及可选上下文字段。

`fs/readFile` 已通过独立只读 App Server 进程实测：读取 Ariel 仓库 `go.mod` 成功返回 760 bytes。该证据只证明当前安装版本的读取原语，不能证明 Chilling contract 已实现。

当前没有一个公开、稳定、完整描述“路径 + 起止行 + 选中文本”的专用 selection input。因此 Chilling 生成普通文本形式的 canonical envelope；Ariel owner 负责发送，后续若 native selection API 稳定，只替换 Chilling Adapter，不改变 Ariel Web contract。

## 5. Changes 读取

1. 使用公开 App Server 分页读取指定 thread／turn items，只接受 native `fileChange`。
2. 返回项目相对路径、规范化 kind、有界 diff 和 `diffTruncated`；native path 若为绝对路径，必须先以 thread 权威 `cwd` 证明其位于 workspace 内并转换为相对路径。workspace 外或无法规范化的 change 按项 fail closed 并从摘要中省略，不得让可选摘要阻断会话打开；`read` 对同类路径仍返回 `PATH_NOT_ALLOWED`。响应不返回 workspace 绝对路径。
3. 同一 turn 同一路径多次出现时按首次出现顺序去重，最终 kind／diff 取最后一次完成状态。
4. `turnIds[]` 最多 10 个；单次只读取调用方明确请求的 turn，不扫描全部历史。
5. item shape 缺字段、路径不能归一化或 native 状态不确定时，对该项 fail closed；摘要响应省略该项，不从 activity 文本猜路径，也不把某一项的路径错误升级为整批失败。

## 6. 文件读取与路径安全

1. `read` 必须携带 `threadId + turnId + itemId + path`。Chilling 重新读取该 native item，证明 path 确实出现在其 `fileChange.changes[]` 中。
2. 从 thread 权威 `cwd` 解析相对路径；拒绝绝对路径、空路径、NUL、`..` 穿越、workspace 外结果和符号链接。
3. 调用 `fs/getMetadata` 后只接受 regular file，再调用 `fs/readFile`。不直接信任 Web／Relay 提供的 metadata。
4. 只返回 UTF-8 文本；NUL、无效 UTF-8 或明显二进制返回 `UNSUPPORTED_FILE_TYPE`。
5. 解码内容上限 1 MiB；App Server RPC 继续受硬上限保护。超限返回 `FILE_TOO_LARGE`，不把截断内容称为完整文件。
6. 返回 SHA-256、`modifiedAtMs`、行数、扩展名推导的 language hint、当前正文和有界 turn diff。明确标记 current workspace version，不伪造历史快照。
7. 删除／移动文件返回 `FILE_NOT_FOUND`，但 changes 中的 diff 仍可展示。

## 7. 选区校验与 canonical context

`prepare` 输入最多 5 个选区，单个文本最多 8 KiB，总计最多 24 KiB。每项包含 path、来源 turn/item、起止行、文件 digest、mtime 和选择文本。

Chilling 必须：

1. 重做来源、路径和 workspace 校验；
2. 验证当前文件 digest／mtime 与读取时一致；不一致返回 `FILE_CHANGED`；
3. 按 UTF-8 行边界核对选择文本确实对应声明的连续行区间；不能只信任浏览器正文；
4. 把文件内容标记为 untrusted data，不提升为 system／developer instruction；
5. 输出 canonical JSON context block，正文中的 `<`、`>`、`&` 使用等价 Unicode escape，保证无损且不能提前闭合 envelope；
6. 同时返回不含绝对路径的 quote metadata，供 Ariel 历史 UI 投影。

示意格式：

```text
<ariel_file_context>
{"path":"relative/path.go","startLine":12,"endLine":20,"sha256":"…","text":"lossless JSON escaped text"}
</ariel_file_context>
```

Chilling 不保存该 envelope，不把 prepare receipt 当作 Codex 已受理。Ariel 发送后仍须按自己的 exact client-message identity 核对。

## 8. 资源、日志与缓存

- 不将文件正文、diff、选区或 envelope 写入数据库、磁盘 cache、普通日志、trace 或 crash report。
- 允许有界进程内 metadata／内容 LRU，默认最多 10 个文件且总计 8 MiB；以 thread、path、digest 隔离，进程退出即消失。
- 并发 read／prepare 和 App Server pending calls 均有上限；超限返回 `OVERLOADED`，不无限排队。
- 日志只含 request 类别、耗时、字节数、错误码，以及 thread/path 的不可逆摘要；不记录原始 ID、路径或正文。
- Chilling 不访问 Relay token、Passkey、Web session 或 Ariel 用户凭据。Unix peer 权限是本地调用边界，Ariel 的远端身份仍由 Ariel 自己校验。

## 9. 错误模型

- `FEATURE_DISABLED`
- `NOT_READY`
- `CONTRACT_UNSUPPORTED`
- `THREAD_NOT_FOUND`
- `TURN_NOT_FOUND`
- `ITEM_NOT_FOUND`
- `PATH_NOT_ALLOWED`
- `FILE_NOT_FOUND`
- `UNSUPPORTED_FILE_TYPE`
- `FILE_TOO_LARGE`
- `FILE_CHANGED`
- `PROTOCOL_UNSUPPORTED`
- `OVERLOADED`
- `INTERNAL`

错误正文有界且不泄露绝对路径、Codex socket、凭据或文件片段。timeout／连接中断不自动重试 prepare 或 Codex mutation；读取可由 Ariel 用户显式重试。

## 10. TDD 与验收

获批后按 red → green → refactor：

1. **装配 red**：flag 关闭时不解析 task DSN、不连接 MySQL、不启动 task worker，也不启动 file-context socket／Codex probe；仅开启 file-context 时仍保持 MySQL 零调用。
2. **Handshake red**：版本、limits、ready、method gate 和 Unix socket 权限；major 不兼容 fail closed。
3. **Changes red**：fake App Server 覆盖多 turn batch、add/update/delete、重复路径、分页、异常 shape、超长 diff 和未知 item。
4. **Read red**：合法文本、base64 错误、目录、symlink、二进制、无效 UTF-8、超限、绝对路径、穿越、workspace 外、路径未出现在 item 和文件删除。
5. **Prepare red**：真实连续选区、多引用、Unicode、CRLF、emoji、行号越界、digest 改变、文本不匹配、envelope 提前闭合字符和大小限制。
6. **运行态只读 probe**：隔离 fixture 验证 `fileChange`、`fs/getMetadata`、`fs/readFile`；不向真实会话写消息。
7. **跨仓 integration**：由 Ariel fixture 经 Relay／Agent gateway 调 Chilling，完成 changes → read → prepare，并由 fake owner 验证发送内容；证明 Chilling 没有启动第二 mutation owner。
8. Go 全量／race／vet；对 socket、路径和 payload 做 fuzz／property tests。真实 Codex 版本未通过 profile 时 capability 必须关闭。

## 11. 验收标准

- Chilling 可作为不依赖任务模块和 MySQL 的本机后端单独启动。
- Ariel 只能读取 native fileChange 已证明的 workspace 内文本文件，任意路径读取不可达。
- 选区经 digest、行号和正文三重校验后生成无损 canonical context。
- Chilling 不发 turn，不争抢 Codex owner，不保存文件正文。
- Chilling 停止或升级不兼容时 Ariel capability 自动撤回，普通会话功能不受影响。
