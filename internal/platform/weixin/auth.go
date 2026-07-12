package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	defaultBotType     = "3"
	defaultQRFileName  = "login-qr.png"
	defaultSessionFile = "session.json"
	qrLoginDeadline    = 8 * time.Minute
)

// qrCodeResponse is the initial QR payload from iLink.
type qrCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

// qrStatusResponse reports the current QR login state.
type qrStatusResponse struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	ILinkUserID string `json:"ilink_user_id"`
}

// sessionState persists login credentials and long-poll cursor.
type sessionState struct {
	BotToken      string                 `json:"bot_token"`
	BotID         string                 `json:"bot_id"`
	UserID        string                 `json:"user_id"`
	BaseURL       string                 `json:"base_url"`
	GetUpdatesBuf string                 `json:"get_updates_buf,omitempty"`
	Peers         map[string]sessionPeer `json:"peers,omitempty"`
	SavedAt       string                 `json:"saved_at,omitempty"`
}

type sessionPeer struct {
	ContextToken string `json:"context_token"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
}

func loadSessionState(path string) (*sessionState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state sessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	state.BaseURL = normalizeBaseURL(state.BaseURL)
	if state.Peers == nil {
		state.Peers = make(map[string]sessionPeer)
	}
	return &state, nil
}

func saveSessionState(path string, state *sessionState) error {
	if state == nil {
		return errors.New("state is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	state.BaseURL = normalizeBaseURL(state.BaseURL)
	if state.Peers == nil {
		state.Peers = make(map[string]sessionPeer)
	}
	state.SavedAt = time.Now().Format(time.RFC3339)

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hasUsableSession(path string) bool {
	state, err := loadSessionState(path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(state.BotToken) != ""
}

// performQRLogin fetches a QR code from iLink, renders it as a terminal QR code
// and a PNG file, and polls for scan confirmation.
// Note: iLink returns qrcode_img_content as a URL string; the actual QR code
// should encode this URL, and qrcode is only used for polling status.
func performQRLogin(ctx context.Context, client *apiClient, stateDir string) (*sessionState, error) {
	qrResp, err := client.fetchLoginQRCode(ctx, defaultBotType)
	if err != nil {
		return nil, fmt.Errorf("weixin: fetch QR code: %w", err)
	}
	if qrResp.QRCode == "" {
		return nil, errors.New("weixin: QR code content is empty")
	}
	qrUrl := strings.TrimSpace(qrResp.QRCodeImgContent)
	if qrUrl == "" {
		return nil, errors.New("weixin: QR code URL is empty")
	}

	qrFile := filepath.Join(stateDir, defaultQRFileName)
	if err := renderQRCodeImage(qrUrl, qrFile); err != nil {
		slog.Warn("weixin: failed to save QR code image", "error", err)
	}

	fmt.Printf("\n=================================================\n")
	fmt.Printf("请用微信扫描下方二维码登录：\n")
	fmt.Printf("备用链接: %s\n", qrUrl)
	fmt.Printf("图片文件: %s\n", qrFile)
	fmt.Printf("=================================================\n\n")

	qr, err := qrcode.New(qrUrl, qrcode.Low)
	if err == nil {
		fmt.Println(qr.ToSmallString(false))
		fmt.Println()
	}

	slog.Info("weixin: waiting for QR code scan", "deadline", qrLoginDeadline)

	status, err := waitForQRLogin(ctx, client, qrResp.QRCode)
	if err != nil {
		return nil, err
	}

	state := &sessionState{
		BotToken: status.BotToken,
		BotID:    status.ILinkBotID,
		UserID:   status.ILinkUserID,
		BaseURL:  normalizeBaseURL(status.BaseURL),
		Peers:    make(map[string]sessionPeer),
	}
	slog.Info("weixin: login successful", "bot_id", state.BotID, "user_id", state.UserID)
	return state, nil
}

func waitForQRLogin(ctx context.Context, client *apiClient, qrcodeContent string) (*qrStatusResponse, error) {
	deadline := time.Now().Add(qrLoginDeadline)

	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, defaultLongPollTimeout)
		status, err := client.pollLoginStatus(pollCtx, qrcodeContent)
		cancel()
		if err != nil {
			if isTimeoutError(err) {
				continue
			}
			return nil, fmt.Errorf("weixin: poll login status: %w", err)
		}

		switch status.Status {
		case "wait":
		case "scaned":
			slog.Info("weixin: QR code scanned, please confirm on phone")
		case "confirmed":
			if status.ILinkBotID == "" || status.BotToken == "" {
				return nil, errors.New("weixin: login confirmed but token or bot id missing")
			}
			if strings.TrimSpace(status.BaseURL) == "" {
				status.BaseURL = defaultBaseURL
			}
			return status, nil
		case "expired":
			return nil, errors.New("weixin: QR code expired, please restart")
		default:
			slog.Info("weixin: QR login status updated", "status", status.Status)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, errors.New("weixin: QR login timed out")
}

func renderQRCodeImage(content, path string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("empty QR content")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return qrcode.WriteFile(content, qrcode.Low, 256, path)
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(baseURL, "/")
}

// isTimeoutError treats HTTP context deadlines as normal long-poll timeouts.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded")
}
