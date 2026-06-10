// Package data tool_telegram.go
// 调用 https://api.telegram.org Bot API 发送消息。
// 由 tg_daily_push.go 编排"每日定时推送当日 AI 推荐股票 + 涨停复盘"。
package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go-stock/backend/logger"
	"io"
	"net/http"
	netUrl "net/url"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org"

// SendTelegramMessage 把 Markdown 格式文本发送给配置里的 chat_id
// 国内访问 api.telegram.org 通常需要代理：如果 Settings.TgUseProxy=true 且 HttpProxy 已设，自动走代理。
// 不强依赖 SettingConfig 之外的全局状态，方便定时任务里直接调。
func SendTelegramMessage(text string) error {
	cfg := GetSettingConfig()
	if cfg == nil || cfg.Settings == nil {
		return fmt.Errorf("settings 未初始化")
	}
	if !cfg.TgPushEnable {
		return fmt.Errorf("TG 推送未开启")
	}
	token := strings.TrimSpace(cfg.TgBotToken)
	chatId := strings.TrimSpace(cfg.TgChatId)
	if token == "" || chatId == "" {
		return fmt.Errorf("TG 配置不完整：请填写 Bot Token 和 Chat ID")
	}

	// 分片：TG 单条消息上限 4096 字符
	chunks := splitTGText(text, 3800)
	for i, chunk := range chunks {
		if err := postTelegramSendMessage(token, chatId, chunk, cfg); err != nil {
			return fmt.Errorf("分片 %d/%d 发送失败: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

// SendTelegramMessageTo 用指定 token/chatId 发送一条消息，便于测试按钮直接传入未保存的输入框值
func SendTelegramMessageTo(token, chatId, text string, useProxy bool) error {
	token = strings.TrimSpace(token)
	chatId = strings.TrimSpace(chatId)
	if token == "" || chatId == "" {
		return fmt.Errorf("Bot Token 或 Chat ID 为空")
	}
	cfg := GetSettingConfig()
	if cfg == nil || cfg.Settings == nil {
		return fmt.Errorf("settings 未初始化")
	}
	// 临时构造一份覆盖了代理开关的副本，供 postTelegramSendMessage 使用
	tmp := *cfg.Settings
	tmp.TgUseProxy = useProxy
	wrapper := &SettingConfig{Settings: &tmp, AiConfigs: cfg.AiConfigs}
	return postTelegramSendMessage(token, chatId, text, wrapper)
}

func postTelegramSendMessage(token, chatId, text string, cfg *SettingConfig) error {
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)

	reqBody := map[string]any{
		"chat_id":                  chatId,
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg.TgUseProxy && cfg.HttpProxy != "" {
		proxyURL, err := netUrl.Parse(cfg.HttpProxy)
		if err != nil {
			logger.SugaredLogger.Warnf("TG 代理 URL 解析失败，回退直连: %v", err)
		} else {
			httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("调用 TG API 失败（多半是网络/代理问题）: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		// Markdown 解析失败时 TG 返回 400，自动回退到纯文本再发一次
		if resp.StatusCode == 400 && strings.Contains(string(respBytes), "can't parse entities") {
			logger.SugaredLogger.Warnf("TG Markdown 解析失败，回退纯文本: %s", string(respBytes))
			return postTelegramPlainText(httpClient, token, chatId, text)
		}
		return fmt.Errorf("TG HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	// 解析 ok 字段
	var parsed struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(respBytes, &parsed)
	if !parsed.Ok {
		return fmt.Errorf("TG 返回 ok=false: %s", parsed.Description)
	}
	return nil
}

func postTelegramPlainText(client *http.Client, token, chatId, text string) error {
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
	reqBody := map[string]any{
		"chat_id":                  chatId,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TG 纯文本回退 HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// splitTGText 把过长的消息按段落边界拆成多条，避免超 4096 字符限制
func splitTGText(text string, max int) []string {
	if len(text) <= max {
		return []string{text}
	}
	var chunks []string
	for len(text) > max {
		// 尽量从换行处切，找最后一个 \n
		cut := strings.LastIndex(text[:max], "\n")
		if cut <= 0 {
			cut = max
		}
		chunks = append(chunks, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if len(text) > 0 {
		chunks = append(chunks, text)
	}
	return chunks
}

// EscapeTGMarkdown TG Markdown 不允许 _*[]() 等字符出现在普通文本里，转义掉它们
// 用在动态拼接的股票名/代码/数字等位置；标题这种确定不会冲突的就别转义了
func EscapeTGMarkdown(s string) string {
	// 注意：TG 的 Markdown（不是 MarkdownV2）只对 *_`[ 这几个敏感，保守一点全转
	replacer := strings.NewReplacer(
		"_", `\_`,
		"*", `\*`,
		"[", `\[`,
		"`", `\` + "`",
	)
	return replacer.Replace(s)
}
