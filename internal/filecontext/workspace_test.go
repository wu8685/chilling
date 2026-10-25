package filecontext

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workspaceFixture(t *testing.T) (*Service, *fakeSource) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"docs", ".git", "z-dir"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("SAFE=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "readme-link")); err != nil {
		t.Fatal(err)
	}
	f := &fakeSource{thread: Thread{ID: "thread-a", CWD: root}, meta: Metadata{IsFile: true, ModifiedAtMS: 42}, body: []byte("hello\nworld\n")}
	return NewWithWorkspaceBrowser(f, true), f
}

func TestWorkspacePageNeverExceedsResponseBudget(t *testing.T) {
	entries := make([]WorkspaceEntry, 200)
	for i := range entries {
		entries[i] = WorkspaceEntry{Name: "file", Path: strings.Repeat("x", 4000), Kind: WorkspaceEntryFile, Readable: true}
	}
	page, err := boundedWorkspacePage("", entries, 0, 200, "revision")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(page)
	if len(body) > MaxWorkspaceResponseBytes || page.NextCursor == "" || len(page.Entries) >= len(entries) {
		t.Fatalf("bytes=%d entries=%d cursor=%q", len(body), len(page.Entries), page.NextCursor)
	}
}

func TestWorkspaceListIsExplicitPaginatedAndRevisionBound(t *testing.T) {
	s, _ := workspaceFixture(t)
	first, err := s.WorkspaceList(context.Background(), WorkspaceListRequest{ThreadID: "thread-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != "" || first.Revision == "" || len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %#v", first)
	}
	if first.Entries[0].Kind != WorkspaceEntryDirectory || first.Entries[0].Name != ".git" || first.Entries[0].Readable {
		t.Fatalf(".git boundary: %#v", first.Entries[0])
	}
	second, err := s.WorkspaceList(context.Background(), WorkspaceListRequest{ThreadID: "thread-a", Limit: 2, Cursor: first.NextCursor})
	if err != nil || second.Revision != first.Revision || len(second.Entries) == 0 {
		t.Fatalf("second page: %#v %v", second, err)
	}

	if err := os.WriteFile(filepath.Join(s.source.(*fakeSource).thread.CWD, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkspaceList(context.Background(), WorkspaceListRequest{ThreadID: "thread-a", Limit: 2, Cursor: first.NextCursor}); CodeOf(err) != CodeDirectoryChanged {
		t.Fatalf("stale cursor: %v", err)
	}
}

func TestWorkspaceListRejectsEscapeGitAndDisabledFeature(t *testing.T) {
	s, f := workspaceFixture(t)
	for _, path := range []string{"../", "/tmp", ".git", ".git/objects"} {
		if _, err := s.WorkspaceList(context.Background(), WorkspaceListRequest{ThreadID: "thread-a", Path: path}); CodeOf(err) != CodePathNotAllowed {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	if _, err := NewWithWorkspaceBrowser(f, false).WorkspaceList(context.Background(), WorkspaceListRequest{ThreadID: "thread-a"}); CodeOf(err) != CodeFeatureDisabled {
		t.Fatalf("disabled: %v", err)
	}
}

func TestWorkspaceReadAndPrepareRevalidateCurrentFile(t *testing.T) {
	s, f := workspaceFixture(t)
	read, err := s.WorkspaceRead(context.Background(), WorkspaceReadRequest{ThreadID: "thread-a", Path: "README.md"})
	if err != nil || read.Version != "current-workspace" || read.Content != "hello\nworld\n" {
		t.Fatalf("read: %#v %v", read, err)
	}
	selection := Selection{Source: SelectionSourceWorkspaceBrowser, ThreadID: "thread-a", Path: read.Path, SHA256: read.SHA256, ModifiedAtMS: read.ModifiedAtMS, StartLine: 2, EndLine: 2, Text: "world"}
	if got, err := s.Prepare(context.Background(), PrepareRequest{Selections: []Selection{selection}}); err != nil || len(got.Quotes) != 1 {
		t.Fatalf("prepare: %#v %v", got, err)
	}
	f.body = []byte("changed\n")
	if _, err := s.Prepare(context.Background(), PrepareRequest{Selections: []Selection{selection}}); CodeOf(err) != CodeFileChanged {
		t.Fatalf("changed: %v", err)
	}
	for _, path := range []string{"readme-link", ".git/config"} {
		if _, err := s.WorkspaceRead(context.Background(), WorkspaceReadRequest{ThreadID: "thread-a", Path: path}); CodeOf(err) != CodePathNotAllowed {
			t.Fatalf("path %q: %v", path, err)
		}
	}
}
