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
	"strings"
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
	hasID   bool
}

// ACP emits server requests with id: 0. Keep field presence separate from the
// numeric value so id: 0 is neither dropped nor mistaken for a notification.
func (m rpcMessage) MarshalJSON() ([]byte, error) {
	type wireMessage struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *int            `json:"id,omitempty"`
		Method  string          `json:"method,omitempty"`
		Params  json.RawMessage `json:"params,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpcError       `json:"error,omitempty"`
	}
	var id *int
	if m.hasID || m.ID != 0 {
		id = &m.ID
	}
	return json.Marshal(wireMessage{
		JSONRPC: m.JSONRPC,
		ID:      id,
		Method:  m.Method,
		Params:  m.Params,
		Result:  m.Result,
		Error:   m.Error,
	})
}

func (m *rpcMessage) UnmarshalJSON(data []byte) error {
	type wireMessage struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *int            `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	var wire wireMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.JSONRPC = wire.JSONRPC
	m.Method = wire.Method
	m.Params = wire.Params
	m.Result = wire.Result
	m.Error = wire.Error
	m.hasID = wire.ID != nil
	if wire.ID != nil {
		m.ID = *wire.ID
	}
	return nil
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

	slog.Debug("acp: outbound RPC", "id", msg.ID, "method", msg.Method, "params", redactRPCPayload(msg.Params), "result", redactRPCPayload(msg.Result), "error", msg.Error)

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
		slog.Debug("acp: inbound RPC", "id", msg.ID, "method", msg.Method, "params", redactRPCPayload(msg.Params), "result", redactRPCPayload(msg.Result), "error", msg.Error)
		if msg.Method != "" {
			// Server-to-client request or notification (has Method field).
			// Must be checked before the ID branch below, because server
			// requests carry both a Method and an ID; if we fell into the
			// ID branch first we would silently discard them, and the
			// pending-channel lookup would fail (the server sent this ID,
			// not us), so the permission response would never be written.
			if t.serverReqHandler != nil {
				slog.Debug("acp: server request", "method", msg.Method)
				result, err := t.serverReqHandler(msg.Method, msg.Params)
				if msg.hasID {
					resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID, hasID: true}
					if err != nil {
						resp.Error = &rpcError{Code: -32603, Message: err.Error()}
					} else {
						resp.Result = mustMarshal(result)
					}
					_ = t.write(resp)
				}
			}
			continue
		}
		if msg.hasID {
			// Response to a client request (has ID but no Method).
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

func redactRPCPayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return redactRPCValue(value)
}

func redactRPCValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for key, child := range value {
			if isSensitiveRPCKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = redactRPCValue(child)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for i, child := range value {
			redacted[i] = redactRPCValue(child)
		}
		return redacted
	default:
		return value
	}
}

func isSensitiveRPCKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") ||
		strings.Contains(key, "apikey") ||
		strings.Contains(key, "authorization") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "cookie") ||
		strings.Contains(key, "password")
}
