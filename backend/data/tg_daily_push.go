// Package data tg_daily_push.go
// 每日 Telegram 定时推送：
//  - 9:00  「盘前推荐」：拉前一交易日 14:30 之后入库的推荐 + 最新价（昨收盘价 or 盘前），算盈亏对比。本次不跑 AI。
//  - 14:30 「盘中复盘」：先跑一次 AI 推荐（短线选股助手 prompt + 工具调用 入库 ai_recommend_stocks），
//                       再跑一次涨停梯队 AI 复盘，最后把当天 14:30 之前的推荐 + 实时价 + 复盘摘要打包推送。
package data

import (
	"context"
	"fmt"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/duke-git/lancet/v2/slice"
)

// TGDailyPushMode 推送模式
type TGDailyPushMode string

const (
	TGModeMorning   TGDailyPushMode = "morning"   // 9:00 盘前
	TGModeAfternoon TGDailyPushMode = "afternoon" // 14:30 盘中
)

// AgentChatRunner 抽象 agent.NewStockAiAgentApi().ChatWithContext，避免 backend/data 直接依赖 backend/agent
// （agent 已经依赖 data，再反向引用会循环）
// 由 app.go 启动时通过 SetAgentChatRunner 注入实际实现
type AgentChatRunner func(ctx context.Context, question string, aiConfigId int, sysPromptId *int) <-chan *schema.Message

var agentChatRunner AgentChatRunner

// SetAgentChatRunner 由上层（main / app.go）注入跑 AI agent 的具体实现
func SetAgentChatRunner(r AgentChatRunner) {
	agentChatRunner = r
}

// RunTGDailyPush 编排某一次 9:00 / 14:30 推送，并写消息到 TG
// 任何一步失败都会返回错误，但前面已经完成的步骤不会回滚（写库是 append-only）
func RunTGDailyPush(ctx context.Context, mode TGDailyPushMode) (string, error) {
	cfg := GetSettingConfig()
	if cfg == nil || cfg.Settings == nil {
		return "", fmt.Errorf("settings 未初始化")
	}
	if !cfg.TgPushEnable {
		return "", fmt.Errorf("TG 推送未开启，跳过")
	}
	if strings.TrimSpace(cfg.TgBotToken) == "" || strings.TrimSpace(cfg.TgChatId) == "" {
		return "", fmt.Errorf("TG Bot Token / Chat ID 未配置")
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	stepLog := &strings.Builder{}
	stepLog.WriteString(fmt.Sprintf("TG 推送 mode=%s 时间=%s\n", mode, now.Format("2006-01-02 15:04:05")))

	switch mode {
	case TGModeAfternoon:
		// Step 1: 跑 AI 推荐（短线选股助手 + 用户提示词），让 AI 调 CreateAiRecommendStocks 工具入库
		if err := runAfternoonAIAnalysis(ctx, cfg); err != nil {
			logger.SugaredLogger.Warnf("[TG] 14:30 AI 推荐失败: %v（继续后续步骤）", err)
			stepLog.WriteString("⚠️ AI 推荐失败: " + err.Error() + "\n")
		} else {
			stepLog.WriteString("✅ AI 推荐已入库\n")
		}

		// Step 2: 跑涨停梯队复盘
		if _, err := AnalyzeUplimitWithAI(today, cfg.TgAiConfigId); err != nil {
			logger.SugaredLogger.Warnf("[TG] 14:30 涨停复盘失败: %v（继续后续步骤）", err)
			stepLog.WriteString("⚠️ 涨停复盘失败: " + err.Error() + "\n")
		} else {
			stepLog.WriteString("✅ 涨停复盘已入库\n")
		}

		// Step 3: 拼消息（取今天的推荐 + 实时价 + 最新复盘摘要）
		msg := buildAfternoonTGMessage(today, now)
		if err := SendTelegramMessage(msg); err != nil {
			return stepLog.String(), fmt.Errorf("发送 TG 失败: %w", err)
		}
		stepLog.WriteString("✅ TG 已发送\n")

	case TGModeMorning:
		// 9:00：纯回顾，对比前一交易日 14:30 之后的推荐 与 当前最新价
		prevDate := previousTradingDay(now)
		msg := buildMorningTGMessage(prevDate, now)
		if err := SendTelegramMessage(msg); err != nil {
			return stepLog.String(), fmt.Errorf("发送 TG 失败: %w", err)
		}
		stepLog.WriteString("✅ TG 已发送（盘前回顾）\n")

	default:
		return "", fmt.Errorf("未知模式: %s", mode)
	}
	return stepLog.String(), nil
}

// runAfternoonAIAnalysis 14:30 那次跑 AI 推荐：调 agent 跑短线选股助手 prompt
// 等流完全结束（AI 会在过程中调用 CreateAiRecommendStocks 工具入库）
func runAfternoonAIAnalysis(ctx context.Context, cfg *SettingConfig) error {
	if agentChatRunner == nil {
		return fmt.Errorf("agentChatRunner 未注入（请确保 main 启动时调用了 data.SetAgentChatRunner）")
	}

	// 用户自定义提问文本（自由输入），空则用默认
	userPrompt := strings.TrimSpace(cfg.TgUserPrompt)
	if userPrompt == "" {
		userPrompt = "根据今日的市场行情数据，帮我选出几只胜率高的股票推荐"
	}

	sysPromptId := cfg.TgSysPromptId

	// 跑一次完整对话；AI 在过程中会自动调用工具入库，我们这里只需要等它完成
	ctxRun, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	ch := agentChatRunner(ctxRun, userPrompt, cfg.TgAiConfigId, &sysPromptId)
	contentLen := 0
	for msg := range ch {
		if msg != nil {
			contentLen += len(msg.Content)
		}
	}
	logger.SugaredLogger.Infof("[TG] AI 推荐对话完成，content 长度=%d", contentLen)
	return nil
}

// buildAfternoonTGMessage 拼 14:30 那次的消息（含 AI 推荐 + 实时价 + 涨停复盘摘要）
func buildAfternoonTGMessage(today string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*📊 go-stock 14:30 盘中复盘 (%s)*\n\n", today))

	// 拉今天的推荐
	recs := queryTodayRecommendations(today, now)
	if len(recs) == 0 {
		sb.WriteString("_今天还没有 AI 推荐记录_\n\n")
	} else {
		// 刷一遍当前价
		enrichWithCurrentPrice(recs)

		sb.WriteString(fmt.Sprintf("*🤖 AI 推荐 (%d 只)*\n", len(recs)))
		for i, r := range recs {
			sb.WriteString(formatRecommendationLine(i+1, r, true))
		}
		sb.WriteString("\n")
	}

	// 最新涨停复盘摘要（今天的）
	if summary := getLatestUplimitSummary(today); summary != nil {
		sb.WriteString(fmt.Sprintf("*🎯 涨停梯队复盘 · %s*\n", summary.MarketSentiment))
		sb.WriteString(EscapeTGMarkdown(summary.Summary))
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("_推送时间: %s_", now.Format("2006-01-02 15:04:05")))
	return sb.String()
}

// buildMorningTGMessage 拼 9:00 那次的消息（回顾前一交易日 14:30 之后的推荐，对比最新价）
func buildMorningTGMessage(prevDate string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*🌅 go-stock 盘前回顾 (%s 推荐对比)*\n\n", prevDate))

	recs := queryRecommendationsAfter1430(prevDate)
	if len(recs) == 0 {
		sb.WriteString(fmt.Sprintf("_%s 14:30 后没有 AI 推荐记录_\n", prevDate))
		sb.WriteString(fmt.Sprintf("\n_推送时间: %s_", now.Format("2006-01-02 15:04:05")))
		return sb.String()
	}

	enrichWithCurrentPrice(recs)

	sb.WriteString(fmt.Sprintf("昨日 14:30 后 AI 共推荐 %d 只，对比当前最新价：\n\n", len(recs)))
	winCount, loseCount := 0, 0
	for i, r := range recs {
		line, pnl := formatRecommendationLineWithPnL(i+1, r)
		sb.WriteString(line)
		if pnl > 0 {
			winCount++
		} else if pnl < 0 {
			loseCount++
		}
	}
	sb.WriteString(fmt.Sprintf("\n*胜负统计*：✅ 盈利 %d 只 / ❌ 亏损 %d 只\n", winCount, loseCount))
	sb.WriteString(fmt.Sprintf("\n_推送时间: %s_", now.Format("2006-01-02 15:04:05")))
	return sb.String()
}

// queryTodayRecommendations 拉今天（today 当日，data_time BETWEEN 0:00~now）入库的推荐
func queryTodayRecommendations(today string, now time.Time) []models.AiRecommendStocks {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	start, _ := time.ParseInLocation("2006-01-02", today, loc)
	end := now

	var list []models.AiRecommendStocks
	err := db.Dao.Model(&models.AiRecommendStocks{}).
		Where("data_time BETWEEN ? AND ?", start, end).
		Order("created_at DESC").
		Limit(30).
		Find(&list).Error
	if err != nil {
		logger.SugaredLogger.Errorf("[TG] 查询今日推荐失败: %v", err)
		return nil
	}
	return list
}

// queryRecommendationsAfter1430 拉指定日期 14:30 之后入库的推荐
func queryRecommendationsAfter1430(date string) []models.AiRecommendStocks {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	start, err := time.ParseInLocation("2006-01-02 15:04:05", date+" 14:30:00", loc)
	if err != nil {
		return nil
	}
	end, _ := time.ParseInLocation("2006-01-02 15:04:05", date+" 23:59:59", loc)

	var list []models.AiRecommendStocks
	if e := db.Dao.Model(&models.AiRecommendStocks{}).
		Where("data_time BETWEEN ? AND ?", start, end).
		Order("created_at DESC").
		Limit(30).
		Find(&list).Error; e != nil {
		logger.SugaredLogger.Errorf("[TG] 查询昨日 14:30 后推荐失败: %v", e)
		return nil
	}
	return list
}

// enrichWithCurrentPrice 拉一次实时价填回 list
func enrichWithCurrentPrice(list []models.AiRecommendStocks) {
	if len(list) == 0 {
		return
	}
	codes := slice.Map(list, func(_ int, r models.AiRecommendStocks) string {
		return ConvertTushareCodeToStockCode(r.StockCode)
	})
	stockData, err := NewStockDataApi().GetStockCodeRealTimeData(codes...)
	if err != nil || stockData == nil {
		return
	}
	for _, info := range *stockData {
		infoKey := normalizeStockCodeForMatch(info.Code)
		for idx, item := range list {
			if normalizeStockCodeForMatch(item.StockCode) == infoKey {
				list[idx].StockCurrentPrice = info.Price
				list[idx].StockPrePrice = info.PreClose
				list[idx].StockCurrentPriceTime = info.Date + " " + info.Time
			}
		}
	}
}

// formatRecommendationLine 拼一行 TG 推荐：序号 / 名称 / 代码 / 评级 / 当前价 (涨跌%) / 买入区间
// withReason: 是否带"理由"，14:30 那次带，9:00 那次不带（用 PnL 行替代）
func formatRecommendationLine(idx int, r models.AiRecommendStocks, withReason bool) string {
	var sb strings.Builder
	name := EscapeTGMarkdown(r.StockName)
	code := EscapeTGMarkdown(r.StockCode)
	rating := EscapeTGMarkdown(r.Rating)
	curPrice := r.StockCurrentPrice
	prePrice := r.StockPrePrice

	changePct := ""
	if curF, err1 := strconv.ParseFloat(curPrice, 64); err1 == nil {
		if preF, err2 := strconv.ParseFloat(prePrice, 64); err2 == nil && preF > 0 {
			pct := (curF - preF) / preF * 100
			emoji := "📈"
			if pct < 0 {
				emoji = "📉"
			} else if pct == 0 {
				emoji = "➖"
			}
			changePct = fmt.Sprintf(" %s%+.2f%%", emoji, pct)
		}
	}

	priceStr := curPrice
	if priceStr == "" {
		priceStr = r.StockPrice
	}

	sb.WriteString(fmt.Sprintf("%d. *%s* (`%s`)  %s\n", idx, name, code, rating))
	sb.WriteString(fmt.Sprintf("   现价 `%s`%s", priceStr, changePct))
	if r.RecommendBuyPrice != "" {
		sb.WriteString(fmt.Sprintf("  · 建议买入 `%s`", r.RecommendBuyPrice))
	}
	sb.WriteString("\n")
	if withReason && strings.TrimSpace(r.RecommendReason) != "" {
		reason := r.RecommendReason
		if len(reason) > 80 {
			reason = reason[:80] + "…"
		}
		sb.WriteString("   💡 " + EscapeTGMarkdown(reason) + "\n")
	}
	return sb.String()
}

// formatRecommendationLineWithPnL 9:00 用：在普通行下方加一行"入手价 → 现价 → 涨跌%"
// 返回行文本 + 涨跌百分比（用于胜负统计），preprice 不可解析时返回 0
func formatRecommendationLineWithPnL(idx int, r models.AiRecommendStocks) (string, float64) {
	var sb strings.Builder
	name := EscapeTGMarkdown(r.StockName)
	code := EscapeTGMarkdown(r.StockCode)

	// 入手价用推荐时入库的 StockPrice，若空则取建议买入区间中位数
	entryPrice := r.StockPrice
	if entryPrice == "" || entryPrice == "0" {
		if r.RecommendBuyPriceMin > 0 && r.RecommendBuyPriceMax > 0 {
			entryPrice = fmt.Sprintf("%.2f", (r.RecommendBuyPriceMin+r.RecommendBuyPriceMax)/2)
		}
	}
	curPrice := r.StockCurrentPrice

	pnl := 0.0
	pnlStr := "?"
	if ef, err1 := strconv.ParseFloat(entryPrice, 64); err1 == nil && ef > 0 {
		if cf, err2 := strconv.ParseFloat(curPrice, 64); err2 == nil && cf > 0 {
			pnl = (cf - ef) / ef * 100
			emoji := "✅"
			if pnl < 0 {
				emoji = "❌"
			} else if pnl == 0 {
				emoji = "➖"
			}
			pnlStr = fmt.Sprintf("%s %+.2f%%", emoji, pnl)
		}
	}

	sb.WriteString(fmt.Sprintf("%d. *%s* (`%s`)\n", idx, name, code))
	sb.WriteString(fmt.Sprintf("   入手 `%s` → 现价 `%s`  %s\n", entryPrice, curPrice, pnlStr))
	return sb.String(), pnl
}

// getLatestUplimitSummary 取指定日期的最新一条涨停复盘摘要
func getLatestUplimitSummary(date string) *UplimitAISummary {
	var s UplimitAISummary
	err := db.Dao.Where("analyze_date = ?", date).Order("created_at DESC").First(&s).Error
	if err != nil {
		return nil
	}
	return &s
}

// previousTradingDay 简化版前一交易日：周一往前推 3 天，否则前 1 天。
// 不查交易日历，已经够这里的需求（用户连发两天 9:00 时同一周末数据不会被错误推送）
func previousTradingDay(now time.Time) string {
	d := now
	d = d.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Format("2006-01-02")
}

// TGPushParams cron 任务参数 JSON 反序列化用
type TGPushParams struct {
	Mode string `json:"mode"`
}

// sortRecommendationsByDataTime DESC（默认 DB Order 已经 DESC，留接口给可能的别处排序）
var _ = func() func([]models.AiRecommendStocks) {
	return func(list []models.AiRecommendStocks) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].DataTime == nil || list[j].DataTime == nil {
				return false
			}
			return list[i].DataTime.After(*list[j].DataTime)
		})
	}
}
