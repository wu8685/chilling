// Package filecontext implements Chilling's read-only file-context/v1 contract.
package filecontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	Contract          = "file-context/v1"
	MaxTurnBatch      = 10
	MaxFileBytes      = 1 << 20
	MaxSelections     = 5
	MaxSelectionBytes = 8 << 10
	MaxTotalBytes     = 24 << 10
	MaxDiffBytes      = 64 << 10
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

type Service struct{ source Source }

func New(source Source) *Service { return &Service{source: source} }

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
	abs, err := resolveNoSymlink(thread.CWD, rel)
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
	diff, truncated := truncate(declared.Diff, MaxDiffBytes)
	return ReadResponse{Path: rel, Content: string(body), SHA256: hex.EncodeToString(digest[:]), ModifiedAtMS: meta.ModifiedAtMS, LineCount: bytes.Count(body, []byte{'\n'}) + 1, Language: languageFor(rel), Diff: diff, DiffTruncated: truncated, Version: "current-workspace"}, nil
}

type Selection struct {
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	ModifiedAtMS int64  `json:"modifiedAtMs"`
	StartLine    int    `json:"startLine"`
	EndLine      int    `json:"endLine"`
	Text         string `json:"text"`
}
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
		read, err := s.Read(ctx, ReadRequest{ThreadID: selection.ThreadID, TurnID: selection.TurnID, ItemID: selection.ItemID, Path: selection.Path})
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
