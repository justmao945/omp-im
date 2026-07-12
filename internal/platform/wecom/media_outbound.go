package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/justmao945/omp-im/internal/core"
)

var (
	_ core.ImageSender = (*Platform)(nil)
	_ core.FileSender  = (*Platform)(nil)
)

// SendImage implements core.ImageSender for WeCom.
// It uploads the image as a temporary media and sends it to the original chat.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("wecom: image data is empty")
	}

	filename := wsImageFileName(img)
	mediaID, err := p.uploadWSMedia(ctx, "image", filename, img.Data)
	if err != nil {
		return fmt.Errorf("wecom: send image: %w", err)
	}
	if err := p.sendWSMediaReply(ctx, rc, "image", mediaID); err != nil {
		return fmt.Errorf("wecom: send image: %w", err)
	}
	slog.Debug("wecom: image sent", "chatid", rc.chatid, "media_id", mediaID, "bytes", len(img.Data))
	return nil
}

// SendFile implements core.FileSender for WeCom.
// It uploads the file as a temporary media and sends it to the original chat.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if len(file.Data) == 0 {
		return fmt.Errorf("wecom: file data is empty")
	}

	filename := wsFileFileName(file)
	mediaID, err := p.uploadWSMedia(ctx, "file", filename, file.Data)
	if err != nil {
		return fmt.Errorf("wecom: send file: %w", err)
	}
	if err := p.sendWSMediaReply(ctx, rc, "file", mediaID); err != nil {
		return fmt.Errorf("wecom: send file: %w", err)
	}
	slog.Debug("wecom: file sent", "chatid", rc.chatid, "media_id", mediaID, "filename", filename, "bytes", len(file.Data))
	return nil
}

// uploadWSMedia uploads a temporary media via the WebSocket three-step protocol:
// aibot_upload_media_init -> aibot_upload_media_chunk (×N) -> aibot_upload_media_finish.
func (p *Platform) uploadWSMedia(ctx context.Context, mediaType, filename string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty media data")
	}
	if len(data) > wsUploadMaxBytes {
		return "", fmt.Errorf("media too large: %d bytes exceeds %d", len(data), wsUploadMaxBytes)
	}

	totalChunks := (len(data) + wsUploadChunkSize - 1) / wsUploadChunkSize
	if totalChunks == 0 {
		return "", fmt.Errorf("empty media data")
	}
	if totalChunks > wsUploadMaxChunks {
		return "", fmt.Errorf("media too large: %d chunks exceeds maximum %d", totalChunks, wsUploadMaxChunks)
	}

	sum := md5.Sum(data)
	initReqID := generateReqID()
	initFrame := map[string]interface{}{
		"cmd": "aibot_upload_media_init",
		"headers": map[string]string{"req_id": initReqID},
		"body": map[string]interface{}{
			"type":         mediaType,
			"filename":     filename,
			"total_size":   len(data),
			"total_chunks": totalChunks,
			"md5":          hex.EncodeToString(sum[:]),
		},
	}
	initResp, err := p.wsClient.writeAndWaitFrameWithTimeout(ctx, initFrame, initReqID, wsMediaAckTimeout)
	if err != nil {
		return "", fmt.Errorf("upload init: %w", err)
	}
	var initBody struct {
		UploadID string `json:"upload_id"`
	}
	if err := decodeMap(initResp.Body, &initBody); err != nil {
		return "", fmt.Errorf("decode upload init response: %w", err)
	}
	if initBody.UploadID == "" {
		return "", fmt.Errorf("upload init: empty upload_id")
	}

	for i := 0; i < totalChunks; i++ {
		start := i * wsUploadChunkSize
		end := start + wsUploadChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]

		reqID := generateReqID()
		chunkFrame := map[string]interface{}{
			"cmd": "aibot_upload_media_chunk",
			"headers": map[string]string{"req_id": reqID},
			"body": map[string]interface{}{
				"upload_id":   initBody.UploadID,
				"chunk_index": i,
				"base64_data": base64.StdEncoding.EncodeToString(chunk),
			},
		}
		chunkResp, err := p.wsClient.writeAndWaitFrameWithTimeout(ctx, chunkFrame, reqID, wsMediaAckTimeout)
		if err != nil {
			return "", fmt.Errorf("upload chunk %d: %w", i, err)
		}
		var chunkBody struct {
			Ack bool `json:"ack"`
		}
		if err := decodeMap(chunkResp.Body, &chunkBody); err != nil {
			return "", fmt.Errorf("decode upload chunk %d response: %w", i, err)
		}
		if !chunkBody.Ack {
			return "", fmt.Errorf("upload chunk %d not acknowledged", i)
		}
	}

	finishReqID := generateReqID()
	finishFrame := map[string]interface{}{
		"cmd": "aibot_upload_media_finish",
		"headers": map[string]string{"req_id": finishReqID},
		"body": map[string]interface{}{
			"upload_id": initBody.UploadID,
		},
	}
	finishResp, err := p.wsClient.writeAndWaitFrameWithTimeout(ctx, finishFrame, finishReqID, wsMediaAckTimeout)
	if err != nil {
		return "", fmt.Errorf("upload finish: %w", err)
	}
	var finishBody struct {
		MediaID string `json:"media_id"`
	}
	if err := decodeMap(finishResp.Body, &finishBody); err != nil {
		return "", fmt.Errorf("decode upload finish response: %w", err)
	}
	if finishBody.MediaID == "" {
		return "", fmt.Errorf("upload finish: empty media_id")
	}
	return finishBody.MediaID, nil
}

// sendWSMediaReply sends a passive media reply bound to the original inbound req_id.
// If the passive reply fails (no req_id available), it falls back to an active push.
func (p *Platform) sendWSMediaReply(ctx context.Context, rc *replyContext, mediaType, mediaID string) error {
	body := map[string]interface{}{
		"msgtype": mediaType,
		mediaType: map[string]string{
			"media_id": mediaID,
		},
	}
	if rc.reqID != "" {
		frame := map[string]interface{}{
			"cmd":     "aibot_respond_msg",
			"headers": map[string]string{"req_id": rc.reqID},
			"body":    body,
		}
		if err := p.wsClient.send(frame); err != nil {
			return err
		}
		return nil
	}
	return p.sendWSMediaMessage(ctx, rc.chatid, mediaType, mediaID)
}

// sendWSMediaMessage sends an active media message with a temporary media_id.
func (p *Platform) sendWSMediaMessage(ctx context.Context, chatID, mediaType, mediaID string) error {
	reqID := generateReqID()
	body := map[string]interface{}{
		"chatid":  chatID,
		"msgtype": mediaType,
		mediaType: map[string]string{
			"media_id": mediaID,
		},
	}
	frame := map[string]interface{}{
		"cmd":     "aibot_send_msg",
		"headers": map[string]string{"req_id": reqID},
		"body":    body,
	}
	return p.wsClient.writeAndWaitAck(ctx, frame, reqID)
}

// decodeMap converts a map[string]interface{} to a struct using JSON as the bridge.
func decodeMap(m map[string]interface{}, v interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func wsImageFileName(img core.ImageAttachment) string {
	name := filepath.Base(strings.TrimSpace(img.FileName))
	if name != "" && name != "." {
		return name
	}
	switch strings.ToLower(img.MimeType) {
	case "image/jpeg", "image/jpg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	default:
		return "image.png"
	}
}

func wsFileFileName(file core.FileAttachment) string {
	name := filepath.Base(strings.TrimSpace(file.FileName))
	if name != "" && name != "." {
		return name
	}
	switch strings.ToLower(file.MimeType) {
	case "text/html":
		return "file.html"
	case "application/pdf":
		return "file.pdf"
	case "text/plain":
		return "file.txt"
	case "text/markdown":
		return "file.md"
	case "application/json":
		return "file.json"
	case "application/zip":
		return "file.zip"
	default:
		return "file.bin"
	}
}
