package weixin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImageDecryptMaterial(t *testing.T) {
	enc, key, url, ok := imageDecryptMaterial(&imageItem{Media: &cdnMedia{EncryptQueryParam: "abc", AESKey: "MTIzNDU2Nzg5MGFiY2RlZg=="}})
	if !ok || enc != "abc" || key == "" || url != "" {
		t.Fatalf("unexpected material: enc=%q key=%q url=%q ok=%v", enc, key, url, ok)
	}

	enc, key, url, ok = imageDecryptMaterial(
		&imageItem{Media: &cdnMedia{URL: "https://example.com/img.png"}})
	if !ok || enc != "" || key != "" || url != "https://example.com/img.png" {
		t.Fatalf("unexpected URL material: enc=%q key=%q url=%q ok=%v", enc, key, url, ok)
	}

	enc, key, url, ok = imageDecryptMaterial(
		&imageItem{ThumbMedia: &cdnMedia{EncryptQueryParam: "thumb"}})
	if !ok || enc != "thumb" {
		t.Fatalf("unexpected thumb material: enc=%q key=%q url=%q ok=%v", enc, key, url, ok)
	}
}

func TestCollectInboundImages(t *testing.T) {
	wantBody := []byte("fake-image-bytes")
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		enc := r.URL.Query().Get("encrypted_query_param")
		if enc == "" {
			http.Error(w, "missing encrypted_query_param", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(wantBody)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &Platform{cdnBaseURL: srv.URL}

	items := []messageItem{
		{Type: messageItemImage, ImageItem: &imageItem{Media: &cdnMedia{EncryptQueryParam: "plain-enc"}}},
	}
	images := p.collectInboundImages(context.Background(), items)
	if len(images) != 1 {
		t.Fatalf("images = %d", len(images))
	}
	if string(images[0].Data) != string(wantBody) {
		t.Fatalf("image data mismatch")
	}
	if images[0].MimeType != "image/jpeg" {
		t.Fatalf("mime = %q", images[0].MimeType)
	}
}

func TestDetectImageMime(t *testing.T) {
	cases := []struct {
		prefix []byte
		want   string
	}{
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0}, "image/jpeg"},
		{[]byte("\x89PNG\r\n\x1a\n"), "image/png"},
		{[]byte("GIF87a"), "image/gif"},
		{[]byte("GIF89a"), "image/gif"},
		{[]byte("RIFF....WEBP"), "image/webp"},
		{[]byte("unknown"), "image/jpeg"},
	}
	for _, c := range cases {
		got := detectImageMime(c.prefix)
		if got != c.want {
			t.Fatalf("detectImageMime(%q) = %q, want %q", c.prefix, got, c.want)
		}
	}
}
