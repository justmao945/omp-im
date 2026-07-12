package weixin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/justmao945/omp-im/internal/core"
)

const (
	sessionKeyPrefix = "weixin:"
	maxWeixinChunk   = 3800

	weixinSendMaxRetries = 3
	weixinSendRetryDelay = 500 * time.Millisecond
	weixinChunkSendDelay = 100 * time.Millisecond
)

type replyContext struct {
	peerUserID   string
	contextToken string
}

// Platform implements core.Platform for Weixin personal chat via the ilink bot HTTP API.
// It supports login via QR code (default) or a pre-configured Bearer token.
type Platform struct {
	baseURL    string
	cdnBaseURL string
	allowFrom  string
	routeTag   string
	stateDir   string

	api           *apiClient
	httpClient    *http.Client
	cdnHTTPClient *http.Client

	mu       sync.RWMutex
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool

	sessionMu   sync.RWMutex
	session     *sessionState
	sessionPath string

	dedupMu sync.Mutex
	dedup   map[string]time.Time

	pauseMu    sync.Mutex
	pauseUntil time.Time
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '\x00':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func defaultDataDir() string {
	if d := os.Getenv("OMP_IM_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".omp-im")
	}
	return filepath.Join(home, ".omp-im")
}

// New constructs a Weixin platform.
// If options.token is provided, it is used directly. Otherwise the platform loads
// a saved session from ~/.omp-im/weixin/<account>/session.json. If no session
// exists, New returns an error and instructs the user to run `weixin login`.
// Optional: base_url, allow_from, route_tag, account_id, proxy, token.
func New(opts map[string]any) (*Platform, error) {
	token, _ := opts["token"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	checkAllowFrom("weixin", allowFrom)

	baseURL, _ := opts["base_url"].(string)
	cdnBaseURL, _ := opts["cdn_base_url"].(string)
	if strings.TrimSpace(cdnBaseURL) == "" {
		cdnBaseURL = defaultCDNBaseURL
	}
	cdnBaseURL = strings.TrimRight(strings.TrimSpace(cdnBaseURL), "/")
	routeTag, _ := opts["route_tag"].(string)
	accountLabel, _ := opts["account_id"].(string)
	if accountLabel == "" {
		accountLabel = "default"
	}

	stateDir := filepath.Join(defaultDataDir(), "weixin", sanitizePathSegment(accountLabel))

	httpClient := &http.Client{Timeout: defaultAPITimeout}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("weixin: invalid proxy URL %q: %w", proxyURL, err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("weixin: using proxy", "proxy", u.Redacted())
	}

	cdnHTTPClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}

	p := &Platform{
		baseURL:       baseURL,
		cdnBaseURL:    cdnBaseURL,
		allowFrom:     allowFrom,
		routeTag:      routeTag,
		stateDir:      stateDir,
		dedup:         make(map[string]time.Time),
		cdnHTTPClient: cdnHTTPClient,
	}
	p.httpClient = httpClient
	p.api = newAPIClient(baseURL, "", routeTag, httpClient)

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("weixin: create state dir: %w", err)
	}
	p.sessionPath = filepath.Join(stateDir, defaultSessionFile)

	if strings.TrimSpace(token) != "" {
		p.api.setToken(token)
		p.session = &sessionState{BotToken: token, BaseURL: normalizeBaseURL(baseURL), Peers: make(map[string]sessionPeer)}
		return p, nil
	}

	state, err := p.loadSession()
	if err != nil {
		return nil, fmt.Errorf("weixin: no saved session in %s; run `weixin login` first", p.sessionPath)
	}
	p.session = state
	p.api.setToken(state.BotToken)
	if state.BaseURL != "" {
		p.api.baseURL = normalizeBaseURL(state.BaseURL) + "/"
	}

	return p, nil
}

// Login performs QR-code login for Weixin and persists the session to disk.
// Optional: base_url, route_tag, account_id, proxy.
func Login(ctx context.Context, opts map[string]any) error {
	baseURL, _ := opts["base_url"].(string)
	routeTag, _ := opts["route_tag"].(string)
	accountLabel, _ := opts["account_id"].(string)
	if accountLabel == "" {
		accountLabel = "default"
	}
	stateDir := filepath.Join(defaultDataDir(), "weixin", sanitizePathSegment(accountLabel))

	httpClient := &http.Client{Timeout: defaultAPITimeout}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return fmt.Errorf("weixin: invalid proxy URL %q: %w", proxyURL, err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("weixin: using proxy", "proxy", u.Redacted())
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("weixin: create state dir: %w", err)
	}

	api := newAPIClient(baseURL, "", routeTag, httpClient)
	state, err := performQRLogin(ctx, api, stateDir)
	if err != nil {
		return err
	}

	statePath := filepath.Join(stateDir, defaultSessionFile)
	if err := saveSessionState(statePath, state); err != nil {
		return fmt.Errorf("weixin: save session: %w", err)
	}
	slog.Info("weixin: session saved", "path", statePath)
	return nil
}

// Logout removes the saved Weixin session for the given account.
func Logout(opts map[string]any) error {
	accountLabel, _ := opts["account_id"].(string)
	if accountLabel == "" {
		accountLabel = "default"
	}
	stateDir := filepath.Join(defaultDataDir(), "weixin", sanitizePathSegment(accountLabel))
	statePath := filepath.Join(stateDir, defaultSessionFile)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("weixin: remove session: %w", err)
	}
	slog.Info("weixin: logged out", "account", accountLabel)
	return nil
}

func (p *Platform) Name() string { return "weixin" }

func (p *Platform) loadSession() (*sessionState, error) {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	if p.session != nil {
		return p.session, nil
	}
	return loadSessionState(p.sessionPath)
}

func (p *Platform) persistSession() {
	p.sessionMu.RLock()
	state := p.session
	p.sessionMu.RUnlock()
	if state == nil {
		return
	}
	if err := saveSessionState(p.sessionPath, state); err != nil {
		slog.Warn("weixin: save session failed", "path", p.sessionPath, "error", err)
	}
}

func (p *Platform) syncBuf() string {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	if p.session == nil {
		return ""
	}
	return p.session.GetUpdatesBuf
}

func (p *Platform) persistSyncBuf(buf string) {
	p.sessionMu.Lock()
	if p.session == nil {
		p.session = &sessionState{BaseURL: defaultBaseURL, Peers: make(map[string]sessionPeer)}
	}
	p.session.GetUpdatesBuf = buf
	p.sessionMu.Unlock()
	p.persistSession()
}

func (p *Platform) setContextToken(peer, tok string) {
	if peer == "" || tok == "" {
		return
	}
	p.sessionMu.Lock()
	if p.session == nil {
		p.session = &sessionState{BaseURL: defaultBaseURL, Peers: make(map[string]sessionPeer)}
	}
	if p.session.Peers == nil {
		p.session.Peers = make(map[string]sessionPeer)
	}
	peerEntry := p.session.Peers[peer]
	peerEntry.ContextToken = tok
	peerEntry.LastSeenAt = time.Now().Format(time.RFC3339)
	p.session.Peers[peer] = peerEntry
	p.sessionMu.Unlock()
	p.persistSession()
}

func (p *Platform) getContextToken(peer string) string {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	if p.session == nil {
		return ""
	}
	return p.session.Peers[peer].ContextToken
}

func (p *Platform) isPaused() bool {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()
	if p.pauseUntil.IsZero() || time.Now().After(p.pauseUntil) {
		p.pauseUntil = time.Time{}
		return false
	}
	return true
}

func (p *Platform) pauseSession(d time.Duration) {
	if d <= 0 {
		d = time.Hour
	}
	p.pauseMu.Lock()
	p.pauseUntil = time.Now().Add(d)
	p.pauseMu.Unlock()
	slog.Warn("weixin: session paused after gateway error", "duration", d)
}

// Start begins the long-poll loop and blocks until Stop is called.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return fmt.Errorf("weixin: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()
	go p.pollLoop(ctx)
	<-ctx.Done()
	return nil
}

// Stop halts the long-poll loop.
func (p *Platform) Stop() error {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.stopping = true
	p.mu.Unlock()
	return nil
}

func (p *Platform) pollLoop(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if p.isPaused() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		buf := p.syncBuf()

		resp, err := p.api.getUpdates(ctx, buf, 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("weixin: getUpdates failed", "error", err, "backoff", backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		backoff = time.Second

		if resp.Errcode == sessionExpiredErrcode {
			p.pauseSession(time.Hour)
			continue
		}
		if resp.Ret != 0 && resp.Errmsg != "" {
			slog.Warn("weixin: getUpdates ret", "ret", resp.Ret, "errcode", resp.Errcode, "errmsg", resp.Errmsg)
		}

		p.mu.RLock()
		h := p.handler
		p.mu.RUnlock()
		if h == nil {
			continue
		}

		var wg sync.WaitGroup
		for i := range resp.Msgs {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.dispatchInbound(ctx, &resp.Msgs[i], h)
			}()
		}
		wg.Wait()

		if ctx.Err() == nil && resp.GetUpdatesBuf != "" {
			p.persistSyncBuf(resp.GetUpdatesBuf)
		}
	}
}

func (p *Platform) dispatchInbound(ctx context.Context, m *weixinMessage, h core.MessageHandler) {
	if m == nil {
		return
	}
	if m.MessageType == messageTypeBot {
		return
	}
	if m.MessageType != 0 && m.MessageType != messageTypeUser {
		return
	}
	from := strings.TrimSpace(m.FromUserID)
	if from == "" {
		return
	}
	if !allowList(p.allowFrom, from) {
		slog.Debug("weixin: sender not in allow_from", "from", from)
		return
	}

	body := bodyFromItemList(m.ItemList)
	images := p.collectInboundImages(ctx, m.ItemList)
	if strings.TrimSpace(body) == "" && len(images) == 0 {
		return
	}
	if strings.TrimSpace(body) == "" && len(images) > 0 {
		body = "[图片]"
	}

	if tok := strings.TrimSpace(m.ContextToken); tok != "" {
		p.setContextToken(from, tok)
	}

	dedupKey := fmt.Sprintf("%s|%d|%d|%s", from, m.MessageID, m.Seq, strings.TrimSpace(m.ClientID))
	p.dedupMu.Lock()
	now := time.Now()
	for k, ts := range p.dedup {
		if now.Sub(ts) > 5*time.Minute {
			delete(p.dedup, k)
		}
	}
	if _, ok := p.dedup[dedupKey]; ok {
		p.dedupMu.Unlock()
		return
	}
	p.dedup[dedupKey] = now
	p.dedupMu.Unlock()

	rc := &replyContext{peerUserID: from, contextToken: strings.TrimSpace(m.ContextToken)}
	msgID := fmt.Sprintf("%d", m.MessageID)
	if m.MessageID == 0 {
		msgID = randomHex(8)
	}

	h(p, &core.Message{
		SessionKey: sessionKeyPrefix + from,
		Platform:   p.Name(),
		MessageID:  msgID,
		ChannelID:  from,
		UserID:     from,
		Content:    body,
		Images:     images,
		ReplyCtx:   rc,
	})
}

func bodyFromItemList(items []messageItem) string {
	var parts []string
	for _, it := range items {
		if it.Type == messageItemText && it.TextItem != nil {
			parts = append(parts, it.TextItem.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Reply sends text back to the user.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("weixin: invalid reply context")
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		rc.contextToken = p.getContextToken(rc.peerUserID)
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		return fmt.Errorf("weixin: missing context_token for peer %q", rc.peerUserID)
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}

	chunks := splitUTF8(content, maxWeixinChunk)
	total := len(chunks)
	for i, chunk := range chunks {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(weixinChunkSendDelay):
			}
		}
		if err := p.sendChunkWithRetry(ctx, rc, chunk); err != nil {
			return fmt.Errorf("weixin: send chunk %d/%d: %w", i+1, total, err)
		}
	}
	return nil
}

func (p *Platform) sendChunkWithRetry(ctx context.Context, rc *replyContext, chunk string) error {
	var lastErr error
	for attempt := 0; attempt < weixinSendMaxRetries; attempt++ {
		clientID := "omp-" + randomHex(6)
		msg := sendMessageReq{
			Msg: weixinOutboundMsg{
				ToUserID:     rc.peerUserID,
				ClientID:     clientID,
				MessageType:  messageTypeBot,
				MessageState: messageStateFinish,
				ItemList:     []messageItem{{Type: messageItemText, TextItem: &textItem{Text: chunk}}},
				ContextToken: rc.contextToken,
			},
		}
		err := p.api.sendMessage(ctx, &msg)
		if err == nil {
			return nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "ret=-2") {
			freshToken := p.getContextToken(rc.peerUserID)
			if freshToken == "" || freshToken == rc.contextToken {
				return fmt.Errorf("weixin: sendMessage ret=-2 (expired context_token): %w", lastErr)
			}
			rc.contextToken = freshToken
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(weixinSendRetryDelay):
			}
			continue
		}
		return err
	}
	return lastErr
}

func splitUTF8(s string, maxRunes int) []string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		n := maxRunes
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// checkAllowFrom mirrors core.CheckAllowFrom without importing the full core package text.
func checkAllowFrom(platform, allowFrom string) {
	if strings.TrimSpace(allowFrom) == "" {
		slog.Warn(platform+": allow_from is empty, allowing all senders", "platform", platform)
	}
}

// allowList mirrors core.AllowList.
func allowList(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" || allowFrom == "*" {
		return true
	}
	for _, p := range strings.Split(allowFrom, ",") {
		if strings.EqualFold(strings.TrimSpace(p), userID) {
			return true
		}
	}
	return false
}
