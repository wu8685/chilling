package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
)

func TestSessionCallsJSONLAndRejectsRemoteErrors(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	session := NewSession(client, 1<<20)
	defer session.Close()
	go func() {
		scanner := bufio.NewScanner(server)
		if !scanner.Scan() {
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		_, _ = fmt.Fprintf(server, `{"id":%d,"result":{"ok":true}}`+"\n", req.ID)
	}()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := session.Call(context.Background(), "thread/read", map[string]any{"threadId": "x"}, &out); err != nil || !out.OK {
		t.Fatalf("call: %+v %v", out, err)
	}
}
