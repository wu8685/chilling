# 工作区文件浏览后端验收记录

- 日期：2026-10-25
- 规格：[0004 Ariel 当前工作区文件浏览后端](../specs/0004-workspace-file-browser-backend.md)

## 自动化结果

- `go test ./... -count=1` 通过。
- `go test -race ./... -count=1` 通过。
- `go vet ./...` 通过。
- Ariel 的 protocol、gateway、Web unit、production build 和真实 Chromium fixture 通过。

## 已验证行为

- workspace flag 默认关闭，不能脱离 file-context flag 单独开启。
- 每次 list/read/prepare 均由 thread metadata 重新取得权威 cwd。
- 单层分页、稳定排序、revision cursor 和 256 KiB response budget 生效。
- `.git`、symlink、workspace escape、binary、非法 UTF-8 和超过 1 MiB 的正文不可读。
- `workspace_browser` 引用不要求伪造 turn/item，但发送前必须重新核对 digest、mtime、范围和文本。
