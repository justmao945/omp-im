package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

	defaultLongPollTimeout = 35 * time.Second
	defaultAPITimeout      = 15 * time.Second

	maxIlinkHTTPResponseBody = 64 << 20

	channelVersion = "omp-im-weixin/1.0"
)

type apiClient struct {
	baseURL    string
	token      string
	routeTag   string
	httpClient *http.Client
}

func newAPIClient(baseURL, token, routeTag string, httpClient *http.Client) *apiClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/") + "/"
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultAPITimeout}
	}
	return &apiClient{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		routeTag:   strings.TrimSpace(routeTag),
		httpClient: httpClient,
	}
}

func randomWechatUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0000"))
	}
	u := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", u)))
}

func (c *apiClient) longPollClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultLongPollTimeout
	}
	tr := http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = t.Clone()
	}
	return &http.Client{
		Timeout:   timeout + 5*time.Second,
		Transport: tr,
	}
}

func (c *apiClient) post(ctx context.Context, endpoint string, body []byte, timeout time.Duration, label string) ([]byte, error) {
	url := c.baseURL + strings.TrimPrefix(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: new request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.routeTag != "" {
		req.Header.Set("SKRouteTag", c.routeTag)
	}

	client := c.httpClient
	if timeout > 0 {
		client = c.longPollClient(timeout)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: %w", label, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIlinkHTTPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: read body: %w", label, err)
	}
	if len(raw) > maxIlinkHTTPResponseBody {
		return nil, fmt.Errorf("weixin: %s: response body exceeds %d bytes", label, maxIlinkHTTPResponseBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: %s: http %d: %s", label, resp.StatusCode, truncateForLog(raw, 512))
	}
	return raw, nil
}

func truncateForLog(b []byte, max int) string {
	s := string(b)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (c *apiClient) getUpdates(ctx context.Context, buf string, timeoutMs int) (*getUpdatesResp, error) {
	timeout := defaultLongPollTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	req := getUpdatesReq{
		GetUpdatesBuf: buf,
		BaseInfo:      baseInfo{ChannelVersion: channelVersion},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "ilink/bot/getupdates", payload, timeout, "getUpdates")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &getUpdatesResp{Ret: 0, Msgs: nil, GetUpdatesBuf: buf}, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return &getUpdatesResp{Ret: 0, Msgs: nil, GetUpdatesBuf: buf}, nil
		}
		return nil, err
	}
	var out getUpdatesResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("weixin: getUpdates json: %w", err)
	}
	return &out, nil
}

func (c *apiClient) getUploadURL(ctx context.Context, req getUploadURLRequest) (*getUploadURLResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "ilink/bot/getuploadurl", payload, 0, "getUploadURL")
	if err != nil {
		return nil, err
	}
	var resp getUploadURLResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("weixin: getUploadURL: response json: %w: %s", err, truncateForLog(raw, 256))
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("weixin: getUploadURL: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.Errcode, resp.Errmsg)
	}
	return &resp, nil
}

func (c *apiClient) sendMessage(ctx context.Context, msg *sendMessageReq) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	raw, err := c.post(ctx, "ilink/bot/sendmessage", payload, 0, "sendMessage")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var resp sendMessageResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("weixin: sendMessage: response json: %w: %s", err, truncateForLog(raw, 256))
	}
	if resp.Ret != 0 {
		return fmt.Errorf("weixin: sendMessage: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.Errcode, resp.Errmsg)
	}
	return nil
}

func (c *apiClient) fetchLoginQRCode(ctx context.Context, botType string) (*qrCodeResponse, error) {
	endpoint := fmt.Sprintf("%s/ilink/bot/get_bot_qrcode?bot_type=%s", strings.TrimRight(c.baseURL, "/"), url.QueryEscape(botType))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: new request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIlinkHTTPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: read body: %w", err)
	}
	if len(raw) > maxIlinkHTTPResponseBody {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: response body exceeds %d bytes", maxIlinkHTTPResponseBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: http %d: %s", resp.StatusCode, truncateForLog(raw, 512))
	}
	var out qrCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("weixin: fetchLoginQRCode: json: %w", err)
	}
	return &out, nil
}

func (c *apiClient) pollLoginStatus(ctx context.Context, qrcode string) (*qrStatusResponse, error) {
	endpoint := fmt.Sprintf("%s/ilink/bot/get_qrcode_status?qrcode=%s", strings.TrimRight(c.baseURL, "/"), url.QueryEscape(qrcode))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: pollLoginStatus: new request: %w", err)
	}
	req.Header.Set("iLink-App-ClientVersion", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: pollLoginStatus: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIlinkHTTPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("weixin: pollLoginStatus: read body: %w", err)
	}
	if len(raw) > maxIlinkHTTPResponseBody {
		return nil, fmt.Errorf("weixin: pollLoginStatus: response body exceeds %d bytes", maxIlinkHTTPResponseBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: pollLoginStatus: http %d: %s", resp.StatusCode, truncateForLog(raw, 512))
	}
	var out qrStatusResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("weixin: pollLoginStatus: json: %w", err)
	}
	return &out, nil
}

// setToken updates the client's authorization token.
func (c *apiClient) setToken(token string) {
	c.token = strings.TrimSpace(token)
}
