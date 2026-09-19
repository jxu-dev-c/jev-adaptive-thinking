package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type message struct {
	Role    string          `json:"role"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

func protocol(format string) string {
	switch strings.ToLower(format) {
	case "openai", "chat-completions":
		return "chat-completions"
	case "openai-response", "openai-responses", "responses":
		return "responses"
	case "claude", "anthropic":
		return "claude"
	default:
		return "unsupported"
	}
}

// firstPrompt never searches past the first actual user message to classify a
// later task. Tool-result-only messages are transport continuations, not prompts.
func firstPrompt(body []byte, format string) (string, string) {
	var root struct {
		Messages []message       `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if !json.Valid(body) || json.Unmarshal(body, &root) != nil {
		return "", "invalid_body"
	}
	messages := root.Messages
	if protocol(format) == "responses" {
		var text string
		if json.Unmarshal(root.Input, &text) == nil && len(root.Input) > 0 && string(root.Input) != "null" {
			return boundedText(text)
		}
		if json.Unmarshal(root.Input, &messages) != nil {
			return "", "missing_prompt"
		}
	}
	for _, m := range messages {
		if m.Role != "user" || (m.Type != "" && m.Type != "message") {
			continue
		}
		text, reason, toolOnly := userContent(m.Content)
		if toolOnly {
			continue
		}
		return text, reason
	}
	return "", "missing_prompt"
}

func userContent(raw json.RawMessage) (string, string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		t, reason := boundedText(text)
		return t, reason, false
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", "missing_prompt", false
	}
	var texts []string
	tools := 0
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			texts = append(texts, p.Text)
		case "tool_result":
			tools++
		default:
			return "", "multimodal_prompt", false
		}
	}
	if tools == len(parts) {
		return "", "", true
	}
	t, reason := boundedText(strings.Join(texts, "\n"))
	return t, reason, false
}

func boundedText(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "missing_prompt"
	}
	// Only the classification copy is truncated, never the forwarded request.
	if utf8.RuneCountInString(text) > 8000 {
		text = string([]rune(text)[:8000])
	}
	return text, ""
}

func header(h http.Header, names ...string) string {
	for _, name := range names {
		for key, values := range h {
			if strings.EqualFold(key, name) && len(values) > 0 {
				if id := validID(values[0]); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func validID(s string) string {
	if len(s) > 256 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return ""
	}
	return strings.TrimSpace(s)
}

var legacyClaudeID = regexp.MustCompile(`_session_([a-fA-F0-9-]+)$`)

func sessionKey(req pluginapi.ModelRouteRequest) string {
	var root map[string]json.RawMessage
	_ = json.Unmarshal(req.Body, &root)
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(root["metadata"], &meta)
	get := func(m map[string]json.RawMessage, names ...string) string {
		for _, name := range names {
			var s string
			if json.Unmarshal(m[name], &s) == nil && validID(s) != "" {
				return validID(s)
			}
		}
		return ""
	}
	var claudeMeta map[string]json.RawMessage
	// metadata.user_id is accepted only when it encodes a Claude session, never
	// as a plain user identifier shared by several conversations.
	var userID string
	_ = json.Unmarshal(meta["user_id"], &userID)
	_ = json.Unmarshal([]byte(userID), &claudeMeta)
	claudeID := get(claudeMeta, "session_id")
	if claudeID == "" {
		if match := legacyClaudeID.FindStringSubmatch(userID); len(match) == 2 {
			claudeID = validID(match[1])
		}
	}
	id := header(req.Headers, "X-Claude-Code-Session-Id")
	if id == "" {
		id = claudeID
	}
	if id == "" {
		id = header(req.Headers, "Session-Id", "Session_id", "X-Session-ID", "X-Conversation-Id", "X-Thread-Id", "Thread-Id", "Thread_id")
	}
	if id == "" {
		id = get(root, "session_id", "sessionId", "thread_id", "conversation_id")
	}
	if id == "" {
		id = get(meta, "session_id", "sessionId", "thread_id", "conversation_id")
	}
	if id == "" {
		var conversation map[string]json.RawMessage
		_ = json.Unmarshal(root["conversation"], &conversation)
		id = get(conversation, "id")
	}
	if id == "" {
		return ""
	}
	agent := header(req.Headers, "X-Claude-Code-Agent-Id")
	if agent == "" {
		agent = get(meta, "agent_id", "subagent_id")
	}
	if agent == "" {
		agent = get(claudeMeta, "agent_id", "subagent_id")
	}
	if agent == "main" {
		agent = ""
	}
	scope, _ := req.Metadata["caller_scope"].(string)
	if scope == "" {
		// Only the hash is retained; the credential is never logged or stored.
		for _, name := range []string{"Authorization", "X-Api-Key"} {
			for key, values := range req.Headers {
				if strings.EqualFold(key, name) && len(values) > 0 {
					scope = values[0]
					break
				}
			}
			if scope != "" {
				break
			}
		}
	}
	encoded, _ := json.Marshal([]string{"jev-session-v1", scope, id, agent})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
