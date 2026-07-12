package weixin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func formatAesKeyForAPI(key []byte) string {
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
}

func isWeixinCDNHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".weixin.qq.com") || strings.HasSuffix(host, ".wechat.com")
}

type cdnUploadedRef struct {
	downloadParam string
	aesKey        []byte
	cipherSize    int
	rawSize       int
}

func (p *Platform) resolveReplyContext(replyCtx any) (*replyContext, error) {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return nil, fmt.Errorf("weixin: invalid reply context")
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		rc.contextToken = p.getContextToken(rc.peerUserID)
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		return nil, fmt.Errorf("weixin: missing context_token for peer %q", rc.peerUserID)
	}
	return rc, nil
}

func (p *Platform) uploadToWeixinCDN(ctx context.Context, to string, plaintext []byte, mediaType int, label string) (*cdnUploadedRef, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("weixin: %s: empty payload", label)
	}
	if strings.TrimSpace(p.cdnBaseURL) == "" {
		return nil, fmt.Errorf("weixin: cdn_base_url is empty")
	}
	rawSize := len(plaintext)
	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, fmt.Errorf("weixin: %s: aes key: %w", label, err)
	}
	filekey := randomHex(16)
	req := getUploadURLRequest{
		Filekey:     filekey,
		MediaType:   mediaType,
		ToUserID:    to,
		Rawsize:     rawSize,
		Rawfilemd5:  md5Hex(plaintext),
		Filesize:    aesECBPaddedSize(rawSize),
		NoNeedThumb: true,
		Aeskey:      hex.EncodeToString(aesKey),
	}
	resp, err := p.api.getUploadURL(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: %w", label, err)
	}

	var cdnUploadURL string
	var uploadClient *http.Client
	if resp.UploadFullURL != "" {
		cdnUploadURL = resp.UploadFullURL
		if isWeixinCDNHost(cdnUploadURL) {
			uploadClient = p.cdnHTTPClient
		} else {
			uploadClient = p.httpClient
		}
	} else {
		cdnUploadURL = buildCdnUploadURL(p.cdnBaseURL, resp.UploadParam, filekey)
		uploadClient = p.httpClient
	}
	if uploadClient == nil {
		uploadClient = http.DefaultClient
	}

	dl, err := uploadBufferToCDN(ctx, uploadClient, cdnUploadURL, plaintext, aesKey, label)
	if err != nil {
		return nil, err
	}
	return &cdnUploadedRef{
		downloadParam: dl,
		aesKey:        aesKey,
		cipherSize:    aesECBPaddedSize(rawSize),
		rawSize:       rawSize,
	}, nil
}

func (p *Platform) sendSingleItemWithRetry(ctx context.Context, rc *replyContext, item messageItem) error {
	var lastErr error
	for attempt := 0; attempt < weixinSendMaxRetries; attempt++ {
		msg := sendMessageReq{
			Msg: weixinOutboundMsg{
				ToUserID:     rc.peerUserID,
				ClientID:     "omp-" + randomHex(8),
				MessageType:  messageTypeBot,
				MessageState: messageStateFinish,
				ItemList:     []messageItem{item},
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
				slog.Warn("weixin: sendMessage ret=-2, no fresh context_token",
					"attempt", attempt+1, "peer", rc.peerUserID)
				return fmt.Errorf("weixin: sendMessage ret=-2 (expired context_token); user must send a new message: %w", lastErr)
			}
			slog.Warn("weixin: sendMessage ret=-2, retrying with fresh context_token",
				"attempt", attempt+1, "peer", rc.peerUserID)
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

func mediaFromUploadRef(ref *cdnUploadedRef) *cdnMedia {
	return &cdnMedia{
		EncryptQueryParam: ref.downloadParam,
		AESKey:            formatAesKeyForAPI(ref.aesKey),
		EncryptType:       1,
	}
}

// SendImage implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, err := p.resolveReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("weixin: empty image")
	}
	ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, img.Data, uploadMediaImage, "SendImage")
	if err != nil {
		return err
	}
	item := messageItem{
		Type: messageItemImage,
		ImageItem: &imageItem{
			Media:   mediaFromUploadRef(ref),
			MidSize: ref.cipherSize,
		},
	}
	return p.sendSingleItemWithRetry(ctx, rc, item)
}

func isVideoFile(file core.FileAttachment) bool {
	mime := strings.ToLower(strings.TrimSpace(file.MimeType))
	if strings.HasPrefix(mime, "video/") {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.FileName)), ".")
	switch ext {
	case "avi", "m4v", "mkv", "mov", "mp4", "mpeg", "mpg", "webm":
		return true
	}
	return false
}

// SendFile implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, err := p.resolveReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if len(file.Data) == 0 {
		return fmt.Errorf("weixin: empty file")
	}
	name := strings.TrimSpace(file.FileName)
	if name == "" {
		name = "file.bin"
	}

	if isVideoFile(file) {
		ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, file.Data, uploadMediaVideo, "SendFileVideo")
		if err != nil {
			return err
		}
		item := messageItem{
			Type: messageItemVideo,
			VideoItem: &videoItem{
				Media:     mediaFromUploadRef(ref),
				VideoSize: ref.cipherSize,
			},
		}
		return p.sendSingleItemWithRetry(ctx, rc, item)
	}

	ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, file.Data, uploadMediaFile, "SendFile")
	if err != nil {
		return err
	}
	item := messageItem{
		Type: messageItemFile,
		FileItem: &fileItem{
			Media:    mediaFromUploadRef(ref),
			FileName: name,
			Len:      fmt.Sprintf("%d", ref.rawSize),
		},
	}
	return p.sendSingleItemWithRetry(ctx, rc, item)
}
