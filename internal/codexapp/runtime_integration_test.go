package codexapp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wu8685/chilling/internal/filecontext"
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
