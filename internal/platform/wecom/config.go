package wecom

import (
	"fmt"
	"strings"
)

// config holds parsed options for the WeCom platform.
type config struct {
	botID           string
	secret          string
	websocketURL    string
	allowFrom       string
	groupAllowFrom  string
	stream          bool
	footer          bool
	thinkingDisplay string // "concise", "detailed", or "off"
	toolDisplay     string // "concise", "detailed", or "off"
}

func parseConfig(opts map[string]any) (*config, error) {
	botID, _ := opts["bot_id"].(string)
	secret, _ := opts["secret"].(string)
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("wecom: bot_id and secret are required")
	}

	websocketURL, _ := opts["websocket_url"].(string)
	if strings.TrimSpace(websocketURL) == "" {
		websocketURL = "wss://openws.work.weixin.qq.com"
	}

	allowFrom, _ := opts["allow_from"].(string)
	groupAllowFrom, _ := opts["group_allow_from"].(string)
	stream := true
	if value, exists := opts["stream"]; exists {
		var ok bool
		stream, ok = value.(bool)
		if !ok {
			return nil, fmt.Errorf("wecom: stream must be a boolean")
		}
	}
	footer := true
	if value, exists := opts["footer"]; exists {
		var ok bool
		footer, ok = value.(bool)
		if !ok {
			return nil, fmt.Errorf("wecom: footer must be a boolean")
		}
	}
	thinkingDisplay, _ := opts["thinking_display"].(string)
	thinkingDisplay = strings.ToLower(strings.TrimSpace(thinkingDisplay))
	if thinkingDisplay == "" {
		thinkingDisplay = "concise"
	}
	switch thinkingDisplay {
	case "concise", "detailed", "off":
	default:
		return nil, fmt.Errorf("wecom: thinking_display must be concise, detailed, or off")
	}

	toolDisplay, _ := opts["tool_display"].(string)
	toolDisplay = strings.ToLower(strings.TrimSpace(toolDisplay))
	if toolDisplay == "" {
		toolDisplay = "concise"
	}
	switch toolDisplay {
	case "concise", "detailed", "off":
	default:
		return nil, fmt.Errorf("wecom: tool_display must be concise, detailed, or off")
	}

	return &config{
		botID:           strings.TrimSpace(botID),
		secret:          strings.TrimSpace(secret),
		websocketURL:    strings.TrimSpace(websocketURL),
		allowFrom:       strings.TrimSpace(allowFrom),
		groupAllowFrom:  strings.TrimSpace(groupAllowFrom),
		stream:          stream,
		footer:          footer,
		thinkingDisplay: thinkingDisplay,
		toolDisplay:     toolDisplay,
	}, nil
}

func (c *config) allowUser(userID string) bool {
	return allowList(c.allowFrom, userID)
}

func (c *config) allowGroup(chatID string) bool {
	return allowList(c.groupAllowFrom, chatID)
}

// allowList checks whether id is in the comma-separated allow list.
func allowList(allowFrom, id string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" || allowFrom == "*" {
		return true
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, entry := range strings.Split(allowFrom, ",") {
		if strings.TrimSpace(entry) == id {
			return true
		}
	}
	return false
}
