package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsCmdSubscribe = "aibot_subscribe"
	wsCmdPing      = "ping"

	defaultHeartbeatInterval = 30 * time.Second
	defaultReconnectDelay    = 5 * time.Second
	maxReconnectAttempts     = 10

	wsAckTimeout       = 10 * time.Second
	wsMediaAckTimeout  = 30 * time.Second
	wsUploadChunkSize  = 512 << 10
	wsUploadMaxChunks  = 100
	wsUploadMaxBytes   = wsUploadChunkSize * wsUploadMaxChunks // 50 MB
)

// errWSAckTimeout is returned when the server does not acknowledge a frame in time.
var errWSAckTimeout = errors.New("wecom: ack timeout")

// wsAckResult carries a server response frame or an error for a pending request.
type wsAckResult struct {
	frame wsFrame
	err   error
}

// wsClient manages a WebSocket connection to the WeCom AI bot gateway.
type wsClient struct {
	cfg         *config
	recvHandler func(*wsFrame)

	conn     *websocket.Conn
	connMu   sync.RWMutex
	writeMu  sync.Mutex
	stopCh   chan struct{}

	pendingMu   sync.Mutex
	pendingAcks map[string]chan wsAckResult

	// sendFn is used in tests to stub outbound sends.
	sendFn func(map[string]interface{}) error
}

func newWSClient(cfg *config, recvHandler func(*wsFrame)) *wsClient {
	return &wsClient{
		cfg:         cfg,
		recvHandler: recvHandler,
		stopCh:      make(chan struct{}),
		pendingAcks: make(map[string]chan wsAckResult),
	}
}

// run connects and reconnects until Stop is called.
func (c *wsClient) run(ctx context.Context) error {
	reconnectDelay := defaultReconnectDelay
	for attempt := 0; ; attempt++ {
		select {
		case <-c.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		slog.Info("wecom: connecting to websocket", "url", c.cfg.websocketURL, "attempt", attempt+1)
		start := time.Now()
		if err := c.connectAndServe(ctx); err != nil {
			slog.Error("wecom: websocket connection error", "error", err, "attempt", attempt+1)
		}
		if time.Since(start) > 30*time.Second {
			reconnectDelay = defaultReconnectDelay
		}

		if attempt >= maxReconnectAttempts {
			attempt = maxReconnectAttempts - 1
		}

		select {
		case <-c.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
			if reconnectDelay < 60*time.Second {
				reconnectDelay += 5 * time.Second
			}
		}
	}
}

func (c *wsClient) connectAndServe(ctx context.Context) error {
	u, err := url.Parse(c.cfg.websocketURL)
	if err != nil {
		return fmt.Errorf("parse websocket url: %w", err)
	}
	if u.Scheme == "" {
		u.Scheme = "wss"
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	if err := c.subscribe(conn); err != nil {
		conn.Close()
		c.clearConn()
		return fmt.Errorf("subscribe: %w", err)
	}

	connDone := make(chan struct{})
	var localWg sync.WaitGroup
	localWg.Add(2)
	go func() {
		defer localWg.Done()
		c.readLoop(conn, connDone)
	}()
	go func() {
		defer localWg.Done()
		c.heartbeatLoop(conn, connDone)
	}()

	select {
	case <-c.stopCh:
		conn.Close()
		localWg.Wait()
		c.clearConn()
		c.failAllAcksLocked(fmt.Errorf("wecom: connection stopped"))
		return nil
	case <-connDone:
		localWg.Wait()
		c.clearConn()
		c.failAllAcksLocked(fmt.Errorf("wecom: connection closed"))
		return fmt.Errorf("connection closed")
	}
}

func (c *wsClient) subscribe(conn *websocket.Conn) error {
	frame := map[string]interface{}{
		"cmd": wsCmdSubscribe,
		"headers": map[string]string{
			"req_id": generateReqID(),
		},
		"body": map[string]string{
			"bot_id": c.cfg.botID,
			"secret": c.cfg.secret,
		},
	}
	if err := c.writeJSON(conn, frame); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var resp wsFrame
	if err := conn.ReadJSON(&resp); err != nil {
		return fmt.Errorf("read subscribe response: %w", err)
	}
	if !resp.isSuccess() {
		return fmt.Errorf("subscribe failed: %d %s", resp.ErrCode, resp.ErrMsg)
	}
	slog.Info("wecom: subscribed", "bot_id", c.cfg.botID)
	return nil
}

func (c *wsClient) readLoop(conn *websocket.Conn, done chan<- struct{}) {
	defer close(done)

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		var frame wsFrame
		if err := conn.ReadJSON(&frame); err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}
			lvl := slog.LevelWarn
			if websocket.IsUnexpectedCloseError(err) {
				lvl = slog.LevelDebug
			}
			slog.Log(nil, lvl, "wecom: read websocket error, reconnecting", "error", err)
			return
		}

		reqID, hasReqID := frame.Headers["req_id"]
		if hasReqID && c.dispatchAck(reqID, wsAckResult{frame: frame, err: nil}) {
			continue
		}

		if c.recvHandler != nil {
			c.recvHandler(&frame)
		}
	}
}

func (c *wsClient) heartbeatLoop(conn *websocket.Conn, done <-chan struct{}) {
	defer func() {
		conn.Close()
	}()

	ticker := time.NewTicker(defaultHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-done:
			return
		case <-ticker.C:
			if err := c.ping(conn); err != nil {
				slog.Debug("wecom: ping failed", "error", err)
				return
			}
		}
	}
}

func (c *wsClient) ping(conn *websocket.Conn) error {
	frame := map[string]interface{}{
		"cmd": wsCmdPing,
		"headers": map[string]string{
			"req_id": generateReqID(),
		},
	}
	return c.writeJSON(conn, frame)
}

// send sends a message frame via the current connection without waiting for an ack.
func (c *wsClient) send(payload map[string]interface{}) error {
	if c.sendFn != nil {
		return c.sendFn(payload)
	}
	conn := c.currentConn()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.writeJSON(conn, payload)
}

// writeJSON sends a JSON message over the WebSocket connection with mutex protection.
func (c *wsClient) writeJSON(conn *websocket.Conn, v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// writeAndWaitAck sends a frame and waits for a server ack, returning any ack error.
func (c *wsClient) writeAndWaitAck(ctx context.Context, frame map[string]interface{}, reqID string) error {
	result, err := c.writeAndWaitResult(ctx, frame, reqID, wsAckTimeout)
	if errors.Is(err, errWSAckTimeout) {
		slog.Warn("wecom: ack timeout, proceeding", "req_id", reqID)
		return nil
	}
	if err != nil {
		return err
	}
	if result.err != nil {
		slog.Warn("wecom: ack returned error", "req_id", reqID, "errcode", result.frame.ErrCode, "errmsg", result.frame.ErrMsg)
	}
	return result.err
}

// writeAndWaitFrameWithTimeout sends a frame and waits for the response body.
func (c *wsClient) writeAndWaitFrameWithTimeout(ctx context.Context, frame map[string]interface{}, reqID string, timeout time.Duration) (wsFrame, error) {
	result, err := c.writeAndWaitResult(ctx, frame, reqID, timeout)
	if errors.Is(err, errWSAckTimeout) {
		return wsFrame{}, fmt.Errorf("wecom: ack timeout waiting for %s", reqID)
	}
	if err != nil {
		return wsFrame{}, err
	}
	if result.err != nil {
		return wsFrame{}, result.err
	}
	return result.frame, nil
}

// writeAndWaitResult sends a frame and waits for the server to respond with the same req_id.
func (c *wsClient) writeAndWaitResult(ctx context.Context, frame map[string]interface{}, reqID string, timeout time.Duration) (wsAckResult, error) {
	if c.sendFn != nil {
		// Test mode: sendFn stubs the outbound send. No ack is expected;
		// return immediately so tests using sendFn are not blocked.
		return wsAckResult{}, c.sendFn(frame)
	}

	ch := make(chan wsAckResult, 1)
	c.pendingMu.Lock()
	c.pendingAcks[reqID] = ch
	c.pendingMu.Unlock()

	conn := c.currentConn()
	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pendingAcks, reqID)
		c.pendingMu.Unlock()
		return wsAckResult{}, fmt.Errorf("wecom: not connected")
	}
	if err := c.writeJSON(conn, frame); err != nil {
		c.pendingMu.Lock()
		delete(c.pendingAcks, reqID)
		c.pendingMu.Unlock()
		return wsAckResult{}, err
	}

	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pendingAcks, reqID)
		c.pendingMu.Unlock()
		return wsAckResult{}, ctx.Err()
	case <-time.After(timeout):
		c.pendingMu.Lock()
		delete(c.pendingAcks, reqID)
		c.pendingMu.Unlock()
		return wsAckResult{}, errWSAckTimeout
	}
}

// dispatchAck routes a server response frame to a pending request channel.
func (c *wsClient) dispatchAck(reqID string, result wsAckResult) bool {
	c.pendingMu.Lock()
	ch, ok := c.pendingAcks[reqID]
	if ok {
		delete(c.pendingAcks, reqID)
	}
	c.pendingMu.Unlock()
	if !ok {
		return false
	}

	// Server response frames have no cmd and carry errcode/errmsg in the body for some commands.
	if result.err == nil && !result.frame.isSuccess() {
		result.err = fmt.Errorf("wecom: ack error: errcode=%d errmsg=%s", result.frame.ErrCode, result.frame.ErrMsg)
	}
	ch <- result
	return true
}

// failAllAcksLocked fails all pending acks. Caller must hold no locks that c.pendingMu depends on.
func (c *wsClient) failAllAcksLocked(err error) {
	c.pendingMu.Lock()
	pending := make(map[string]chan wsAckResult, len(c.pendingAcks))
	for k, v := range c.pendingAcks {
		pending[k] = v
	}
	c.pendingAcks = make(map[string]chan wsAckResult)
	c.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- wsAckResult{err: err}
	}
}

func (c *wsClient) currentConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

func (c *wsClient) clearConn() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.conn = nil
}

// stop closes the websocket connection and signals the run loop to exit.
func (c *wsClient) stop() error {
	close(c.stopCh)
	if conn := c.currentConn(); conn != nil {
		conn.Close()
	}
	c.failAllAcksLocked(fmt.Errorf("wecom: stopped"))
	return nil
}

var reqIDCounter uint64

func generateReqID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&reqIDCounter, 1))
}
