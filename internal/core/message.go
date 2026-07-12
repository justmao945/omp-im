package core

// ImageAttachment represents an image sent by the user.
type ImageAttachment struct {
	FileName string
	MimeType string
	Data     []byte
}

// FileAttachment represents a generic file sent by the user or agent.
type FileAttachment struct {
	FileName string
	MimeType string
	Data     []byte
}

// OutboundAttachment is a generated file or image the engine should send back.
type OutboundAttachment struct {
	Kind     string // "image" or "file"
	FileName string
	MimeType string
	Data     []byte
}

// Message represents a unified incoming message from any platform.
type Message struct {
	// SessionKey uniquely identifies this conversation.
	// e.g. "weixin:user@im.wechat" or "weixin:123@chatroom".
	SessionKey string
	// Platform is the platform name that produced this message.
	Platform string
	// MessageID is the platform message ID for tracing.
	MessageID string
	// ChannelID identifies the chat/channel (group or direct).
	ChannelID string
	// UserID identifies the sender.
	UserID string
	// UserName is the human-readable sender name, if known.
	UserName string
	// ChatName is the human-readable chat name, if known.
	ChatName string
	// Content is the text content of the message.
	Content string
	// Images are attached images (if any).
	Images []ImageAttachment
	// ReplyCtx is platform-specific data needed to reply to this message.
	ReplyCtx any
}
