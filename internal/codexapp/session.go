package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type Session struct {
	rw       io.ReadWriteCloser
	reader   *bufio.Scanner
	maxBytes int
	mu       sync.Mutex
	next     int
}

func NewSession(rw io.ReadWriteCloser, maxBytes int) *Session {
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	scanner := bufio.NewScanner(rw)
	scanner.Buffer(make([]byte, 32<<10), maxBytes)
	return &Session{rw: rw, reader: scanner, maxBytes: maxBytes}
}
func (s *Session) Close() error { return s.rw.Close() }

func (s *Session) Call(ctx context.Context, method string, params any, result any) error {
	if method == "" {
		return errors.New("empty method")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	request, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil || len(request) > s.maxBytes {
		return errors.New("invalid request")
	}
	if _, err := s.rw.Write(append(request, '\n')); err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() { finished <- s.readResponse(id, result) }()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		_ = s.rw.Close()
		return ctx.Err()
	}
}

func (s *Session) readResponse(id int, result any) error {
	for s.reader.Scan() {
		var response struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(s.reader.Bytes(), &response) != nil {
			return errors.New("invalid App Server response")
		}
		if response.Method != "" {
			if response.ID != nil {
				rejected, _ := json.Marshal(map[string]any{"id": *response.ID, "error": map[string]any{"code": -32601, "message": "read-only client"}})
				if _, err := s.rw.Write(append(rejected, '\n')); err != nil {
					return err
				}
			}
			continue
		}
		if response.ID == nil || *response.ID != id {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return errors.New("App Server rejected request")
		}
		if result != nil && json.Unmarshal(response.Result, result) != nil {
			return errors.New("invalid App Server result")
		}
		return nil
	}
	if err := s.reader.Err(); err != nil {
		return err
	}
	return io.EOF
}

type pipes struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p pipes) Read(body []byte) (int, error)  { return p.r.Read(body) }
func (p pipes) Write(body []byte) (int, error) { return p.w.Write(body) }
func (p pipes) Close() error                   { _ = p.w.Close(); return p.r.Close() }

type Process struct {
	*Session
	cmd    *exec.Cmd
	exited chan struct{}
}

func Start(ctx context.Context, binary, cwd string) (*Process, error) {
	if binary == "" || cwd == "" {
		return nil, errors.New("Codex binary and cwd are required")
	}
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Dir = cwd
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start configured Codex binary: %w", err)
	}
	process := &Process{Session: NewSession(pipes{r: stdout, w: stdin}, 64<<20), cmd: cmd, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(process.exited) }()
	var initialized any
	if err := process.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "chilling", "title": "Chilling file context", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialized); err != nil {
		_ = process.Close()
		return nil, err
	}
	notification, _ := json.Marshal(map[string]any{"method": "initialized", "params": map[string]any{}})
	process.mu.Lock()
	_, err = process.rw.Write(append(notification, '\n'))
	process.mu.Unlock()
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	return process, nil
}
func (p *Process) Close() error {
	_ = p.Session.Close()
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	return nil
}
