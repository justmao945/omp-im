package omp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/justmao945/omp-im/internal/config"
)

// transport implements a minimal JSON-RPC client over the stdio of an ACP agent.
type transport struct {
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

func newTransport(cfg config.AgentConfig, serverReqHandler func(method string, params json.RawMessage) (any, error)) (*transport, error) {
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

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp start %s: %w", cfg.Command, err)
	}

	t := &transport{
		cmd:              cmd,
		stdin:            stdin,
		stdout:           stdout,
		pending:          make(map[int]chan rpcResponse),
		serverReqHandler: serverReqHandler,
	}
	go t.readLoop()
	return t, nil
}

func (t *transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("acp transport closed")
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

func (t *transport) notify(method string, params any) error {
	if t.closed.Load() {
		return fmt.Errorf("acp transport closed")
	}
	return t.write(rpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  mustMarshal(params),
	})
}

func (t *transport) write(msg rpcMessage) error {
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

func (t *transport) readLoop() {
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
			// Server-to-client request/notification
			go func(m rpcMessage) {
				result, err := t.serverReqHandler(m.Method, m.Params)
				if m.ID != 0 {
					resp := rpcMessage{JSONRPC: "2.0", ID: m.ID}
					if err != nil {
						resp.Error = &rpcError{Code: -32603, Message: err.Error()}
					} else {
						resp.Result = mustMarshal(result)
					}
					_ = t.write(resp)
				}
			}(msg)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Error("acp: read loop error", "error", err)
	}
	t.closed.Store(true)
}

func (t *transport) close() error {
	t.closed.Store(true)
	_ = t.stdin.Close()
	return t.cmd.Wait()
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
