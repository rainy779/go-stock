// Package data uplimit_ai_analyze.go
// 涨停梯队 AI 一键复盘分析：拉数据 → 构造 prompt → 调 AI → 解析 JSON → 写库
package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
	"net/http"
	netUrl "net/url"
	"sort"
	"strings"
	"time"

	"github.com/duke-git/lancet/v2/convertor"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
)

// UplimitAISummary 涨停梯队 AI 复盘历史记录（持久化保存，关闭弹窗后可重新查看）
type UplimitAISummary struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	AnalyzeDate     string    `gorm:"size:20;index" json:"analyzeDate"`     // 复盘的日期（数据日期）
	AnalyzeTime     string    `gorm:"size:30" json:"analyzeTime"`           // 实际生成时间
	ModelName       string    `gorm:"size:100" json:"modelName"`            // 用的哪个 AI 模型
	MarketSentiment string    `gorm:"size:20" json:"marketSentiment"`       // 极强/强/中性偏强/中性/偏冷
	Summary         string    `gorm:"type:text" json:"summary"`             // 200 字摘要
	RawJSON         string    `gorm:"type:text" json:"rawJson"`             // AI 完整返回 JSON（含推荐）方便重新展示
	SavedCount      int       `json:"savedCount"`                           // 入库推荐条数
}

func (UplimitAISummary) TableName() string { return "uplimit_ai_summaries" }

// UplimitAIRecommendation Claude 输出的单个股票推荐
type UplimitAIRecommendation struct {
	StockCode     string  `json:"stockCode"`
	StockName     string  `json:"stockName"`
	Rating        string  `json:"rating"`
	Reason        string  `json:"reason"`
	BuyPriceMin   float64 `json:"buyPriceMin"`
	BuyPriceMax   float64 `json:"buyPriceMax"`
	StopProfitMin float64 `json:"stopProfitMin"`
	StopProfitMax float64 `json:"stopProfitMax"`
	StopLoss      float64 `json:"stopLoss"`
	Risk          string  `json:"risk"`
}

// UplimitAIAnalysisResult Claude 输出的完整复盘结构
type UplimitAIAnalysisResult struct {
	MarketSentiment string                    `json:"marketSentiment"`
	Summary         string                    `json:"summary"`
	Recommendations []UplimitAIRecommendation `json:"recommendations"`
}

// AnalyzeUplimitWithAI 一键复盘主流程
func AnalyzeUplimitWithAI(date string, aiConfigID int) (map[string]any, error) {
	// 1. 拉涨停梯队数据
	if date == "" {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		date = time.Now().In(loc).Format("2006-01-02")
	}
	rawData := NewMarketNewsApi().GetUplimitHot(date, 20)
	if rawData == nil || rawData["code"] == nil {
		return nil, fmt.Errorf("获取涨停梯队数据失败")
	}
	code, _ := rawData["code"].(float64)
	if int(code) != 20000 {
		msg, _ := rawData["message"].(string)
		return nil, fmt.Errorf("获取涨停梯队数据失败: %s", msg)
	}
	dataMap, ok := rawData["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("涨停梯队数据格式异常")
	}

	// 2. 构造 prompt
	prompt := buildUplimitAnalysisPrompt(dataMap, date)
	logger.SugaredLogger.Infof("AnalyzeUplimitWithAI prompt len=%d", len(prompt))

	// 3. 调 AI (取 AI 配置)
	settingConfig := GetSettingConfig()
	aiConfig, found := lo.Find(settingConfig.AiConfigs, func(item *AIConfig) bool {
		return uint(aiConfigID) == item.ID
	})
	if !found {
		// 没指定就用第一个
		if len(settingConfig.AiConfigs) == 0 {
			return nil, fmt.Errorf("未配置任何 AI，请先在「设置 → AI 设置」中配置 Claude/LiteLLM")
		}
		aiConfig = settingConfig.AiConfigs[0]
	}

	aiContent, err := callAIChatCompletion(aiConfig, prompt)
	if err != nil {
		return nil, fmt.Errorf("调用 AI 失败: %w", err)
	}

	// 4. 解析 JSON（AI 输出可能带 markdown 代码块包裹，要先剥掉）
	cleanJSON := extractJSONFromAIResponse(aiContent)
	var result UplimitAIAnalysisResult
	if err := json.Unmarshal([]byte(cleanJSON), &result); err != nil {
		logger.SugaredLogger.Errorf("AI 输出 JSON 解析失败: %v\n原始输出: %s", err, aiContent)
		return nil, fmt.Errorf("AI 返回的 JSON 解析失败，请重试或换更强模型: %w", err)
	}

	// 5. 写入 ai_recommend_stocks 表
	savedCount := saveUplimitRecommendations(result.Recommendations, aiConfig.ModelName, date)

	// 6. 写入复盘摘要表（关掉弹窗后可重新查看）
	_ = db.Dao.AutoMigrate(&UplimitAISummary{}) // 幂等，首次调用时建表
	analyzeTime := time.Now().Format("2006-01-02 15:04:05")
	rawJSON, _ := json.Marshal(result)
	summary := &UplimitAISummary{
		AnalyzeDate:     date,
		AnalyzeTime:     analyzeTime,
		ModelName:       aiConfig.ModelName,
		MarketSentiment: result.MarketSentiment,
		Summary:         result.Summary,
		RawJSON:         string(rawJSON),
		SavedCount:      savedCount,
	}
	if err := db.Dao.Create(summary).Error; err != nil {
		logger.SugaredLogger.Warnf("保存复盘摘要失败: %v", err)
	}

	// 7. 返回结果给前端
	return map[string]any{
		"id":              summary.ID,
		"marketSentiment": result.MarketSentiment,
		"summary":         result.Summary,
		"recommendations": result.Recommendations,
		"savedCount":      savedCount,
		"modelName":       aiConfig.ModelName,
		"analyzeDate":     date,
		"analyzeTime":     analyzeTime,
	}, nil
}

// GetUplimitAISummaries 获取最近 N 条复盘摘要（按时间倒序）
func GetUplimitAISummaries(limit int) []UplimitAISummary {
	_ = db.Dao.AutoMigrate(&UplimitAISummary{})
	if limit <= 0 {
		limit = 20
	}
	var summaries []UplimitAISummary
	db.Dao.Order("created_at DESC").Limit(limit).Find(&summaries)
	return summaries
}

// GetUplimitAISummaryDetail 获取某条复盘摘要的完整数据（含 recommendations 反序列化）
// 返回结构跟 AnalyzeUplimitWithAI 一致，可直接喂给前端弹窗
func GetUplimitAISummaryDetail(id uint) (map[string]any, error) {
	var s UplimitAISummary
	if err := db.Dao.First(&s, id).Error; err != nil {
		return nil, err
	}
	var raw UplimitAIAnalysisResult
	_ = json.Unmarshal([]byte(s.RawJSON), &raw)
	return map[string]any{
		"id":              s.ID,
		"marketSentiment": s.MarketSentiment,
		"summary":         s.Summary,
		"recommendations": raw.Recommendations,
		"savedCount":      s.SavedCount,
		"modelName":       s.ModelName,
		"analyzeDate":     s.AnalyzeDate,
		"analyzeTime":     s.AnalyzeTime,
	}, nil
}

// DeleteUplimitAISummary 删除某条复盘摘要
func DeleteUplimitAISummary(id uint) error {
	return db.Dao.Delete(&UplimitAISummary{}, id).Error
}

// buildUplimitAnalysisPrompt 把涨停梯队数据拼成 prompt
func buildUplimitAnalysisPrompt(dataMap map[string]any, date string) string {
	var sb strings.Builder
	sb.WriteString("你是一个资深 A 股短线交易复盘专家。基于以下今日涨停梯队数据，给出客观复盘和股票推荐。\n\n")
	sb.WriteString(fmt.Sprintf("【日期】%s\n", date))

	// 涨停统计
	stocksStr, _ := dataMap["stocks"].(string)
	allCodes := []string{}
	for _, c := range strings.Split(stocksStr, ",") {
		if strings.TrimSpace(c) != "" {
			allCodes = append(allCodes, strings.TrimSpace(c))
		}
	}
	totalZt := len(allCodes)
	maxCount := int(convertor.ToString(dataMap["max_count"])[0]) - '0' // safe parse
	if maxCount < 0 {
		maxCount = 0
	}
	if v, ok := dataMap["max_count"].(float64); ok {
		maxCount = int(v)
	}

	// 炸板统计
	plateStocksZb, _ := dataMap["plate_stocks_zb"].(map[string]any)
	explodedSet := map[string]bool{}
	for _, sv := range plateStocksZb {
		if arr, ok := sv.([]any); ok {
			for _, s := range arr {
				if sm, ok := s.(map[string]any); ok {
					if code, _ := sm["stock_code"].(string); code != "" {
						explodedSet[code] = true
					}
				}
			}
		}
	}
	explodedCount := len(explodedSet)
	explodedRatio := 0
	if totalZt+explodedCount > 0 {
		explodedRatio = int(float64(explodedCount) / float64(totalZt+explodedCount) * 100)
	}

	sb.WriteString(fmt.Sprintf("【市场温度】总涨停 %d 只 | 最高 %d 板 | 炸板 %d 只 | 炸板率 %d%%\n", totalZt, maxCount, explodedCount, explodedRatio))

	// 连板分布
	banInfo, _ := dataMap["ban_info"].(map[string]any)
	if len(banInfo) > 0 {
		sb.WriteString("【连板分布】")
		for i := maxCount; i >= 1; i-- {
			if info, ok := banInfo[fmt.Sprintf("%d", i)].(map[string]any); ok {
				cnt, _ := info["count"].(float64)
				sb.WriteString(fmt.Sprintf("%d板(%d) ", i, int(cnt)))
			}
		}
		sb.WriteString("\n")
	}

	// 主线 TOP 5
	plateArr, _ := dataMap["plate"].([]any)
	plateInfoMap, _ := dataMap["plate_info"].(map[string]any)
	plateStocks, _ := dataMap["plate_stocks"].(map[string]any)
	sb.WriteString("【主线 TOP 5】\n")
	for i, p := range plateArr {
		if i >= 5 {
			break
		}
		if arr, ok := p.([]any); ok && len(arr) >= 3 {
			name, _ := arr[0].(string)
			code, _ := arr[1].(string)
			score, _ := arr[2].(float64)
			ztCount := 0
			if zts, ok := plateStocks[code].([]any); ok {
				ztCount = len(zts)
			}
			zbCount := 0
			if zbs, ok := plateStocksZb[code].([]any); ok {
				zbCount = len(zbs)
			}
			sb.WriteString(fmt.Sprintf("%d. %s 热度%d 涨停%d只 炸板%d只\n", i+1, name, int(score), ztCount, zbCount))
		}
	}

	// 接力主线
	if relay, ok := dataMap["relay"].(map[string]any); ok {
		if relayArea, ok := relay["area"].([]any); ok && len(relayArea) > 0 {
			relayNames := []string{}
			for i, ra := range relayArea {
				if i >= 5 {
					break
				}
				if ram, ok := ra.(map[string]any); ok {
					pcode, _ := ram["p_code"].(string)
					if pinfo, ok := plateInfoMap[pcode].(map[string]any); ok {
						pname, _ := pinfo["name"].(string)
						relayNames = append(relayNames, pname)
					}
				}
			}
			if len(relayNames) > 0 {
				sb.WriteString("【接力主线】" + strings.Join(relayNames, "、") + "\n")
			}
		}
	}

	// 龙头候选 TOP 10：热度高+在主线
	mainPlateNames := map[string]bool{}
	for i, p := range plateArr {
		if i >= 5 {
			break
		}
		if arr, ok := p.([]any); ok && len(arr) >= 1 {
			if name, _ := arr[0].(string); name != "" {
				mainPlateNames[name] = true
			}
		}
	}
	stocksHot, _ := dataMap["stocks_hot"].(map[string]any)
	stockInfo, _ := dataMap["stock_info"].(map[string]any)

	type hotEntry struct {
		Code     string
		Name     string
		Score    float64
		KTTimes  int
		Plates   []string
		InMain   bool
	}
	hotList := []hotEntry{}
	for code, scoreVal := range stocksHot {
		score, _ := scoreVal.(float64)
		name := getStockNameFromPlateStocks(plateStocks, code)
		ktTimes := 0
		for _, ps := range plateStocks {
			if arr, ok := ps.([]any); ok {
				for _, s := range arr {
					if sm, ok := s.(map[string]any); ok && sm["stock_code"] == code {
						if v, ok := sm["up_limit_keep_times"].(float64); ok {
							ktTimes = int(v)
						}
						break
					}
				}
			}
			if ktTimes > 0 {
				break
			}
		}
		plates := []string{}
		inMain := false
		if si, ok := stockInfo[code].(map[string]any); ok {
			if pa, ok := si["plates"].([]any); ok {
				for _, p := range pa {
					pname := fmt.Sprintf("%v", p)
					plates = append(plates, pname)
					if mainPlateNames[pname] {
						inMain = true
					}
				}
			}
		}
		hotList = append(hotList, hotEntry{Code: code, Name: name, Score: score, KTTimes: ktTimes, Plates: plates, InMain: inMain})
	}
	sort.Slice(hotList, func(i, j int) bool {
		return hotList[i].Score > hotList[j].Score
	})
	sb.WriteString("【龙头候选 TOP 10】（热度 + 连板 + 主线匹配）\n")
	for i, h := range hotList {
		if i >= 10 {
			break
		}
		mainMark := ""
		if h.InMain {
			mainMark = " [主线✓]"
		}
		plateStr := strings.Join(h.Plates, ",")
		if len(plateStr) > 60 {
			plateStr = plateStr[:60] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s(%s) %d连板 热度%d%s 概念:%s\n", h.Name, h.Code, h.KTTimes, int(h.Score), mainMark, plateStr))
	}

	// 炸板预警（前期连板炸板）
	dangerExploded := []string{}
	for _, sv := range plateStocksZb {
		if arr, ok := sv.([]any); ok {
			for _, s := range arr {
				if sm, ok := s.(map[string]any); ok {
					if kt, ok := sm["up_limit_keep_times"].(float64); ok && int(kt) >= 2 {
						name, _ := sm["stock_name"].(string)
						code, _ := sm["stock_code"].(string)
						dangerExploded = append(dangerExploded, fmt.Sprintf("%s(%s)%d板炸", name, code, int(kt)))
					}
				}
			}
		}
	}
	if len(dangerExploded) > 0 {
		sb.WriteString("【炸板预警 - 前期连板今日炸板】\n")
		for i, s := range dangerExploded {
			if i >= 5 {
				break
			}
			sb.WriteString("- " + s + "\n")
		}
	}

	// 输出要求
	sb.WriteString("\n请严格输出以下 JSON（**只输出 JSON，不要 markdown 代码块标记**，不要其他任何文字）:\n")
	sb.WriteString(`{
  "marketSentiment": "极强 | 强 | 中性偏强 | 中性 | 偏冷",
  "summary": "200字内复盘摘要：包含市场情绪判断、主线方向、龙头股、明日策略",
  "recommendations": [
    {
      "stockCode": "股票代码必须带交易所后缀，如 688256.SH / 000811.SZ / 002364.SZ（沪市6开头.SH，深市0/3开头.SZ，北交所8/4开头.BJ）",
      "stockName": "股票名称",
      "rating": "买入 | 增持 | 中性",
      "reason": "推荐理由 50 字内",
      "buyPriceMin": 0.0,
      "buyPriceMax": 0.0,
      "stopProfitMin": 0.0,
      "stopProfitMax": 0.0,
      "stopLoss": 0.0,
      "risk": "风险点 30 字内"
    }
  ]
}`)
	sb.WriteString("\n\n要求：输出 3-5 只推荐，从龙头候选 TOP 10 里选。注意是 A 股，不要推港股美股。")
	return sb.String()
}

// normalizeAShareCode 把 AI 输出的裸数字代码补上 .SH/.SZ/.BJ 后缀
// 兼容: "000811" / "000811.SZ" / "sz000811" / "SZ000811"
// 输出统一为 Tushare 格式: "000811.SZ"
func normalizeAShareCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return code
	}
	// 如果是 hk/us/HK/US 前缀 → 不动
	lower := strings.ToLower(code)
	if strings.HasPrefix(lower, "hk") || strings.HasPrefix(lower, "us") || strings.HasPrefix(lower, "gb_") {
		return code
	}
	// 已经是 Tushare 格式 (含 .SH/.SZ/.BJ)
	if strings.Contains(code, ".") {
		return strings.ToUpper(code)
	}
	// 带 sh/sz/bj 前缀 → 转成 Tushare 格式
	if len(code) >= 8 && (strings.HasPrefix(lower, "sh") || strings.HasPrefix(lower, "sz") || strings.HasPrefix(lower, "bj")) {
		prefix := strings.ToUpper(lower[:2])
		digits := code[2:]
		return digits + "." + prefix
	}
	// 纯 6 位数字 → 根据首位猜市场
	if len(code) == 6 {
		switch code[0] {
		case '0', '2', '3':
			return code + ".SZ"
		case '6':
			return code + ".SH"
		case '4', '8', '9':
			return code + ".BJ"
		}
	}
	return code
}

// getStockNameFromPlateStocks 从 plate_stocks 嵌套结构里按 code 找 stock_name
func getStockNameFromPlateStocks(plateStocks map[string]any, code string) string {
	for _, pStocks := range plateStocks {
		if arr, ok := pStocks.([]any); ok {
			for _, s := range arr {
				if sm, ok := s.(map[string]any); ok {
					if sm["stock_code"] == code {
						name, _ := sm["stock_name"].(string)
						return name
					}
				}
			}
		}
	}
	return ""
}

// callAIChatCompletion 同步调 OpenAI 兼容协议 /chat/completions（用户的 LiteLLM/Claude 走这条）
func callAIChatCompletion(aiConfig *AIConfig, prompt string) (string, error) {
	baseURL := strings.TrimRight(aiConfig.BaseUrl, "/")
	url := baseURL + "/chat/completions"

	reqBody := map[string]any{
		"model": aiConfig.ModelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  4000,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	timeout := time.Duration(120) * time.Second
	if aiConfig.TimeOut > 0 {
		timeout = time.Duration(aiConfig.TimeOut) * time.Second
	}

	httpClient := &http.Client{Timeout: timeout}
	// 如果 AI 配置启用 http 代理
	if aiConfig.HttpProxyEnabled && aiConfig.HttpProxy != "" {
		if proxyURL, err := netUrl.Parse(aiConfig.HttpProxy); err == nil {
			httpClient = &http.Client{
				Timeout: timeout,
				Transport: &http.Transport{
					Proxy: http.ProxyURL(proxyURL),
				},
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiConfig.ApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes := new(bytes.Buffer)
	_, _ = respBytes.ReadFrom(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("AI HTTP %d: %s", resp.StatusCode, respBytes.String())
	}

	// 用 gjson 提取 content
	content := gjson.Get(respBytes.String(), "choices.0.message.content").String()
	if content == "" {
		return "", fmt.Errorf("AI 响应无内容: %s", respBytes.String())
	}
	return content, nil
}

// extractJSONFromAIResponse AI 可能用 ```json ... ``` 包裹，剥掉
func extractJSONFromAIResponse(content string) string {
	content = strings.TrimSpace(content)
	// 去除 markdown 代码块
	if strings.HasPrefix(content, "```") {
		// 找第一个换行（跳过 ```json 这一行）
		idx := strings.Index(content, "\n")
		if idx > 0 {
			content = content[idx+1:]
		}
		// 去掉结尾 ```
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
		content = strings.TrimSpace(content)
	}
	// 找第一个 { 和最后一个 }
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return content[start : end+1]
	}
	return content
}

// saveUplimitRecommendations 把 AI 推荐写入 ai_recommend_stocks 表
// 1) 先规范化代码 (000811 → 000811.SZ)
// 2) 拉当前价填进去
func saveUplimitRecommendations(recs []UplimitAIRecommendation, modelName, date string) int {
	if len(recs) == 0 {
		return 0
	}
	now := time.Now()

	// 1. 规范化代码为 Tushare 格式（带 .SH/.SZ/.BJ 后缀）
	for i := range recs {
		recs[i].StockCode = normalizeAShareCode(recs[i].StockCode)
	}

	// 2. 拉当前价（用规范化后的代码）
	apiCodes := lo.Map(recs, func(r UplimitAIRecommendation, _ int) string {
		return ConvertTushareCodeToStockCode(r.StockCode)
	})
	logger.SugaredLogger.Infof("saveUplimitRecommendations apiCodes: %v", apiCodes)

	priceMap := map[string]string{}
	prePriceMap := map[string]string{}
	timeMap := map[string]string{}
	if stockData, err := NewStockDataApi().GetStockCodeRealTimeData(apiCodes...); err == nil && stockData != nil {
		for _, info := range *stockData {
			k := normalizeStockCodeForMatch(info.Code)
			priceMap[k] = info.Price
			prePriceMap[k] = info.PreClose
			timeMap[k] = info.Date + " " + info.Time
		}
	}

	saved := 0
	for _, r := range recs {
		key := normalizeStockCodeForMatch(r.StockCode)
		curPrice := priceMap[key]
		prePrice := prePriceMap[key]
		curTime := timeMap[key]
		bkName := "涨停梯队-" + date

		rec := &models.AiRecommendStocks{
			DataTime:                    &now,
			ModelName:                   "涨停梯队AI复盘-" + modelName,
			Rating:                      r.Rating,
			StockCode:                   r.StockCode,
			StockName:                   r.StockName,
			BkName:                      bkName,
			StockPrice:                  curPrice,
			StockCurrentPrice:           curPrice,
			StockPrePrice:               prePrice,
			StockCurrentPriceTime:       curTime,
			RecommendReason:             r.Reason,
			RecommendBuyPrice:           fmt.Sprintf("%.2f-%.2f", r.BuyPriceMin, r.BuyPriceMax),
			RecommendBuyPriceMin:        r.BuyPriceMin,
			RecommendBuyPriceMax:        r.BuyPriceMax,
			RecommendStopProfitPrice:    fmt.Sprintf("%.2f-%.2f", r.StopProfitMin, r.StopProfitMax),
			RecommendStopProfitPriceMin: r.StopProfitMin,
			RecommendStopProfitPriceMax: r.StopProfitMax,
			RecommendStopLossPrice:      fmt.Sprintf("%.2f", r.StopLoss),
			RiskRemarks:                 r.Risk,
			Remarks:                     "由涨停梯队 AI 一键复盘生成 (" + date + ")",
		}
		if err := db.Dao.Create(rec).Error; err != nil {
			logger.SugaredLogger.Warnf("保存推荐失败 %s: %v", r.StockCode, err)
			continue
		}
		saved++
	}
	return saved
}
