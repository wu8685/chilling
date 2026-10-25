package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wu8685/chilling/internal/codexapp"
	"github.com/wu8685/chilling/internal/filecontext"
	"github.com/wu8685/chilling/internal/localserver"
)

var buildVersion = "dev"

type config struct {
	enabled             bool
	workspaceBrowser    bool
	socket, binary, cwd string
}

func parse(args []string) (config, error) {
	if len(args) == 0 || args[0] != "serve" {
		return config{}, errors.New("usage: chilling serve --enable-ariel-file-context --socket PATH --codex-binary PATH")
	}
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var cfg config
	set.BoolVar(&cfg.enabled, "enable-ariel-file-context", false, "enable the local Ariel file-context contract")
	set.BoolVar(&cfg.workspaceBrowser, "enable-ariel-workspace-browser", false, "enable the read-only Ariel workspace browser")
	set.StringVar(&cfg.socket, "socket", "", "absolute Unix socket path")
	set.StringVar(&cfg.binary, "codex-binary", "", "Codex binary providing app-server")
	set.StringVar(&cfg.cwd, "cwd", "", "initial App Server working directory")
	if err := set.Parse(args[1:]); err != nil {
		return config{}, err
	}
	if set.NArg() != 0 || !cfg.enabled || cfg.socket == "" || cfg.binary == "" {
		return config{}, errors.New("file context requires its explicit flag, socket, and Codex binary")
	}
	if cfg.cwd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config{}, err
		}
		cfg.cwd = cwd
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config) error {
	process, err := codexapp.Start(ctx, cfg.binary, cfg.cwd)
	if err != nil {
		return err
	}
	defer process.Close()
	source := codexapp.NewSource(process)
	service := filecontext.NewWithWorkspaceBrowser(source, cfg.workspaceBrowser)
	handler := filecontext.NewHTTPHandler(service, buildVersion, "codex-app-server-public-v2")
	return localserver.Serve(ctx, localserver.Config{Enabled: cfg.enabled, Socket: cfg.socket}, handler)
}

func main() {
	cfg, err := parse(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(fmt.Errorf("chilling file context: %w", err))
	}
}
