package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

const maxWecomMediaBytes = 100 << 20

// downloadFile fetches and decrypts a generic file attachment from the WeCom CDN.
func downloadFile(ctx context.Context, client *http.Client, f fileAttachment) (core.FileAttachment, error) {
	if client == nil {
		client = http.DefaultClient
	}

	slog.Debug("wecom: downloading file", "url", f.url, "has_aeskey", f.aeskey != "", "filename", f.filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return core.FileAttachment{}, fmt.Errorf("wecom file: new request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return core.FileAttachment{}, fmt.Errorf("wecom file: get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWecomMediaBytes+1))
	if err != nil {
		return core.FileAttachment{}, fmt.Errorf("wecom file: read: %w", err)
	}
	if len(body) > maxWecomMediaBytes {
		return core.FileAttachment{}, fmt.Errorf("wecom file: exceeds %d bytes", maxWecomMediaBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return core.FileAttachment{}, fmt.Errorf("wecom file: http %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	filename := f.filename
	if filename == "" {
		filename = parseContentDispositionFilename(resp.Header.Get("Content-Disposition"))
	}
	if filename == "" {
		filename = filepath.Base(resp.Request.URL.Path)
	}
	if filename == "" {
		filename = filepath.Base(f.url)
	}

	var data []byte
	if f.aeskey == "" {
		data = body
	} else {
		plain, err := decryptWecomAES(body, f.aeskey)
		if err != nil {
			return core.FileAttachment{}, fmt.Errorf("wecom file: decrypt: %w", err)
		}
		data = plain
	}

	mt := detectMimeType(data, filename)
	slog.Debug("wecom: file downloaded", "filename", filename, "mime", mt, "bytes", len(data))
	return core.FileAttachment{FileName: filename, MimeType: mt, Data: data}, nil
}

// detectMimeType tries to determine the MIME type from content sniffing and filename extension.
func detectMimeType(data []byte, filename string) string {
	if mt := http.DetectContentType(data); mt != "application/octet-stream" {
		return mt
	}
	if ext := filepath.Ext(filename); ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	return "application/octet-stream"
}

// downloadImage fetches and decrypts an image from the WeCom CDN.
func downloadImage(ctx context.Context, client *http.Client, img imageAttachment) (core.ImageAttachment, error) {
	if client == nil {
		client = http.DefaultClient
	}

	slog.Debug("wecom: downloading image", "url", img.url, "has_aeskey", img.aeskey != "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, img.url, nil)
	if err != nil {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: new request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWecomMediaBytes+1))
	if err != nil {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: read: %w", err)
	}
	if len(body) > maxWecomMediaBytes {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: exceeds %d bytes", maxWecomMediaBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: http %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	slog.Debug("wecom: image download response", "status", resp.StatusCode, "bytes", len(body), "content_type", resp.Header.Get("Content-Type"))

	if img.aeskey == "" {
		mt := detectImageMime(body)
		if mt == "" {
			mt = http.DetectContentType(body)
		}
		if !strings.HasPrefix(mt, "image/") {
			mt = "image/jpeg"
		}
		slog.Debug("wecom: image plain url", "mime", mt, "bytes", len(body))
		return core.ImageAttachment{MimeType: mt, Data: body}, nil
	}

	plain, err := decryptWecomAES(body, img.aeskey)
	if err != nil {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: decrypt: %w", err)
	}
	mt := detectImageMime(plain)
	if mt == "" {
		mt = http.DetectContentType(plain)
	}
	if !strings.HasPrefix(mt, "image/") {
		mt = "image/jpeg"
	}
	slog.Debug("wecom: image decrypted", "mime", mt, "bytes", len(plain))
	return core.ImageAttachment{MimeType: mt, Data: plain}, nil
}

// decodeWeComAESKey normalizes and decodes the aeskey from WeCom WS callbacks.
// The server may send standard Base64, URL-safe Base64 (- _), omit padding,
// insert whitespace, or (rarely) a 64-char hex string. Node's Buffer.from is more
// permissive than Go's StdEncoding; we mirror common cases so decryption matches
// the official SDK.
func decodeWeComAESKey(aesKey string) ([]byte, error) {
	s := strings.TrimSpace(aesKey)
	if s == "" {
		return nil, fmt.Errorf("empty aeskey")
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n', '\r', ' ', '\t':
			continue
		default:
			b.WriteByte(s[i])
		}
	}
	s = b.String()

	if len(s) == 64 && isHexString(s) {
		key, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("decode aeskey hex: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("aeskey hex length %d, want 32 bytes", len(key))
		}
		return key, nil
	}

	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	switch len(s) % 4 {
	case 0:
	case 2:
		s += "=="
	case 3:
		s += "="
	default:
		return nil, fmt.Errorf("invalid aeskey base64 length")
	}

	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode aeskey: %w", err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("aeskey decoded length %d, need >= 32", len(key))
	}
	return key, nil
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// decryptWecomAES decrypts WeCom CDN data using AES-256-CBC.
// IV is the first 16 bytes of the decoded key, matching the official SDK.
func decryptWecomAES(ciphertext []byte, aesKeyBase64 string) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	key, err := decodeWeComAESKey(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("decode aes key: %w", err)
	}
	key32 := key[:32]
	iv := key32[:16]

	block, err := aes.NewCipher(key32)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d not a multiple of block size", len(ciphertext))
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return pkcs7Unpad(plain, 32)
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || len(b) == 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	padLen := int(b[len(b)-1])
	if padLen < 1 || padLen > blockSize || padLen > len(b) {
		return nil, fmt.Errorf("invalid padding length")
	}
	for i := len(b) - padLen; i < len(b); i++ {
		if b[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid padding bytes")
		}
	}
	return b[:len(b)-padLen], nil
}

func detectImageMime(b []byte) string {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return "image/jpeg"
	}
	if len(b) >= 8 && string(b[0:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(b) >= 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// collectInboundFiles downloads file attachments referenced by an inbound message.
func (p *Platform) collectInboundFiles(ctx context.Context, msg *inboundMessage) []core.FileAttachment {
	if msg == nil || len(msg.files) == 0 {
		slog.Debug("wecom: no files to collect", "msgtype", msg.msgtype)
		return nil
	}
	dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	var result []core.FileAttachment
	for i, f := range msg.files {
		at, err := downloadFile(dlCtx, client, f)
		if err != nil {
			slog.Warn("wecom: failed to download file", "index", i, "url", f.url, "error", err)
			continue
		}
		result = append(result, at)
		slog.Info("wecom: file collected", "index", i, "filename", at.FileName, "mime", at.MimeType, "bytes", len(at.Data))
	}
	if len(result) == 0 && len(msg.files) > 0 {
		slog.Warn("wecom: all file downloads failed", "count", len(msg.files), "msgtype", msg.msgtype)
	}
	return result
}

// collectInboundImages downloads images referenced by an inbound message.
func (p *Platform) collectInboundImages(ctx context.Context, msg *inboundMessage) []core.ImageAttachment {
	if msg == nil || len(msg.images) == 0 {
		slog.Debug("wecom: no images to collect", "msgtype", msg.msgtype)
		return nil
	}
	dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	var result []core.ImageAttachment
	for i, img := range msg.images {
		at, err := downloadImage(dlCtx, client, img)
		if err != nil {
			slog.Warn("wecom: failed to download image", "index", i, "url", img.url, "error", err)
			continue
		}
		result = append(result, at)
		slog.Info("wecom: image collected", "index", i, "mime", at.MimeType, "bytes", len(at.Data))
	}
	if len(result) == 0 && len(msg.images) > 0 {
		slog.Warn("wecom: all image downloads failed", "count", len(msg.images), "msgtype", msg.msgtype)
	}
	return result
}

// parseContentDispositionFilename extracts the filename from a Content-Disposition header.
func parseContentDispositionFilename(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	lower := strings.ToLower(h)
	if idx := strings.Index(lower, "filename*="); idx >= 0 {
		val := strings.TrimSpace(h[idx+len("filename*="):])
		val = strings.TrimSuffix(strings.TrimSpace(val), ";")
		if after, ok := strings.CutPrefix(val, "UTF-8''"); ok {
			if dec, err := url.QueryUnescape(after); err == nil {
				return filepath.Base(dec)
			}
			return filepath.Base(after)
		}
	}
	if idx := strings.Index(lower, "filename="); idx >= 0 {
		val := strings.TrimSpace(h[idx+len("filename="):])
		val = strings.TrimSuffix(val, ";")
		val = strings.Trim(val, `"`)
		if dec, err := url.QueryUnescape(val); err == nil {
			return filepath.Base(dec)
		}
		return filepath.Base(val)
	}
	return ""
}
