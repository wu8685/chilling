package filecontext

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPContractIsAllowlistedAndBounded(t *testing.T) {
	s, _, _ := fixture(t)
	h := NewHTTPHandler(s, "test-build", "codex-test")
	req := httptest.NewRequest(http.MethodGet, "/v1/file-context/capabilities", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("handshake status: %d", res.Code)
	}
	var capability Capability
	if json.Unmarshal(res.Body.Bytes(), &capability) != nil || !capability.Ready || !capability.MessageReferences || capability.Contract != Contract || capability.Limits.MaxFileBytes != MaxFileBytes {
		t.Fatalf("capability: %s", res.Body.String())
	}

	body := []byte(`{"threadId":"thread-a","turnIds":["turn-a"]}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/file-context/changes", bytes.NewReader(body))
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !stringsContain(res.Body.String(), "internal/hello.go") {
		t.Fatalf("changes: %d %s", res.Code, res.Body.String())
	}

	refBody := []byte(`{"threadId":"thread-a","turnId":"turn-a","itemId":"reply","referenceKind":"plain_path","rawReference":"internal/hello.go"}`)
	source := s.source.(*fakeSource)
	source.items = []Item{{ID: "reply", Type: "agentMessage", Text: "文件：internal/hello.go"}}
	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/file-context/reference/read", bytes.NewReader(refBody)))
	if res.Code != http.StatusOK || !stringsContain(res.Body.String(), "internal/hello.go") {
		t.Fatalf("reference read: %d %s", res.Code, res.Body.String())
	}

	for _, path := range []string{"/v1/file-context/proxy", "/v1/file-context/turn/start"} {
		res = httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`))))
		if res.Code != http.StatusNotFound {
			t.Fatalf("unsafe route %s: %d", path, res.Code)
		}
	}
}

func TestHTTPRejectsUnknownFieldsAndOversizedBody(t *testing.T) {
	s, _, _ := fixture(t)
	h := NewHTTPHandler(s, "test", "test")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/file-context/changes", bytes.NewReader([]byte(`{"threadId":"thread-a","turnIds":["turn-a"],"method":"fs/readFile"}`))))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	large := []byte(`{"threadId":"` + strings.Repeat("a", MaxRequestBytes) + `","turnIds":["turn-a"]}`)
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/file-context/changes", bytes.NewReader(large)))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d", res.Code)
	}
}

func stringsContain(value, needle string) bool { return bytes.Contains([]byte(value), []byte(needle)) }
