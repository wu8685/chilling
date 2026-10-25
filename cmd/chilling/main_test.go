package main

import "testing"

func TestParseServeRequiresExplicitFileContextFlag(t *testing.T) {
	if _, err := parse([]string{"serve", "--socket", "/tmp/x.sock", "--codex-binary", "/bin/codex"}); err == nil {
		t.Fatal("disabled server accepted")
	}
	cfg, err := parse([]string{"serve", "--enable-ariel-file-context", "--socket", "/tmp/x.sock", "--codex-binary", "/bin/codex", "--cwd", "/tmp"})
	if err != nil || !cfg.enabled || cfg.socket != "/tmp/x.sock" || cfg.binary != "/bin/codex" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	if _, err := parse([]string{"serve", "--enable-ariel-file-context", "--enable-tasks"}); err == nil {
		t.Fatal("unknown task flag accepted by file-context entrypoint")
	}
}

func TestWorkspaceBrowserRequiresBothExplicitFlags(t *testing.T) {
	if _, err := parse([]string{"serve", "--enable-ariel-workspace-browser", "--socket", "/tmp/x.sock", "--codex-binary", "/bin/codex"}); err == nil {
		t.Fatal("workspace browser accepted without file context")
	}
	cfg, err := parse([]string{"serve", "--enable-ariel-file-context", "--enable-ariel-workspace-browser", "--socket", "/tmp/x.sock", "--codex-binary", "/bin/codex", "--cwd", "/tmp"})
	if err != nil || !cfg.workspaceBrowser {
		t.Fatalf("config: %+v %v", cfg, err)
	}
}
