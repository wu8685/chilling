package codexapp

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wu8685/chilling/internal/filecontext"
)

var (
	realMarkdownReference = regexp.MustCompile(`\]\((<?)([^)\r\n>]+)>?\)`)
	realPlainReference    = regexp.MustCompile(`(?:^|[\s：])((?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\.[A-Za-z0-9]{1,16}(?::[1-9][0-9]*)?)`)
)

// Opt-in, read-only compatibility probe. It never starts or mutates a turn.
func TestRealCodexFileContextReadOnly(t *testing.T) {
	binary, threadID := os.Getenv("CHILLING_REAL_CODEX_BINARY"), os.Getenv("CHILLING_REAL_THREAD_ID")
	if binary == "" || threadID == "" {
		t.Skip("requires explicit real Codex binary and thread")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process, err := Start(ctx, binary, os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	service := filecontext.New(NewSource(process))
	cursor := ""
	turnCount, changeCount, rejectedTurns, readFailures := 0, 0, 0, 0
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"threadId": threadID, "limit": 10, "sortDirection": "desc", "itemsView": "notLoaded"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := process.Call(ctx, "thread/turns/list", params, &page); err != nil {
			t.Fatal(err)
		}
		for _, turn := range page.Data {
			turnCount++
			changes, err := service.Changes(ctx, filecontext.ChangesRequest{ThreadID: threadID, TurnIDs: []string{turn.ID}})
			if err != nil {
				if filecontext.CodeOf(err) == filecontext.CodePathNotAllowed {
					rejectedTurns++
					continue
				}
				t.Fatal(err)
			}
			for _, change := range changes.Turns[0].FileChanges {
				changeCount++
				if change.Kind == "delete" {
					continue
				}
				read, err := service.Read(ctx, filecontext.ReadRequest{ThreadID: threadID, TurnID: turn.ID, ItemID: change.ItemID, Path: change.Path})
				if err != nil {
					readFailures++
					continue
				}
				if read.Content == "" || read.SHA256 == "" {
					t.Fatalf("empty read for %s", change.Path)
				}
				return
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	t.Fatalf("no readable fileChange in bounded pages (turns=%d changes=%d rejectedTurns=%d readFailures=%d)", turnCount, changeCount, rejectedTurns, readFailures)
}

// Opt-in compatibility probe for the public agentMessage text projection. It
// only attempts references already present in the selected thread.
func TestRealCodexMessageReferenceReadOnly(t *testing.T) {
	binary, threadID := os.Getenv("CHILLING_REAL_CODEX_BINARY"), os.Getenv("CHILLING_REAL_THREAD_ID")
	if binary == "" || threadID == "" {
		t.Skip("requires explicit real Codex binary and thread")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process, err := Start(ctx, binary, os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	source := NewSource(process)
	service := filecontext.New(source)
	cursor := ""
	candidates := 0
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"threadId": threadID, "limit": 10, "sortDirection": "desc", "itemsView": "notLoaded"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := process.Call(ctx, "thread/turns/list", params, &page); err != nil {
			t.Fatal(err)
		}
		for _, turn := range page.Data {
			items, err := source.Items(ctx, threadID, turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Type != "agentMessage" {
					continue
				}
				for _, match := range realMarkdownReference.FindAllStringSubmatch(item.Text, -1) {
					raw := strings.TrimSpace(match[2])
					candidates++
					if read, err := service.ReferenceRead(ctx, filecontext.ReferenceReadRequest{ThreadID: threadID, TurnID: turn.ID, ItemID: item.ID, ReferenceKind: filecontext.ReferenceKindMarkdownLink, RawReference: raw}); err == nil && read.Path != "" && read.SHA256 != "" {
						return
					}
				}
				for _, match := range realPlainReference.FindAllStringSubmatch(item.Text, -1) {
					raw := match[1]
					candidates++
					if read, err := service.ReferenceRead(ctx, filecontext.ReferenceReadRequest{ThreadID: threadID, TurnID: turn.ID, ItemID: item.ID, ReferenceKind: filecontext.ReferenceKindPlainPath, RawReference: raw}); err == nil && read.Path != "" && read.SHA256 != "" {
						return
					}
				}
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	t.Fatalf("no readable agentMessage reference in bounded pages (candidates=%d)", candidates)
}
