package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

const maxWecomMediaBytes = 100 << 20

// downloadImage fetches and decrypts an image from the WeCom CDN.
func downloadImage(ctx context.Context, client *http.Client, img imageAttachment) (core.ImageAttachment, error) {
	if client == nil {
		client = http.DefaultClient
	}
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

	if img.aeskey == "" {
		mt := detectImageMime(body)
		return core.ImageAttachment{MimeType: mt, Data: body}, nil
	}

	plain, err := decryptWecomAES(body, img.aeskey)
	if err != nil {
		return core.ImageAttachment{}, fmt.Errorf("wecom image: decrypt: %w", err)
	}
	mt := detectImageMime(plain)
	return core.ImageAttachment{MimeType: mt, Data: plain}, nil
}

// decryptWecomAES decrypts WeCom CDN data using AES-256-CBC.
// IV is the first 16 bytes of the base64-decoded key.
func decryptWecomAES(ciphertext []byte, aesKeyBase64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("aes_key base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("aes_key must be 32 bytes, got %d", len(key))
	}
	iv := key[:16]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not aligned to block size")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	out := make([]byte, len(ciphertext))
	mode.CryptBlocks(out, ciphertext)
	return pkcs7Unpad(out, 32)
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
	return "image/jpeg"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// collectInboundImages downloads images referenced by an inbound message.
func (p *Platform) collectInboundImages(ctx context.Context, msg *inboundMessage) []core.ImageAttachment {
	if msg == nil || len(msg.images) == 0 {
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
	}
	return result
}
