package weixin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// imageDecryptMaterial extracts the CDN encrypted query param and AES key from an image item.
func imageDecryptMaterial(img *imageItem) (encParam, aesKeyBase64 string, ok bool) {
	if img == nil || img.Media == nil {
		return "", "", false
	}
	encParam = strings.TrimSpace(img.Media.EncryptQueryParam)
	if encParam == "" {
		return "", "", false
	}
	if hx := strings.TrimSpace(img.AESKeyHex); hx != "" {
		raw, err := hex.DecodeString(hx)
		if err == nil && len(raw) == 16 {
			return encParam, base64.StdEncoding.EncodeToString(raw), true
		}
	}
	if k := strings.TrimSpace(img.Media.AESKey); k != "" {
		return encParam, k, true
	}
	return encParam, "", false
}

// collectInboundImages downloads and decrypts image attachments from Weixin CDN.
func (p *Platform) collectInboundImages(ctx context.Context, items []messageItem) []core.ImageAttachment {
	if p == nil || len(items) == 0 || strings.TrimSpace(p.cdnBaseURL) == "" {
		return nil
	}
	dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := p.cdnHTTPClient
	if client == nil {
		client = p.httpClient
	}
	base := p.cdnBaseURL

	seenEnc := make(map[string]struct{})
	var images []core.ImageAttachment

	for _, it := range items {
		if it.Type != messageItemImage {
			continue
		}
		enc, keyB64, hasKey := imageDecryptMaterial(it.ImageItem)
		if enc == "" {
			continue
		}
		if _, ok := seenEnc[enc]; ok {
			continue
		}
		seenEnc[enc] = struct{}{}

		var buf []byte
		var err error
		if hasKey && keyB64 != "" {
			buf, err = downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound image")
		} else {
			buf, err = downloadPlainCDN(dlCtx, client, base, enc, "weixin inbound image")
		}
		if err != nil {
			slog.Warn("weixin: inbound image CDN failed", "error", err)
			continue
		}
		mt := detectImageMime(buf)
		images = append(images, core.ImageAttachment{MimeType: mt, Data: buf})
	}
	return images
}

// mediaFallbackNotice returns a user-facing hint when media is present but could not be fetched.
func mediaFallbackNotice(items []messageItem) string {
	for _, it := range items {
		switch it.Type {
		case messageItemImage:
			return "[图片未能下载，请检查 cdn_base_url 和加密密钥配置。]"
		case messageItemVoice:
			if it.VoiceItem == nil || strings.TrimSpace(it.VoiceItem.Text) == "" {
				return "[语音消息未能处理。]"
			}
		case messageItemFile:
			return "[文件未能下载。]"
		case messageItemVideo:
			return "[视频未能下载。]"
		}
	}
	return ""
}
