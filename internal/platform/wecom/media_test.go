package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justmao945/omp-im/internal/core"
)

func TestParseImageMessage(t *testing.T) {
	frame := &wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: map[string]string{"req_id": "r1"},
		Body: map[string]interface{}{
			"msgid":    "m1",
			"chatid":   "u1",
			"chattype": "single",
			"msgtype":  "image",
			"from":     map[string]interface{}{"userid": "u1"},
			"image":    map[string]interface{}{"url": "https://cdn.example.com/img.jpg", "aeskey": "key"},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.text != "[image]" {
		t.Fatalf("text = %q", msg.text)
	}
	if len(msg.images) != 1 || msg.images[0].url != "https://cdn.example.com/img.jpg" {
		t.Fatalf("images = %+v", msg.images)
	}
}

func TestParseMixedWithImage(t *testing.T) {
	frame := &wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: map[string]string{"req_id": "r1"},
		Body: map[string]interface{}{
			"msgid":    "m1",
			"chatid":   "g1",
			"chattype": "group",
			"msgtype":  "mixed",
			"from":     map[string]interface{}{"userid": "u1"},
			"mixed": map[string]interface{}{
				"msg_item": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": map[string]interface{}{"content": "look"},
					},
					map[string]interface{}{
						"type":  "image",
						"image": map[string]interface{}{"url": "https://cdn.example.com/a.png", "aeskey": "k"},
					},
				},
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.text != "look" {
		t.Fatalf("text = %q", msg.text)
	}
	if len(msg.images) != 1 || msg.images[0].url != "https://cdn.example.com/a.png" {
		t.Fatalf("images = %+v", msg.images)
	}
}

func TestDownloadImagePlain(t *testing.T) {
	wantBody := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(wantBody)
	}))
	defer srv.Close()

	at, err := downloadImage(context.Background(), nil, imageAttachment{url: srv.URL})
	var _ core.ImageAttachment = at
	if err != nil {
		t.Fatal(err)
	}
	if string(at.Data) != string(wantBody) {
		t.Fatalf("data mismatch")
	}
	if at.MimeType != "image/jpeg" {
		t.Fatalf("mime = %q", at.MimeType)
	}
}

func TestDecryptWecomAES(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	// plaintext padded to 32 bytes
	plain := []byte("hello world")
	padLen := 32 - len(plain)%32
	if padLen == 0 {
		padLen = 32
	}
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	iv := key[:16]
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	cipher := make([]byte, len(padded))
	mode.CryptBlocks(cipher, padded)

	got, err := decryptWecomAES(cipher, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted = %q", got)
	}
}
