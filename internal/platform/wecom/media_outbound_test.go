package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justmao945/omp-im/internal/core"
)

// mockWeComWSServer upgrades HTTP connections to WebSocket and replies to upload/send frames.
type mockWeComWSServer struct {
	upgrader websocket.Upgrader
	server   *httptest.Server
	conn     *websocket.Conn

	uploadInitFrames    []map[string]interface{}
	uploadChunkFrames   []map[string]interface{}
	uploadFinishFrames  []map[string]interface{}
	sendMessageFrames   []map[string]interface{}
	respondMessageFrames []map[string]interface{}
	respondedCh         chan map[string]interface{}
}

func newMockWeComWSServer(t *testing.T) *mockWeComWSServer {
	m := &mockWeComWSServer{respondedCh: make(chan map[string]interface{}, 10)}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := m.upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		m.conn = conn
		defer conn.Close()

		// Read subscribe
		var sub map[string]interface{}
		if err := conn.ReadJSON(&sub); err != nil {
			t.Fatalf("read subscribe: %v", err)
		}
		// Subscribe ack
		if err := conn.WriteJSON(map[string]interface{}{
			"headers":  map[string]string{"req_id": getMapString(sub, "headers", "req_id")},
			"errcode":  0,
			"errmsg":   "ok",
			"body":     map[string]interface{}{},
		}); err != nil {
			t.Fatalf("write subscribe ack: %v", err)
		}

		for {
			var frame map[string]interface{}
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			cmd, _ := frame["cmd"].(string)
			reqID := getMapString(frame, "headers", "req_id")

			switch cmd {
			case "aibot_upload_media_init":
				m.uploadInitFrames = append(m.uploadInitFrames, frame)
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
					"body":    map[string]interface{}{"upload_id": "upload-1"},
				}); err != nil {
					t.Fatalf("write init ack: %v", err)
				}
			case "aibot_upload_media_chunk":
				m.uploadChunkFrames = append(m.uploadChunkFrames, frame)
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
					"body":    map[string]interface{}{"ack": true},
				}); err != nil {
					t.Fatalf("write chunk ack: %v", err)
				}
			case "aibot_upload_media_finish":
				m.uploadFinishFrames = append(m.uploadFinishFrames, frame)
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
					"body":    map[string]interface{}{"media_id": "media-1"},
				}); err != nil {
					t.Fatalf("write finish ack: %v", err)
				}
			case "aibot_send_msg":
				m.sendMessageFrames = append(m.sendMessageFrames, frame)
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
				}); err != nil {
					t.Fatalf("write send ack: %v", err)
				}
			case "aibot_respond_msg":
				m.respondMessageFrames = append(m.respondMessageFrames, frame)
				m.respondedCh <- frame
				// Passive replies do not always get a separate ack; we still send one for completeness.
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
				}); err != nil {
					t.Fatalf("write respond ack: %v", err)
				}
			case "ping":
				if err := conn.WriteJSON(map[string]interface{}{
					"headers": map[string]string{"req_id": reqID},
					"errcode": 0,
					"errmsg":  "ok",
				}); err != nil {
					t.Fatalf("write ping ack: %v", err)
				}
			}
		}
	}))
	return m
}

func (m *mockWeComWSServer) URL() string {
	return strings.Replace(m.server.URL, "http:", "ws:", 1)
}

func (m *mockWeComWSServer) Close() {
	m.server.Close()
	if m.conn != nil {
		m.conn.Close()
	}
}

func getMapString(m map[string]interface{}, path ...string) string {
	v := m
	for i := 0; i < len(path)-1; i++ {
		next, ok := v[path[i]].(map[string]interface{})
		if !ok {
			return ""
		}
		v = next
	}
	if s, ok := v[path[len(path)-1]].(string); ok {
		return s
	}
	return ""
}

func getMapMap(m map[string]interface{}, path ...string) map[string]interface{} {
	v := m
	for i := 0; i < len(path)-1; i++ {
		next, ok := v[path[i]].(map[string]interface{})
		if !ok {
			return nil
		}
		v = next
	}
	if mm, ok := v[path[len(path)-1]].(map[string]interface{}); ok {
		return mm
	}
	return nil
}

func newWeComPlatformWithMockServer(t *testing.T, mockURL string) *Platform {
	cfg, err := parseConfig(map[string]interface{}{
		"bot_id":       "b1",
		"secret":       "s1",
		"websocket_url": mockURL,
		"allow_from":   "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Platform{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	p.wsClient = newWSClient(cfg, p.handleFrame)
	return p
}

func TestSendImage_UploadsAndSendsMedia(t *testing.T) {
	mock := newMockWeComWSServer(t)
	defer mock.Close()

	p := newWeComPlatformWithMockServer(t, mock.URL())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := p.wsClient.run(ctx); err != nil && err != context.Canceled {
			t.Errorf("wsClient.run: %v", err)
		}
	}()

	// Wait for connection + subscribe
	if err := p.Ping(); err != nil {
		t.Fatal(err)
	}

	img := core.ImageAttachment{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}}
	rc := &replyContext{chatid: "c1", chattype: "single", reqID: "r1"}
	if err := p.SendImage(context.Background(), rc, img); err != nil {
		t.Fatalf("SendImage: %v", err)
	}

	select {
	case <-mock.respondedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for passive respond frame")
	}

	if len(mock.uploadInitFrames) != 1 {
		t.Fatalf("init frames = %d", len(mock.uploadInitFrames))
	}
	initBody := getMapMap(mock.uploadInitFrames[0], "body")
	if initBody["type"] != "image" || initBody["filename"] != "image.png" || initBody["total_chunks"] != 1.0 {
		t.Fatalf("unexpected init body: %v", initBody)
	}
	if len(mock.uploadChunkFrames) != 1 {
		t.Fatalf("chunk frames = %d", len(mock.uploadChunkFrames))
	}
	if len(mock.uploadFinishFrames) != 1 {
		t.Fatalf("finish frames = %d", len(mock.uploadFinishFrames))
	}

	// We supplied a reqID, so passive reply should be used.
	if len(mock.respondMessageFrames) != 1 {
		t.Fatalf("respond frames = %d", len(mock.respondMessageFrames))
	}
	respBody := getMapMap(mock.respondMessageFrames[0], "body")
	if respBody["msgtype"] != "image" {
		t.Fatalf("respond msgtype = %v", respBody["msgtype"])
	}
	imgRef := getMapMap(respBody, "image")
	if imgRef["media_id"] != "media-1" {
		t.Fatalf("media_id = %v", imgRef["media_id"])
	}
}

func TestSendFile_UploadsAndSendsMedia(t *testing.T) {
	mock := newMockWeComWSServer(t)
	defer mock.Close()

	p := newWeComPlatformWithMockServer(t, mock.URL())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := p.wsClient.run(ctx); err != nil && err != context.Canceled {
			t.Errorf("wsClient.run: %v", err)
		}
	}()

	if err := p.Ping(); err != nil {
		t.Fatal(err)
	}

	file := core.FileAttachment{FileName: "report.txt", MimeType: "text/plain", Data: []byte("hello")}
	// No reqID triggers active send fallback.
	rc := &replyContext{chatid: "c1", chattype: "single", reqID: ""}
	if err := p.SendFile(context.Background(), rc, file); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	if len(mock.uploadInitFrames) != 1 {
		t.Fatalf("init frames = %d", len(mock.uploadInitFrames))
	}
	initBody := getMapMap(mock.uploadInitFrames[0], "body")
	if initBody["type"] != "file" || initBody["filename"] != "report.txt" {
		t.Fatalf("unexpected init body: %v", initBody)
	}
	if len(mock.sendMessageFrames) != 1 {
		t.Fatalf("send frames = %d", len(mock.sendMessageFrames))
	}
	body := getMapMap(mock.sendMessageFrames[0], "body")
	if body["msgtype"] != "file" {
		t.Fatalf("msgtype = %v", body["msgtype"])
	}
	fileRef := getMapMap(body, "file")
	if fileRef["media_id"] != "media-1" {
		t.Fatalf("media_id = %v", fileRef["media_id"])
	}
}

func TestSendImage_FilenameFallback(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/jpeg", "image.jpg"},
		{"image/gif", "image.gif"},
		{"image/webp", "image.webp"},
		{"image/png", "image.png"},
		{"image/svg+xml", "image.png"},
	}
	for _, tc := range cases {
		img := core.ImageAttachment{MimeType: tc.mime, Data: []byte{1, 2, 3}}
		got := wsImageFileName(img)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.mime, got, tc.want)
		}
	}

	img := core.ImageAttachment{FileName: "/tmp/foo.jpg", MimeType: "image/png", Data: []byte{1, 2, 3}}
	if got := wsImageFileName(img); got != "foo.jpg" {
		t.Errorf("explicit filename: got %q", got)
	}
}

func TestSendFile_FilenameFallback(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"application/pdf", "file.pdf"},
		{"text/plain", "file.txt"},
		{"application/octet-stream", "file.bin"},
	}
	for _, tc := range cases {
		file := core.FileAttachment{MimeType: tc.mime, Data: []byte{1, 2, 3}}
		got := wsFileFileName(file)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestUploadMedia_Chunking(t *testing.T) {
	mock := newMockWeComWSServer(t)
	defer mock.Close()

	p := newWeComPlatformWithMockServer(t, mock.URL())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := p.wsClient.run(ctx); err != nil && err != context.Canceled {
			t.Errorf("wsClient.run: %v", err)
		}
	}()

	if err := p.Ping(); err != nil {
		t.Fatal(err)
	}

	// Two chunks exactly.
	data := make([]byte, wsUploadChunkSize+1)
	mediaID, err := p.uploadWSMedia(context.Background(), "image", "big.png", data)
	if err != nil {
		t.Fatal(err)
	}
	if mediaID != "media-1" {
		t.Fatalf("media_id = %q", mediaID)
	}
	if len(mock.uploadChunkFrames) != 2 {
		t.Fatalf("chunks = %d", len(mock.uploadChunkFrames))
	}
}

func TestUploadMedia_TooLarge(t *testing.T) {
	p := &Platform{}
	_, err := p.uploadWSMedia(context.Background(), "image", "huge.png", make([]byte, wsUploadMaxBytes+1))
	if err == nil {
		t.Fatal("expected error for oversized media")
	}
}

func TestDecodeMap(t *testing.T) {
	m := map[string]interface{}{"upload_id": "u1", "count": 2.0}
	var out struct {
		UploadID string `json:"upload_id"`
		Count    int    `json:"count"`
	}
	if err := decodeMap(m, &out); err != nil {
		t.Fatal(err)
	}
	if out.UploadID != "u1" || out.Count != 2 {
		t.Fatalf("out = %+v", out)
	}
}

func TestSendImage_PassiveReply(t *testing.T) {
	// Verify that passive reply frames contain the original req_id and the right media reference.
	frame := map[string]interface{}{
		"cmd":     "aibot_respond_msg",
		"headers": map[string]string{"req_id": "r-origin"},
		"body": map[string]interface{}{
			"msgtype": "image",
			"image":   map[string]string{"media_id": "m1"},
		},
	}
	data, _ := json.Marshal(frame)
	if !strings.Contains(string(data), "r-origin") {
		t.Fatal("req_id not in frame")
	}
}
