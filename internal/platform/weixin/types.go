package weixin

// JSON shapes mirror the ilink bot HTTP API (Weixin / personal bridge).

const (
	messageTypeUser = 1
	messageTypeBot  = 2

	messageItemText  = 1
	messageItemImage = 2
	messageItemVoice = 3
	messageItemFile  = 4
	messageItemVideo = 5

	messageStateFinish = 2

	sessionExpiredErrcode = -14

	uploadMediaImage = 2
	uploadMediaFile  = 4
	uploadMediaVideo = 5
)

type baseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type getUpdatesReq struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      baseInfo `json:"base_info"`
}

type getUpdatesResp struct {
	Ret           int             `json:"ret"`
	Errcode       int             `json:"errcode"`
	Errmsg        string          `json:"errmsg"`
	Msgs          []weixinMessage `json:"msgs"`
	GetUpdatesBuf string          `json:"get_updates_buf"`
}

type textItem struct {
	Text string `json:"text,omitempty"`
}

// cdnMedia mirrors CDNMedia in the ilink JSON API.
type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

type imageItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	ThumbMedia *cdnMedia `json:"thumb_media,omitempty"`
	AESKeyHex  string    `json:"aeskey,omitempty"` // inbound: raw key as hex (16 bytes)
	MidSize    int       `json:"mid_size,omitempty"`
}

type fileItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type videoItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	ThumbMedia *cdnMedia `json:"thumb_media,omitempty"`
	VideoSize  int       `json:"video_size,omitempty"`
}

type voiceItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	Text       string    `json:"text,omitempty"`
	EncodeType int       `json:"encode_type,omitempty"`
}

type messageItem struct {
	Type      int        `json:"type,omitempty"`
	TextItem  *textItem  `json:"text_item,omitempty"`
	VoiceItem *voiceItem `json:"voice_item,omitempty"`
	ImageItem *imageItem `json:"image_item,omitempty"`
	FileItem  *fileItem  `json:"file_item,omitempty"`
	VideoItem *videoItem `json:"video_item,omitempty"`
}

type weixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	ItemList     []messageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

type getUploadURLRequest struct {
	Filekey     string `json:"filekey"`
	MediaType   int    `json:"media_type"`
	ToUserID    string `json:"to_user_id"`
	Rawsize     int    `json:"rawsize"`
	Rawfilemd5  string `json:"rawfilemd5"`
	Filesize    int    `json:"filesize"`
	NoNeedThumb bool   `json:"no_need_thumb"`
	Aeskey      string `json:"aeskey"`
}

type getUploadURLResponse struct {
	Ret           int    `json:"ret"`
	Errcode       int    `json:"errcode"`
	Errmsg        string `json:"errmsg"`
	UploadParam   string `json:"upload_param"`
	UploadFullURL string `json:"upload_full_url"`
}

type sendMessageReq struct {
	Msg      weixinOutboundMsg `json:"msg"`
	BaseInfo baseInfo          `json:"base_info"`
}

type sendMessageResp struct {
	Ret     int    `json:"ret"`
	Errcode int    `json:"errcode"`
	Errmsg  string `json:"errmsg"`
}

type weixinOutboundMsg struct {
	ToUserID     string        `json:"to_user_id"`
	ClientID     string        `json:"client_id"`
	MessageType  int           `json:"message_type"`
	MessageState int           `json:"message_state"`
	ItemList     []messageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}
