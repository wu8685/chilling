package localserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var ErrFeatureDisabled = errors.New("FEATURE_DISABLED")

type Config struct {
	Enabled bool
	Socket  string
}

func Listen(path string) (net.Listener, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("absolute socket path required")
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("socket directory must be private to the current user")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("socket path exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func Serve(ctx context.Context, cfg Config, handler http.Handler) error {
	if !cfg.Enabled {
		return ErrFeatureDisabled
	}
	listener, err := Listen(cfg.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close(); _ = os.Remove(cfg.Socket) }()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
