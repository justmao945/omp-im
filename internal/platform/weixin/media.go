package weixin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// imageDecryptMaterial extracts the CDN encrypted query param or plain URL and AES key from an image item.
func imageDecryptMaterial(img *imageItem) (encParam, aesKeyBase64 string, plainURL string, ok bool) {
	if img == nil {
		return "", "", "", false
	}

	media := img.Media
	if media == nil {
		media = img.ThumbMedia
	}
	if media == nil {
		return "", "", "", false
	}

	encParam = strings.TrimSpace(media.EncryptQueryParam)
	aesKeyBase64 = strings.TrimSpace(media.AESKey)
	plainURL = strings.TrimSpace(firstNonEmpty(media.URL, media.MediaURL, media.DownloadParam))

	if encParam != "" {
		if hx := strings.TrimSpace(img.AESKeyHex); hx != "" {
			raw, err := hex.DecodeString(hx)
			if err == nil && len(raw) == 16 {
				return encParam, base64.StdEncoding.EncodeToString(raw), "", true
			}
		}
		if k := strings.TrimSpace(media.AESKey); k != "" {
			return encParam, k, "", true
		}
		// We have an encrypt_param but no key: try downloading without decryption.
		return encParam, "", "", true
	}
	if plainURL != "" {
		return "", "", plainURL, true
	}
	return "", "", "", false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
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

	for i, it := range items {
		if it.Type != messageItemImage {
			continue
		}
		if it.ImageItem == nil {
			slog.Debug("weixin: inbound image item has nil ImageItem", "index", i)
			continue
		}
		enc, keyB64, plainURL, ok := imageDecryptMaterial(it.ImageItem)
		if !ok {
			slog.Debug("weixin: inbound image item has no CDN reference", "index", i, "media", fmt.Sprintf("%+v", it.ImageItem.Media))
			continue
		}
		ref := firstNonEmpty(enc, plainURL)
		if _, dup := seenEnc[ref]; dup {
			continue
		}
		seenEnc[ref] = struct{}{}

		var buf []byte
		var err error
		if plainURL != "" {
			buf, err = downloadPlainByURL(dlCtx, client, plainURL, "weixin inbound image")
		} else if keyB64 != "" {
			buf, err = downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound image")
		} else {
			buf, err = downloadPlainCDN(dlCtx, client, base, enc, "weixin inbound image")
		}
		if err != nil {
			slog.Warn("weixin: inbound image CDN failed", "index", i, "error", err)
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
			return "[Image download failed; please check cdn_base_url and encryption key config.]"
		case messageItemVoice:
			if it.VoiceItem == nil || strings.TrimSpace(it.VoiceItem.Text) == "" {
				return "[Voice message could not be processed.]"
			}
		case messageItemFile:
			return "[File download failed.]"
		case messageItemVideo:
			return "[Video download failed.]"
		}
	}
	return ""
}
