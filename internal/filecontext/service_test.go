package filecontext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSource struct {
	thread Thread
	items  []Item
	meta   Metadata
	body   []byte
	calls  []string
}

func (f *fakeSource) Thread(_ context.Context, id string) (Thread, error) {
	f.calls = append(f.calls, "thread")
	if id != f.thread.ID {
		return Thread{}, errors.New("missing")
	}
	return f.thread, nil
}
func (f *fakeSource) Items(_ context.Context, threadID, turnID string) ([]Item, error) {
	f.calls = append(f.calls, "items:"+turnID)
	return f.items, nil
}
func (f *fakeSource) Metadata(_ context.Context, path string) (Metadata, error) {
	f.calls = append(f.calls, "metadata")
	return f.meta, nil
}
func (f *fakeSource) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.calls = append(f.calls, "read")
	return f.body, nil
}

func fixture(t *testing.T) (*Service, *fakeSource, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "internal", "hello.go")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package hello\n\nfunc Hi() string {\n\treturn \"hello <world> & friends\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSource{
		thread: Thread{ID: "thread-a", CWD: root},
		items:  []Item{{ID: "item-a", Type: "fileChange", Changes: []Change{{Path: "internal/hello.go", Kind: "update", Diff: "@@"}}}},
		meta:   Metadata{IsFile: true, ModifiedAtMS: 42},
		body:   []byte("package hello\n\nfunc Hi() string {\n\treturn \"hello <world> & friends\"\n}\n"),
	}
	return New(f), f, path
}

func TestChangesBatchesAndDeduplicatesNativeFileChanges(t *testing.T) {
	s, f, _ := fixture(t)
	f.items = []Item{
		{ID: "ignored", Type: "agentMessage"},
		{ID: "first", Type: "fileChange", Changes: []Change{{Path: "a.go", Kind: "add", Diff: "a"}, {Path: "b.go", Kind: "update", Diff: "b"}}},
		{ID: "last", Type: "fileChange", Changes: []Change{{Path: "a.go", Kind: "update", Diff: "new"}}},
	}
	got, err := s.Changes(context.Background(), ChangesRequest{ThreadID: "thread-a", TurnIDs: []string{"turn-a"}})
	if err != nil {
		t.Fatal(err)
	}
	changes := got.Turns[0].FileChanges
	if len(changes) != 2 || changes[0].Path != "a.go" || changes[0].Kind != "update" || changes[0].ItemID != "last" || changes[1].Path != "b.go" {
		t.Fatalf("changes: %#v", changes)
	}
}

func TestChangesRejectsOversizedBatchAndOmitsUnsafeNativePath(t *testing.T) {
	s, f, _ := fixture(t)
	turns := make([]string, 11)
	for i := range turns {
		turns[i] = "turn"
	}
	if _, err := s.Changes(context.Background(), ChangesRequest{ThreadID: "thread-a", TurnIDs: turns}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("batch: %v", err)
	}
	f.items[0].Changes = append(f.items[0].Changes, Change{Path: "../secret", Kind: "update"})
	got, err := s.Changes(context.Background(), ChangesRequest{ThreadID: "thread-a", TurnIDs: []string{"turn-a"}})
	if err != nil || len(got.Turns) != 1 || len(got.Turns[0].FileChanges) != 1 {
		t.Fatalf("invalid discovery path should be omitted without failing the batch: %+v %v", got, err)
	}
}

func TestChangesNormalizesNativeAbsolutePathOnlyInsideWorkspace(t *testing.T) {
	s, f, path := fixture(t)
	f.items[0].Changes[0].Path = path
	got, err := s.Changes(context.Background(), ChangesRequest{ThreadID: "thread-a", TurnIDs: []string{"turn-a"}})
	if err != nil || got.Turns[0].FileChanges[0].Path != "internal/hello.go" {
		t.Fatalf("inside: %+v %v", got, err)
	}
	f.items[0].Changes[0].Path = filepath.Join(filepath.Dir(f.thread.CWD), "outside.go")
	got, err = s.Changes(context.Background(), ChangesRequest{ThreadID: "thread-a", TurnIDs: []string{"turn-a"}})
	if err != nil || len(got.Turns) != 1 || len(got.Turns[0].FileChanges) != 0 {
		t.Fatalf("outside discovery path should be omitted: %+v %v", got, err)
	}
}

func TestReadRequiresDeclaredPathAndReturnsCurrentText(t *testing.T) {
	s, _, _ := fixture(t)
	got, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/hello.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "internal/hello.go" || got.SHA256 == "" || got.LineCount != 6 || !strings.Contains(got.Content, "hello <world>") || got.Version != "current-workspace" {
		t.Fatalf("read: %#v", got)
	}
	if _, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "go.mod"}); CodeOf(err) != CodePathNotAllowed {
		t.Fatalf("undeclared: %v", err)
	}
}

func TestReadAcceptsNativeAbsolutePathOnlyAfterWorkspaceNormalization(t *testing.T) {
	s, f, path := fixture(t)
	f.items[0].Changes[0].Path = path
	got, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/hello.go"})
	if err != nil || got.Path != "internal/hello.go" {
		t.Fatalf("read: %+v %v", got, err)
	}
}

func TestReadRejectsTraversalSymlinkBinaryAndTooLarge(t *testing.T) {
	s, f, path := fixture(t)
	for _, bad := range []string{"/tmp/x", "../x", "a/../../x", ""} {
		if _, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: bad}); CodeOf(err) != CodePathNotAllowed {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	link := filepath.Join(filepath.Dir(path), "link.go")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	f.items[0].Changes[0].Path = "internal/link.go"
	if _, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/link.go"}); CodeOf(err) != CodePathNotAllowed {
		t.Fatalf("symlink: %v", err)
	}
	f.items[0].Changes[0].Path = "internal/hello.go"
	f.body = []byte{'a', 0, 'b'}
	if _, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/hello.go"}); CodeOf(err) != CodeUnsupportedFileType {
		t.Fatalf("binary: %v", err)
	}
	f.body = make([]byte, MaxFileBytes+1)
	if _, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/hello.go"}); CodeOf(err) != CodeFileTooLarge {
		t.Fatalf("large: %v", err)
	}
}

func TestPrepareValidatesDigestLinesTextAndEscapesEnvelope(t *testing.T) {
	s, _, _ := fixture(t)
	read, err := s.Read(context.Background(), ReadRequest{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: "internal/hello.go"})
	if err != nil {
		t.Fatal(err)
	}
	req := PrepareRequest{Selections: []Selection{{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", Path: read.Path, SHA256: read.SHA256, ModifiedAtMS: read.ModifiedAtMS, StartLine: 4, EndLine: 4, Text: "return \"hello <world> & friends\""}}}
	got, err := s.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Quotes) != 1 || !strings.Contains(got.Envelope, `\u003cworld\u003e`) || !strings.Contains(got.Envelope, `\u0026`) || strings.Contains(got.Envelope, "<world>") {
		t.Fatalf("envelope: %s", got.Envelope)
	}
	req.Selections[0].SHA256 = strings.Repeat("0", 64)
	if _, err := s.Prepare(context.Background(), req); CodeOf(err) != CodeFileChanged {
		t.Fatalf("digest: %v", err)
	}
	req.Selections[0].SHA256 = read.SHA256
	req.Selections[0].StartLine = 3
	if _, err := s.Prepare(context.Background(), req); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("line: %v", err)
	}
}

func TestPrepareLimits(t *testing.T) {
	s, _, _ := fixture(t)
	tooMany := PrepareRequest{Selections: make([]Selection, MaxSelections+1)}
	if _, err := s.Prepare(context.Background(), tooMany); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("count: %v", err)
	}
}
