package localserver

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestListenCreatesPrivateUnixSocket(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "chilling-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, "private")
	path := filepath.Join(dir, "file-context.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("dir mode: %o", got)
	}
	if got := socketInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("socket mode: %o", got)
	}
}

func TestServerRequiresExplicitFeatureFlag(t *testing.T) {
	if err := Serve(t.Context(), Config{Enabled: false}, http.NewServeMux()); err != ErrFeatureDisabled {
		t.Fatalf("disabled: %v", err)
	}
}
