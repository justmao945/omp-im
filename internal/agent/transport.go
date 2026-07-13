package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Transport implements a minimal JSON-RPC client over the stdio of an ACP agent.
type Transport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu               sync.Mutex
	pending          map[int]chan rpcResponse
	nextID           atomic.Int32
	closed           atomic.Bool
	serverReqHandler func(method string, params json.RawMessage) (any, error)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage
	Error  error
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("acp rpc error %d: %s", e.Code, e.Message)
}

func NewTransport(cfg Config, serverReqHandler func(method string, params json.RawMessage) (any, error)) (*Transport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.WorkDir
	if cmd.Dir == "" {
		cmd.Dir, _ = exec.LookPath(".")
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp stdout pipe: %w", err)
	}
	cmd.Stderr = nil // let stderr go to parent for now

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) && cfg.InstallHint != "" {
			return nil, fmt.Errorf("acp agent command %q not found; %s", cfg.Command, cfg.InstallHint)
		}
		return nil, fmt.Errorf("acp start %s: %w", cfg.Command, err)
	}

	t := &Transport{
		cmd:              cmd,
		stdin:            stdin,
		stdout:           stdout,
		pending:          make(map[int]chan rpcResponse),
		serverReqHandler: serverReqHandler,
	}
	go t.readLoop()
	return t, nil
}

func (t *Transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("acp Transport closed")
	}

	id := int(t.nextID.Add(1))
	req := rpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  mustMarshal(params),
	}

	respCh := make(chan rpcResponse, 1)
	t.mu.Lock()
	t.pending[id] = respCh
	t.mu.Unlock()

	if err := t.write(req); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-respCh:
		return resp.Result, resp.Error
	}
}

func (t *Transport) notify(method string, params any) error {
	if t.closed.Load() {
		return fmt.Errorf("acp Transport closed")
	}
	return t.write(rpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  mustMarshal(params),
	})
}

func (t *Transport) write(msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	t.mu.Lock()
	defer t.mu.Unlock()
	_, err = t.stdin.Write(data)
	return err
}

func (t *Transport) readLoop() {
	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			slog.Debug("acp: skip non-json line", "line", string(line))
			continue
		}
		if msg.ID != 0 {
			// Response to a client request
			t.mu.Lock()
			ch, ok := t.pending[msg.ID]
			delete(t.pending, msg.ID)
			t.mu.Unlock()
			if ok {
				var err error
				if msg.Error != nil {
					err = msg.Error
				}
				ch <- rpcResponse{Result: msg.Result, Error: err}
			}
			continue
		}
		if msg.Method != "" && t.serverReqHandler != nil {
			// Server-to-client request/notification. Handle sequentially to keep
			// session/update notifications (and therefore textParts) in order.
			slog.Debug("acp: server request", "method", msg.Method)
			result, err := t.serverReqHandler(msg.Method, msg.Params)
			if msg.ID != 0 {
				resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
				if err != nil {
					resp.Error = &rpcError{Code: -32603, Message: err.Error()}
				} else {
					resp.Result = mustMarshal(result)
				}
				_ = t.write(resp)
			}
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Error("acp: read loop error", "error", err)
	}
	t.closed.Store(true)
}

// Close terminates the ACP process and unblocks pending requests.
func (t *Transport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = t.stdin.Close()

	// Close any pending call so callers are not blocked forever.
	t.mu.Lock()
	pendingCalls := make([]chan rpcResponse, 0, len(t.pending))
	for _, ch := range t.pending {
		pendingCalls = append(pendingCalls, ch)
	}
	t.pending = make(map[int]chan rpcResponse)
	t.mu.Unlock()
	for _, ch := range pendingCalls {
		ch <- rpcResponse{Error: fmt.Errorf("acp Transport closed")}
	}

	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}

	// Graceful shutdown: close stdin, then terminate the whole process group.
	pid := t.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = t.cmd.Wait()
	}
	return nil
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
