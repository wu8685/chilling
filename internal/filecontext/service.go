// Package filecontext implements Chilling's read-only file-context/v1 contract.
package filecontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Contract                  = "file-context/v1"
	MaxTurnBatch              = 10
	MaxFileBytes              = 1 << 20
	MaxSelections             = 5
	MaxSelectionBytes         = 8 << 10
	MaxTotalBytes             = 24 << 10
	MaxDiffBytes              = 64 << 10
	MaxWorkspaceResponseBytes = 256 << 10
)

type Code string

const (
	CodeInvalidArgument     Code = "INVALID_ARGUMENT"
	CodeThreadNotFound      Code = "THREAD_NOT_FOUND"
	CodeItemNotFound        Code = "ITEM_NOT_FOUND"
	CodePathNotAllowed      Code = "PATH_NOT_ALLOWED"
	CodeFileNotFound        Code = "FILE_NOT_FOUND"
	CodeUnsupportedFileType Code = "UNSUPPORTED_FILE_TYPE"
	CodeFileTooLarge        Code = "FILE_TOO_LARGE"
	CodeFileChanged         Code = "FILE_CHANGED"
	CodeReferenceNotFound   Code = "REFERENCE_NOT_FOUND"
	CodeFeatureDisabled     Code = "FEATURE_DISABLED"
	CodeDirectoryChanged    Code = "DIRECTORY_CHANGED"
	CodeInternal            Code = "INTERNAL"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) }
func fail(code Code) error     { return &Error{Code: code, Message: string(code)} }
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeInternal
}

type Thread struct{ ID, CWD string }
type Change struct{ Path, Kind, Diff string }
type Item struct {
	ID, Type string
	Text     string
	Changes  []Change
}
type Metadata struct {
	IsFile, IsDirectory, IsSymlink bool
	ModifiedAtMS                   int64
}

type Source interface {
	Thread(context.Context, string) (Thread, error)
	Items(context.Context, string, string) ([]Item, error)
	Metadata(context.Context, string) (Metadata, error)
	ReadFile(context.Context, string) ([]byte, error)
}

type Service struct {
	source           Source
	workspaceBrowser bool
}

func New(source Source) *Service { return &Service{source: source} }
func NewWithWorkspaceBrowser(source Source, enabled bool) *Service {
	return &Service{source: source, workspaceBrowser: enabled}
}

type WorkspaceEntryKind string

const (
	WorkspaceEntryFile        WorkspaceEntryKind = "file"
	WorkspaceEntryDirectory   WorkspaceEntryKind = "directory"
	WorkspaceEntrySymlink     WorkspaceEntryKind = "symlink"
	DefaultWorkspaceListLimit                    = 100
	MaxWorkspaceListLimit                        = 200
)

type WorkspaceListRequest struct {
	ThreadID string `json:"threadId"`
	Path     string `json:"path"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
type WorkspaceEntry struct {
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	Kind         WorkspaceEntryKind `json:"kind"`
	SizeBytes    int64              `json:"sizeBytes"`
	ModifiedAtMS int64              `json:"modifiedAtMs"`
	Readable     bool               `json:"readable"`
}
type WorkspaceListResponse struct {
	Path       string           `json:"path"`
	Entries    []WorkspaceEntry `json:"entries"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Revision   string           `json:"revision"`
}
type workspaceCursor struct {
	Path, Revision string
	Offset         int
}

func (s *Service) WorkspaceList(ctx context.Context, req WorkspaceListRequest) (WorkspaceListResponse, error) {
	if s == nil || s.source == nil || !s.workspaceBrowser {
		return WorkspaceListResponse{}, fail(CodeFeatureDisabled)
	}
	if req.ThreadID == "" || req.Limit < 0 || req.Limit > MaxWorkspaceListLimit {
		return WorkspaceListResponse{}, fail(CodeInvalidArgument)
	}
	if req.Limit == 0 {
		req.Limit = DefaultWorkspaceListLimit
	}
	rel, ok := safeWorkspaceDirectory(req.Path)
	if !ok {
		return WorkspaceListResponse{}, fail(CodePathNotAllowed)
	}
	thread, err := s.source.Thread(ctx, req.ThreadID)
	if err != nil || thread.ID != req.ThreadID || thread.CWD == "" {
		return WorkspaceListResponse{}, fail(CodeThreadNotFound)
	}
	abs, err := resolveWorkspacePath(thread.CWD, rel)
	if err != nil {
		return WorkspaceListResponse{}, err
	}
	children, err := os.ReadDir(abs)
	if err != nil {
		return WorkspaceListResponse{}, fail(CodeFileNotFound)
	}
	entries := make([]WorkspaceEntry, 0, len(children))
	for _, child := range children {
		info, statErr := child.Info()
		if statErr != nil {
			continue
		}
		kind := WorkspaceEntryFile
		readable := info.Mode().IsRegular()
		if child.Type()&os.ModeSymlink != 0 {
			kind, readable = WorkspaceEntrySymlink, false
		} else if child.IsDir() {
			kind, readable = WorkspaceEntryDirectory, true
		}
		if !info.Mode().IsRegular() && !child.IsDir() && kind != WorkspaceEntrySymlink {
			continue
		}
		childPath := child.Name()
		if rel != "" {
			childPath = rel + "/" + child.Name()
		}
		if childPath == ".git" {
			readable = false
		}
		entries = append(entries, WorkspaceEntry{Name: child.Name(), Path: childPath, Kind: kind, SizeBytes: info.Size(), ModifiedAtMS: info.ModTime().UnixMilli(), Readable: readable})
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Kind == WorkspaceEntryDirectory) != (entries[j].Kind == WorkspaceEntryDirectory) {
			return entries[i].Kind == WorkspaceEntryDirectory
		}
		return entries[i].Name < entries[j].Name
	})
	revision := workspaceRevision(entries)
	offset := 0
	if req.Cursor != "" {
		cursor, decodeErr := decodeWorkspaceCursor(req.Cursor)
		if decodeErr != nil || cursor.Path != rel || cursor.Offset < 0 || cursor.Offset > len(entries) {
			return WorkspaceListResponse{}, fail(CodeInvalidArgument)
		}
		if cursor.Revision != revision {
			return WorkspaceListResponse{}, fail(CodeDirectoryChanged)
		}
		offset = cursor.Offset
	}
	return boundedWorkspacePage(rel, entries, offset, req.Limit, revision)
}

func boundedWorkspacePage(path string, entries []WorkspaceEntry, offset, limit int, revision string) (WorkspaceListResponse, error) {
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	for end > offset {
		out := WorkspaceListResponse{Path: path, Entries: entries[offset:end], Revision: revision}
		if end < len(entries) {
			out.NextCursor = encodeWorkspaceCursor(workspaceCursor{Path: path, Revision: revision, Offset: end})
		}
		body, err := json.Marshal(out)
		if err != nil {
			return WorkspaceListResponse{}, fail(CodeInternal)
		}
		if len(body) <= MaxWorkspaceResponseBytes {
			return out, nil
		}
		end--
	}
	return WorkspaceListResponse{}, fail(CodeFileTooLarge)
}

func workspaceRevision(entries []WorkspaceEntry) string {
	h := sha256.New()
	for _, entry := range entries {
		_, _ = h.Write([]byte(entry.Name + "\x00" + string(entry.Kind) + "\x00" + strconv.FormatInt(entry.SizeBytes, 10) + "\x00" + strconv.FormatInt(entry.ModifiedAtMS, 10) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func encodeWorkspaceCursor(cursor workspaceCursor) string {
	body, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}
func decodeWorkspaceCursor(value string) (workspaceCursor, error) {
	var out workspaceCursor
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		err = json.Unmarshal(body, &out)
	}
	return out, err
}
func safeWorkspaceDirectory(input string) (string, bool) {
	if input == "" {
		return "", true
	}
	rel, ok := safeRelativePath(input)
	if !ok || rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", false
	}
	return rel, true
}
func resolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fail(CodePathNotAllowed)
		}
		return abs, nil
	}
	return resolveNoSymlink(root, rel)
}

type WorkspaceReadRequest struct {
	ThreadID string `json:"threadId"`
	Path     string `json:"path"`
}

func (s *Service) WorkspaceRead(ctx context.Context, req WorkspaceReadRequest) (ReadResponse, error) {
	if s == nil || s.source == nil || !s.workspaceBrowser {
		return ReadResponse{}, fail(CodeFeatureDisabled)
	}
	if req.ThreadID == "" {
		return ReadResponse{}, fail(CodeInvalidArgument)
	}
	rel, ok := safeRelativePath(req.Path)
	if !ok || rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	thread, err := s.source.Thread(ctx, req.ThreadID)
	if err != nil || thread.ID != req.ThreadID || thread.CWD == "" {
		return ReadResponse{}, fail(CodeThreadNotFound)
	}
	return s.readCurrentFile(ctx, thread.CWD, rel, nil)
}

type ChangesRequest struct {
	ThreadID string   `json:"threadId"`
	TurnIDs  []string `json:"turnIds"`
}
type FileChange struct {
	ItemID        string `json:"itemId"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Diff          string `json:"diff,omitempty"`
	DiffTruncated bool   `json:"diffTruncated,omitempty"`
}
type TurnChanges struct {
	TurnID      string       `json:"turnId"`
	FileChanges []FileChange `json:"fileChanges"`
}
type ChangesResponse struct {
	Turns []TurnChanges `json:"turns"`
}

func (s *Service) Changes(ctx context.Context, req ChangesRequest) (ChangesResponse, error) {
	if s == nil || s.source == nil || req.ThreadID == "" || len(req.TurnIDs) == 0 || len(req.TurnIDs) > MaxTurnBatch {
		return ChangesResponse{}, fail(CodeInvalidArgument)
	}
	out := ChangesResponse{Turns: make([]TurnChanges, 0, len(req.TurnIDs))}
	thread, err := s.source.Thread(ctx, req.ThreadID)
	if err != nil || thread.ID != req.ThreadID || thread.CWD == "" {
		return ChangesResponse{}, fail(CodeThreadNotFound)
	}
	for _, turnID := range req.TurnIDs {
		if turnID == "" {
			return ChangesResponse{}, fail(CodeInvalidArgument)
		}
		items, err := s.source.Items(ctx, req.ThreadID, turnID)
		if err != nil {
			return ChangesResponse{}, fail(CodeItemNotFound)
		}
		order := make([]string, 0)
		byPath := map[string]FileChange{}
		for _, item := range items {
			if item.Type != "fileChange" || item.ID == "" {
				continue
			}
			for _, change := range item.Changes {
				path, ok := normalizeNativePath(thread.CWD, change.Path)
				if !ok {
					continue
				}
				kind := strings.ToLower(change.Kind)
				if kind != "add" && kind != "update" && kind != "delete" {
					continue
				}
				if _, seen := byPath[path]; !seen {
					order = append(order, path)
				}
				diff, truncated := truncate(change.Diff, MaxDiffBytes)
				byPath[path] = FileChange{ItemID: item.ID, Path: path, Kind: kind, Diff: diff, DiffTruncated: truncated}
			}
		}
		turn := TurnChanges{TurnID: turnID, FileChanges: make([]FileChange, 0, len(order))}
		for _, path := range order {
			turn.FileChanges = append(turn.FileChanges, byPath[path])
		}
		out.Turns = append(out.Turns, turn)
	}
	return out, nil
}

func normalizeNativePath(root, input string) (string, bool) {
	if !filepath.IsAbs(input) {
		return safeRelativePath(input)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(rootAbs, filepath.Clean(input))
	if err != nil {
		return "", false
	}
	return safeRelativePath(filepath.ToSlash(relative))
}

type ReadRequest struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Path     string `json:"path"`
}
type ReadResponse struct {
	Path          string `json:"path"`
	Content       string `json:"content"`
	SHA256        string `json:"sha256"`
	ModifiedAtMS  int64  `json:"modifiedAtMs"`
	LineCount     int    `json:"lineCount"`
	Language      string `json:"language"`
	Diff          string `json:"diff,omitempty"`
	DiffTruncated bool   `json:"diffTruncated,omitempty"`
	Version       string `json:"version"`
}

func (s *Service) Read(ctx context.Context, req ReadRequest) (ReadResponse, error) {
	if s == nil || s.source == nil || req.ThreadID == "" || req.TurnID == "" || req.ItemID == "" {
		return ReadResponse{}, fail(CodeInvalidArgument)
	}
	rel, ok := safeRelativePath(req.Path)
	if !ok {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	thread, err := s.source.Thread(ctx, req.ThreadID)
	if err != nil || thread.ID != req.ThreadID || thread.CWD == "" {
		return ReadResponse{}, fail(CodeThreadNotFound)
	}
	items, err := s.source.Items(ctx, req.ThreadID, req.TurnID)
	if err != nil {
		return ReadResponse{}, fail(CodeItemNotFound)
	}
	var declared *Change
	for _, item := range items {
		if item.ID == req.ItemID && item.Type == "fileChange" {
			for i := range item.Changes {
				normalized, valid := normalizeNativePath(thread.CWD, item.Changes[i].Path)
				if valid && normalized == rel {
					declared = &item.Changes[i]
					break
				}
			}
		}
	}
	if declared == nil {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	return s.readCurrentFile(ctx, thread.CWD, rel, declared)
}

func (s *Service) readCurrentFile(ctx context.Context, root, rel string, declared *Change) (ReadResponse, error) {
	abs, err := resolveNoSymlink(root, rel)
	if err != nil {
		return ReadResponse{}, err
	}
	meta, err := s.source.Metadata(ctx, abs)
	if err != nil {
		return ReadResponse{}, fail(CodeFileNotFound)
	}
	if meta.IsSymlink || meta.IsDirectory || !meta.IsFile {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	body, err := s.source.ReadFile(ctx, abs)
	if err != nil {
		return ReadResponse{}, fail(CodeFileNotFound)
	}
	if len(body) > MaxFileBytes {
		return ReadResponse{}, fail(CodeFileTooLarge)
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return ReadResponse{}, fail(CodeUnsupportedFileType)
	}
	digest := sha256.Sum256(body)
	diff, truncated := "", false
	if declared != nil {
		diff, truncated = truncate(declared.Diff, MaxDiffBytes)
	}
	return ReadResponse{Path: rel, Content: string(body), SHA256: hex.EncodeToString(digest[:]), ModifiedAtMS: meta.ModifiedAtMS, LineCount: bytes.Count(body, []byte{'\n'}) + 1, Language: languageFor(rel), Diff: diff, DiffTruncated: truncated, Version: "current-workspace"}, nil
}

type Selection struct {
	Source        SelectionSource `json:"source,omitempty"`
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
	ItemID        string          `json:"itemId"`
	Path          string          `json:"path"`
	SHA256        string          `json:"sha256"`
	ModifiedAtMS  int64           `json:"modifiedAtMs"`
	StartLine     int             `json:"startLine"`
	EndLine       int             `json:"endLine"`
	Text          string          `json:"text"`
	ReferenceKind ReferenceKind   `json:"referenceKind,omitempty"`
	RawReference  string          `json:"rawReference,omitempty"`
}
type SelectionSource string

const (
	SelectionSourceTurnChange       SelectionSource = "turn_change"
	SelectionSourceMessageReference SelectionSource = "message_reference"
	SelectionSourceWorkspaceBrowser SelectionSource = "workspace_browser"
)

type PrepareRequest struct {
	Selections []Selection `json:"selections"`
}
type Quote struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	SHA256    string `json:"sha256"`
	Text      string `json:"text"`
}
type PrepareResponse struct {
	Envelope string  `json:"envelope"`
	Quotes   []Quote `json:"quotes"`
}

func (s *Service) Prepare(ctx context.Context, req PrepareRequest) (PrepareResponse, error) {
	if len(req.Selections) == 0 || len(req.Selections) > MaxSelections {
		return PrepareResponse{}, fail(CodeInvalidArgument)
	}
	total := 0
	out := PrepareResponse{Quotes: make([]Quote, 0, len(req.Selections))}
	var envelope strings.Builder
	for _, selection := range req.Selections {
		if selection.Text == "" || len(selection.Text) > MaxSelectionBytes || selection.StartLine < 1 || selection.EndLine < selection.StartLine {
			return PrepareResponse{}, fail(CodeInvalidArgument)
		}
		total += len(selection.Text)
		if total > MaxTotalBytes {
			return PrepareResponse{}, fail(CodeInvalidArgument)
		}
		var read ReadResponse
		var err error
		if selection.Source == SelectionSourceWorkspaceBrowser {
			if selection.TurnID != "" || selection.ItemID != "" || selection.ReferenceKind != "" || selection.RawReference != "" {
				return PrepareResponse{}, fail(CodeInvalidArgument)
			}
			read, err = s.WorkspaceRead(ctx, WorkspaceReadRequest{ThreadID: selection.ThreadID, Path: selection.Path})
		} else if selection.ReferenceKind != "" || selection.RawReference != "" {
			if selection.ReferenceKind == "" || selection.RawReference == "" {
				return PrepareResponse{}, fail(CodeInvalidArgument)
			}
			read, err = s.ReferenceRead(ctx, ReferenceReadRequest{ThreadID: selection.ThreadID, TurnID: selection.TurnID, ItemID: selection.ItemID, ReferenceKind: selection.ReferenceKind, RawReference: selection.RawReference})
		} else {
			read, err = s.Read(ctx, ReadRequest{ThreadID: selection.ThreadID, TurnID: selection.TurnID, ItemID: selection.ItemID, Path: selection.Path})
		}
		if err != nil {
			return PrepareResponse{}, err
		}
		if read.SHA256 != selection.SHA256 || read.ModifiedAtMS != selection.ModifiedAtMS {
			return PrepareResponse{}, fail(CodeFileChanged)
		}
		if !matchesLines(read.Content, selection.Text, selection.StartLine, selection.EndLine) {
			return PrepareResponse{}, fail(CodeInvalidArgument)
		}
		quote := Quote{Path: read.Path, StartLine: selection.StartLine, EndLine: selection.EndLine, SHA256: read.SHA256, Text: selection.Text}
		out.Quotes = append(out.Quotes, quote)
		body, err := json.Marshal(quote)
		if err != nil {
			return PrepareResponse{}, fail(CodeInternal)
		}
		envelope.WriteString("<ariel_file_context>\n")
		envelope.Write(body)
		envelope.WriteString("\n</ariel_file_context>")
		if len(out.Quotes) < len(req.Selections) {
			envelope.WriteString("\n")
		}
	}
	out.Envelope = envelope.String()
	return out, nil
}

func safeRelativePath(input string) (string, bool) {
	if input == "" || strings.ContainsRune(input, 0) || strings.Contains(input, "\\") || filepath.IsAbs(input) {
		return "", false
	}
	cleaned := filepath.Clean(input)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
}
func resolveNoSymlink(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fail(CodePathNotAllowed)
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(rel))
	relative, err := filepath.Rel(rootAbs, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fail(CodePathNotAllowed)
	}
	current := rootAbs
	for _, part := range strings.Split(filepath.FromSlash(rel), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fail(CodePathNotAllowed)
		}
	}
	return target, nil
}
func truncate(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	return value[:limit], true
}
func languageFor(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "" {
		return "text"
	}
	return ext
}
func matchesLines(content, selected string, start, end int) bool {
	from := 0
	for {
		index := strings.Index(content[from:], selected)
		if index < 0 {
			return false
		}
		index += from
		actualStart := 1 + strings.Count(content[:index], "\n")
		actualEnd := actualStart + strings.Count(selected, "\n")
		if strings.HasSuffix(selected, "\n") && actualEnd > actualStart {
			actualEnd--
		}
		if actualStart == start && actualEnd == end {
			return true
		}
		from = index + 1
		if from >= len(content) {
			return false
		}
	}
}

func (c Code) String() string { return string(c) }
