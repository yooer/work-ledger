package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// AIClient 调用 OpenAI Chat Completions 兼容接口。
type AIClient struct{ *Core }

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (a *AIClient) Configured() bool {
	return a.Cfg.AI.APIKey != "" && a.Cfg.AI.BaseURL != "" && a.Cfg.AI.Model != ""
}

func (a *AIClient) Model() string { return a.Cfg.AI.Model }

// endpoint 若 base_url 已以 /chat/completions 结尾则直接使用。
func (a *AIClient) endpoint() string {
	base := strings.TrimRight(a.Cfg.AI.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// httpDo 便于测试替换。
var aiHTTPClient = &http.Client{}

// Chat 发送消息并返回文本内容。
func (a *AIClient) Chat(ctx context.Context, messages []ChatMessage) (string, error) {
	if !a.Configured() {
		return "", fmt.Errorf("尚未配置 AI_API_KEY / AI_BASE_URL / AI_MODEL")
	}
	payload, err := json.Marshal(map[string]any{
		"model": a.Cfg.AI.Model, "messages": messages, "temperature": 0.25, "stream": false,
	})
	if err != nil {
		return "", fmt.Errorf("构建 AI 请求失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(a.Cfg.AI.Timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.Cfg.AI.APIKey)
	req.Header.Set("User-Agent", "WorkLedger/2.0")
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 AI 服务失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("读取 AI 响应失败：%v", err)
	}
	var decoded struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("AI 服务返回了无法解析的内容（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if decoded.Error != nil && decoded.Error.Message != "" {
			msg = decoded.Error.Message
		}
		return "", fmt.Errorf("AI 服务返回错误：%s", shorten(msg, 300))
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("AI 服务没有返回总结内容")
	}
	content := extractContent(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("AI 服务没有返回总结内容")
	}
	return content, nil
}

// extractContent 兼容字符串或 [{type,text}] 数组形式。
func extractContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.TrimSpace(strings.Join(out, "\n"))
	}
	return ""
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
