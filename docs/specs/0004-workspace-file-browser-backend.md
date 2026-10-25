# 0004：Ariel 当前工作区文件浏览后端

- 状态：Implemented（用户于 2026-10-25 确认；Go／race／vet 已通过）
- 日期：2026-10-25
- 对应 Ariel：`docs/specs/0105-workspace-file-browser.md`
- 前置：[0002 文件上下文本地后端](0002-ariel-file-context-backend.md)、[0003 会话内文档引用](0003-inline-workspace-document-references.md)

## 1. 目标与边界

Chilling 在 Desktop 主机本地为 Ariel 提供只读工作区目录枚举、文本读取及选区复验。工作区根只能来自 Codex App Server 对目标 thread 返回的权威 `cwd`。

本能力是对 0002“只有 turn change 已证明文件可读”边界的显式扩展，不改变默认行为。必须同时开启：

```text
--enable-ariel-file-context
--enable-ariel-workspace-browser
```

不开启第二个 flag 时，现有 changes/read/reference/prepare 行为与权限不变。

## 2. Contract

`GET /v1/file-context/capabilities` 增加 `workspaceBrowser`。保持 `file-context/v1` 向后兼容；旧 client 忽略字段，旧 server 缺失字段等价于 false。

新增：

```text
POST /v1/file-context/workspace/files/list
POST /v1/file-context/workspace/file/read
```

### 2.1 List

请求：`threadId/path/cursor/limit`。`path=""` 表示 cwd root；其他路径必须是 clean 的 slash-separated 相对目录。默认 limit 100、最大 200。

每次请求重新读取 thread metadata 并 canonicalize cwd；只对该目录执行一层 `ReadDir`，不递归。响应按 directory-first、name ascending 稳定排序，返回相对路径、kind、size、mtime、readable、目录 revision 与绑定 revision 的 opaque cursor。单响应最大 256 KiB。

symlink 只作为 `kind=symlink, readable=false` 显示，不跟随；`.git` 只显示 root entry，不允许展开。socket、device、FIFO 等非普通 entry 不返回。首版不解析 `.gitignore`。

### 2.2 Read

请求：`threadId/path`。读取前重新取得 thread cwd，逐段拒绝 symlink 和 escape；只接受 regular UTF-8 text，最大 1 MiB。响应为相对 path、content、SHA-256、mtime、line count、language 和 `version=current-workspace`，不返回绝对 cwd。

### 2.3 Prepare

现有 Selection 改为有判别字段的 union。`source=workspace_browser` 不要求 turn/item proof，但要求 `threadId/path/digest/mtime/lines/text`；prepare 必须调用同一 workspace read 规则重新校验。现有 `turn_change` 和 `message_reference` 继续按原证据链执行，不能因新增分支放宽。

## 3. 实现约束

1. 目录枚举与文件读取由 Chilling 进程在本地主机完成；不用未经验证的 Codex 私有目录 API，也不启动另一个 Codex mutation owner。
2. thread metadata 仍通过公开 App Server 读取。Web、Relay、请求参数和 cache 不能指定 workspace root。
3. canonical root 可以解析 thread cwd 本身；其下任何 symlink 均不跟随。路径校验必须覆盖每一段并在读取时复验。
4. 目录 revision 至少覆盖 entry name、kind、size 和 mtime；cursor 与 revision 不匹配返回 `DIRECTORY_CHANGED`，不得跳页或混页。
5. response、错误和日志均有界；日志只记录方法类别、耗时、entry count、响应 bytes 和错误码，不记录 cwd、相对路径、文件名或正文。
6. 不持久化目录、正文、digest 或选区；不连接 MySQL，不影响 task module。

## 4. 错误

新增 `DIRECTORY_NOT_FOUND`、`DIRECTORY_CHANGED`、`CAPACITY_EXCEEDED`，并复用 `INVALID_ARGUMENT`、`THREAD_NOT_FOUND`、`PATH_NOT_ALLOWED`、`FILE_NOT_FOUND`、`UNSUPPORTED_FILE_TYPE`、`FILE_TOO_LARGE`、`FILE_CHANGED`、`INTERNAL`。

## 5. TDD 与验收

1. flag/config red：第二个 flag 默认 false；单独开启时报配置错误；关闭时 handler、capability 与 filesystem 调用均不可达。
2. service red：root／子目录、排序、分页、revision 变化、空目录、unicode 名、dotfile、`.git`、symlink、特殊文件、cwd 变化及 workspace escape。
3. read red：正常 UTF-8、空文件、CRLF、无扩展名、binary、非法 UTF-8、1 MiB 边界、过大文件、读时替换与 symlink。
4. prepare red：三种 selection source 严格分支；workspace digest／mtime／行内容变化失败，旧两种来源不能绕过原 item proof。
5. HTTP/client red：unknown fields、超大 body、响应上限、timeout、capability backward compatibility 和错误码。
6. integration：临时 workspace + fake thread metadata，验证 list → read → prepare，全程只读且不泄露绝对路径。
7. 全量 Go、race、vet 与跨仓 Ariel gateway/Web/Playwright 验收通过。

## 6. 验收标准

- 明确开启后，只能浏览目标 thread 当前 cwd 内的普通目录和文本文件。
- 每个未展开目录、未点击文件都不产生 I/O；大目录可稳定分页。
- symlink、`.git` 内部、workspace escape、binary 和超限内容不可读。
- 浏览器选区发送前重新校验；文件变化不会静默发送旧内容。
- 关闭功能时 contract 与资源使用完全保持 0002/0003 行为。

## 7. 实现证据

实现与验证记录见 [2026-10-25 工作区文件浏览后端验收](../testing/2026-10-25-workspace-file-browser.md)。
