package codexapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

type fakeCaller struct {
	methods []string
	params  []any
}

func (f *fakeCaller) Call(_ context.Context, method string, params any, result any) error {
	f.methods = append(f.methods, method)
	f.params = append(f.params, params)
	var body string
	switch method {
	case "thread/read":
		body = `{"thread":{"id":"thread-a","cwd":"/workspace"}}`
	case "thread/items/list":
		body = `{"data":[{"turnId":"turn-a","item":{"type":"fileChange","id":"item-a","changes":[{"path":"a.go","kind":{"type":"update","move_path":null},"diff":"@@"}],"status":"completed"},"startedAtMs":1,"completedAtMs":2},{"turnId":"turn-a","item":{"type":"agentMessage","id":"reply","text":"文件：a.go"}}],"nextCursor":null}`
	case "fs/getMetadata":
		body = `{"isDirectory":false,"isFile":true,"isSymlink":false,"createdAtMs":1,"modifiedAtMs":2}`
	case "fs/readFile":
		body = `{"dataBase64":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}`
	default:
		return fmt.Errorf("unexpected method %s", method)
	}
	return json.Unmarshal([]byte(body), result)
}

func TestSourceMapsOnlyPublicReadMethods(t *testing.T) {
	f := &fakeCaller{}
	s := NewSource(f)
	thread, err := s.Thread(context.Background(), "thread-a")
	if err != nil || thread.ID != "thread-a" || thread.CWD != "/workspace" {
		t.Fatalf("thread: %+v %v", thread, err)
	}
	items, err := s.Items(context.Background(), "thread-a", "turn-a")
	if err != nil || len(items) != 2 || items[0].Changes[0].Kind != "update" || items[1].Text != "文件：a.go" {
		t.Fatalf("items: %+v %v", items, err)
	}
	meta, err := s.Metadata(context.Background(), "/workspace/a.go")
	if err != nil || !meta.IsFile || meta.ModifiedAtMS != 2 {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	body, err := s.ReadFile(context.Background(), "/workspace/a.go")
	if err != nil || string(body) != "hello" {
		t.Fatalf("read: %q %v", body, err)
	}
	if want := []string{"thread/read", "thread/items/list", "fs/getMetadata", "fs/readFile"}; !reflect.DeepEqual(f.methods, want) {
		t.Fatalf("methods: %v", f.methods)
	}
	if f.params[0].(map[string]any)["includeTurns"] != false || f.params[1].(map[string]any)["sortDirection"] != "asc" {
		t.Fatalf("params: %#v", f.params)
	}
}
