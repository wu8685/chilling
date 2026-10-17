package codexapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/wu8685/chilling/internal/filecontext"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}
type Source struct{ caller Caller }

func NewSource(caller Caller) *Source { return &Source{caller: caller} }

func (s *Source) Thread(ctx context.Context, id string) (filecontext.Thread, error) {
	var out struct {
		Thread struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"thread"`
	}
	if s == nil || s.caller == nil || id == "" {
		return filecontext.Thread{}, errors.New("invalid thread")
	}
	if err := s.caller.Call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &out); err != nil {
		return filecontext.Thread{}, err
	}
	if out.Thread.ID != id || out.Thread.CWD == "" {
		return filecontext.Thread{}, errors.New("invalid thread shape")
	}
	return filecontext.Thread{ID: out.Thread.ID, CWD: out.Thread.CWD}, nil
}

func (s *Source) Items(ctx context.Context, threadID, turnID string) ([]filecontext.Item, error) {
	var all []filecontext.Item
	cursor := ""
	for page := 0; page < 32; page++ {
		var out struct {
			Data []struct {
				TurnID string          `json:"turnId"`
				Item   json.RawMessage `json:"item"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"threadId": threadID, "turnId": turnID, "limit": 100, "sortDirection": "asc"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := s.caller.Call(ctx, "thread/items/list", params, &out); err != nil {
			return nil, err
		}
		for _, entry := range out.Data {
			if entry.TurnID != turnID {
				continue
			}
			var item struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Changes []struct {
					Path string `json:"path"`
					Kind struct {
						Type string `json:"type"`
					} `json:"kind"`
					Diff string `json:"diff"`
				} `json:"changes"`
			}
			if json.Unmarshal(entry.Item, &item) != nil || item.ID == "" || item.Type == "" {
				continue
			}
			mapped := filecontext.Item{ID: item.ID, Type: item.Type, Changes: make([]filecontext.Change, 0, len(item.Changes))}
			for _, change := range item.Changes {
				mapped.Changes = append(mapped.Changes, filecontext.Change{Path: change.Path, Kind: change.Kind.Type, Diff: change.Diff})
			}
			all = append(all, mapped)
		}
		if out.NextCursor == nil || *out.NextCursor == "" {
			return all, nil
		}
		cursor = *out.NextCursor
	}
	return nil, errors.New("item pagination limit exceeded")
}

func (s *Source) Metadata(ctx context.Context, path string) (filecontext.Metadata, error) {
	var out struct {
		IsDirectory  bool  `json:"isDirectory"`
		IsFile       bool  `json:"isFile"`
		IsSymlink    bool  `json:"isSymlink"`
		ModifiedAtMS int64 `json:"modifiedAtMs"`
	}
	if err := s.caller.Call(ctx, "fs/getMetadata", map[string]any{"path": path}, &out); err != nil {
		return filecontext.Metadata{}, err
	}
	return filecontext.Metadata{IsDirectory: out.IsDirectory, IsFile: out.IsFile, IsSymlink: out.IsSymlink, ModifiedAtMS: out.ModifiedAtMS}, nil
}

func (s *Source) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var out struct {
		DataBase64 string `json:"dataBase64"`
	}
	if err := s.caller.Call(ctx, "fs/readFile", map[string]any{"path": path}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.DataBase64)
}
