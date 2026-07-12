package weixin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func TestPlatformPollsAndSendsText(t *testing.T) {
	t.Setenv("OMP_IM_DATA_DIR", t.TempDir())

	var mu sync.Mutex
	updatesReqCount := 0
	sentMessages := []sendMessageReq{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ilink/bot/getupdates":
			mu.Lock()
			updatesReqCount++
			count := updatesReqCount
			mu.Unlock()

			var req getUpdatesReq
			_ = json.Unmarshal(body, &req)

			var resp getUpdatesResp
			if count == 1 {
				resp = getUpdatesResp{
					Ret: 0,
					Msgs: []weixinMessage{
						{
							MessageID:    1,
							FromUserID:   "u1@im.wechat",
							MessageType:  messageTypeUser,
							ItemList:     []messageItem{{Type: messageItemText, TextItem: &textItem{Text: "hello"}}},
							ContextToken: "tok-1",
						},
					},
					GetUpdatesBuf: "buf-1",
				}
			} else {
				// No more messages; keep the poll alive by returning empty.
				resp = getUpdatesResp{GetUpdatesBuf: "buf-1"}
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/ilink/bot/sendmessage":
			var req sendMessageReq
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			sentMessages = append(sentMessages, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 1)
	handler := func(p core.Platform, msg *core.Message) {
		received <- msg
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Reply(ctx, msg.ReplyCtx, "hi there")
	}

	p, err := New(map[string]any{
		"token":    "test-token",
		"base_url": srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Start(handler); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	select {
	case msg := <-received:
		if msg.UserID != "u1@im.wechat" {
			t.Fatalf("user = %q", msg.UserID)
		}
		if msg.Content != "hello" {
			t.Fatalf("content = %q", msg.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	// Wait for the async send to complete.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	if len(sentMessages) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sentMessages))
	}
	if sentMessages[0].Msg.ItemList[0].TextItem.Text != "hi there" {
		t.Fatalf("sent text = %q", sentMessages[0].Msg.ItemList[0].TextItem.Text)
	}
	mu.Unlock()
}
