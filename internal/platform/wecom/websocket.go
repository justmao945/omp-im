package wecom

import (
	"context"
	"encoding/json"
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
)

// wsClient manages a WebSocket connection to the WeCom AI bot gateway.
type wsClient struct {
	cfg        *config
	recvHandler func(*wsFrame)

	conn      *websocket.Conn
	connMu    sync.RWMutex
	stopCh    chan struct{}
	wg        sync.WaitGroup
	connected bool

	// sendFn is used in tests to stub outbound sends.
	sendFn func(map[string]interface{}) error
}

func newWSClient(cfg *config, recvHandler func(*wsFrame)) *wsClient {
	return &wsClient{
		cfg:         cfg,
		recvHandler: recvHandler,
		stopCh:      make(chan struct{}),
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
		if err := c.connectAndServe(ctx); err != nil {
			slog.Error("wecom: websocket connection error", "error", err, "attempt", attempt+1)
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
	c.connected = true
	c.connMu.Unlock()

	if err := c.subscribe(conn); err != nil {
		conn.Close()
		c.clearConn()
		return fmt.Errorf("subscribe: %w", err)
	}

	c.wg.Add(2)
	go c.readLoop()
	go c.heartbeatLoop()
	return nil
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

func (c *wsClient) readLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		conn := c.currentConn()
		if conn == nil {
			return
		}

		var frame wsFrame
		if err := conn.ReadJSON(&frame); err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}
			slog.Error("wecom: read websocket error", "error", err)
			return
		}

		if c.recvHandler != nil {
			c.recvHandler(&frame)
		}
	}
}

func (c *wsClient) heartbeatLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(defaultHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			if err := c.ping(); err != nil {
				slog.Debug("wecom: ping failed", "error", err)
			}
		}
	}
}

func (c *wsClient) ping() error {
	conn := c.currentConn()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	frame := map[string]interface{}{
		"cmd": wsCmdPing,
		"headers": map[string]string{
			"req_id": generateReqID(),
		},
	}
	return c.writeJSON(conn, frame)
}

// send sends a message frame via the current connection.
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

func (c *wsClient) writeJSON(conn *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
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
	c.connected = false
}

// stop closes the websocket connection and waits for goroutines to finish.
func (c *wsClient) stop() error {
	close(c.stopCh)
	if conn := c.currentConn(); conn != nil {
		conn.Close()
	}
	c.wg.Wait()
	c.clearConn()
	return nil
}

var reqIDCounter uint64

func generateReqID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&reqIDCounter, 1))
}
