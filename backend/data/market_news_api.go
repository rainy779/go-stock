package data

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
	"go-stock/backend/util"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/coocood/freecache"
	"github.com/duke-git/lancet/v2/convertor"
	"github.com/duke-git/lancet/v2/strutil"
	"github.com/robertkrimen/otto"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
	"golang.org/x/net/html/charset"
)

// 翻译缓存 - 进程内 map，避免重复翻译相同标题
var translateCache = sync.Map{}

// containsChineseChar 检测字符串里是否含至少一个中文字符
func containsChineseChar(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// TranslateBatch 批量把英文翻译为简体中文，自动跳过已有中文的，自动缓存
// 失败的项返回原文
func (m MarketNewsApi) TranslateBatch(texts []string) []string {
	if len(texts) == 0 {
		return texts
	}
	result := make([]string, len(texts))
	var needTranslate []string
	var needIdx []int

	// 第一遍：用缓存 + 跳过中文
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			result[i] = t
			continue
		}
		if cached, ok := translateCache.Load(t); ok {
			result[i] = cached.(string)
			continue
		}
		if containsChineseChar(t) {
			result[i] = t
			translateCache.Store(t, t)
			continue
		}
		needTranslate = append(needTranslate, t)
		needIdx = append(needIdx, i)
	}
	if len(needTranslate) == 0 {
		return result
	}

	// 用换行分隔批量翻译（Google Translate 会按行返回）
	combined := strings.Join(needTranslate, "\n")
	apiUrl := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=zh-CN&dt=t&q=" + url.QueryEscape(combined)

	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/117.0.0.0").
		Get(apiUrl)
	if err != nil {
		logger.SugaredLogger.Warnf("TranslateBatch err: %v", err)
		// 失败 → 全部塞回原文
		for k, idx := range needIdx {
			result[idx] = needTranslate[k]
		}
		return result
	}

	// Google 返回:
	// [[["译文段1","原文段1",null,null,1],["译文段2","原文段2",...],...], null, "en", ...]
	// 每个换行在 Google 内可能拆成多段，所以拼起来再按 \n 切
	items := gjson.Parse(string(resp.Body())).Get("0")
	if !items.IsArray() {
		for k, idx := range needIdx {
			result[idx] = needTranslate[k]
		}
		return result
	}
	var sb strings.Builder
	items.ForEach(func(_, v gjson.Result) bool {
		sb.WriteString(v.Get("0").String())
		return true
	})
	parts := strings.Split(sb.String(), "\n")

	for k, idx := range needIdx {
		if k < len(parts) {
			trans := strings.TrimSpace(parts[k])
			if trans != "" {
				result[idx] = trans
				translateCache.Store(needTranslate[k], trans)
				continue
			}
		}
		// fallback 原文
		result[idx] = needTranslate[k]
	}
	return result
}

// @Author spark
// @Date 2025/4/23 14:54
// @Desc
// -----------------------------------------------------------------------------------
type MarketNewsApi struct {
}

func NewMarketNewsApi() *MarketNewsApi {
	return &MarketNewsApi{}
}

func (m MarketNewsApi) TelegraphList(crawlTimeOut int64) *[]models.Telegraph {
	//https://www.cls.cn/nodeapi/telegraphList
	url := "https://www.cls.cn/nodeapi/telegraphList"
	res := map[string]any{}
	_, _ = SharedHTTPClient.SetTimeout(time.Duration(crawlTimeOut)*time.Second).R().
		SetHeader("Referer", "https://www.cls.cn/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		SetResult(&res).
		Get(url)
	var telegraphs []models.Telegraph

	if v, _ := convertor.ToInt(res["error"]); v == 0 {
		if res["data"] == nil {
			return m.GetNewTelegraph(30)
		}
		data := res["data"].(map[string]any)
		rollData := data["roll_data"].([]any)
		for _, v := range rollData {
			news := v.(map[string]any)
			ctime, _ := convertor.ToInt(news["ctime"])
			dataTime := time.Unix(ctime, 0).Local()
			telegraph := models.Telegraph{
				Title:           news["title"].(string),
				Content:         news["content"].(string),
				Time:            dataTime.Format("15:04:05"),
				DataTime:        &dataTime,
				Url:             news["shareurl"].(string),
				Source:          "财联社电报",
				IsRed:           (news["level"].(string)) != "C",
				SentimentResult: AnalyzeSentiment(news["content"].(string)).Description,
			}
			cnt := int64(0)
			if telegraph.Title == "" {
				db.Dao.Model(telegraph).Where("content=?", telegraph.Content).Count(&cnt)
			} else {
				db.Dao.Model(telegraph).Where("title=?", telegraph.Title).Count(&cnt)
			}
			if cnt > 0 {
				continue
			}
			telegraphs = append(telegraphs, telegraph)
			db.Dao.Model(&models.Telegraph{}).Create(&telegraph)
			////logger.SugaredLogger.Debugf("telegraph: %+v", &telegraph)
			if news["subjects"] == nil {
				continue
			}
			subjects := news["subjects"].([]any)
			for _, subject := range subjects {
				name := subject.(map[string]any)["subject_name"].(string)
				tag := &models.Tags{
					Name: name,
					Type: "subject",
				}
				db.Dao.Model(tag).Where("name=? and type=?", name, "subject").FirstOrCreate(&tag)
				db.Dao.Model(models.TelegraphTags{}).Where("telegraph_id=? and tag_id=?", telegraph.ID, tag.ID).FirstOrCreate(&models.TelegraphTags{
					TelegraphId: telegraph.ID,
					TagId:       tag.ID,
				})
			}

		}
		//db.Dao.Model(&models.Telegraph{}).Create(&telegraphs)
		////logger.SugaredLogger.Debugf("telegraphs: %+v", &telegraphs)
	}

	return &telegraphs
}

func (m MarketNewsApi) GetNewTelegraph(crawlTimeOut int64) *[]models.Telegraph {
	url := "https://www.cls.cn/telegraph"
	response, _ := SharedHTTPClient.SetTimeout(time.Duration(crawlTimeOut)*time.Second).R().
		SetHeader("Referer", "https://www.cls.cn/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get(url)
	var telegraphs []models.Telegraph
	//logger.SugaredLogger.Info(string(response.Body()))
	document, _ := goquery.NewDocumentFromReader(strings.NewReader(string(response.Body())))

	document.Find(".telegraph-content-box").Each(func(i int, selection *goquery.Selection) {
		//logger.SugaredLogger.Info(selection.Text())
		telegraph := models.Telegraph{Source: "财联社电报"}
		spans := selection.Find("div.telegraph-content-box span")
		if spans.Length() == 2 {
			telegraph.Time = spans.First().Text()
			telegraph.Content = spans.Last().Text()
			if spans.Last().HasClass("c-de0422") {
				telegraph.IsRed = true
			}
		}

		labels := selection.Find("div a.label-item")
		labels.Each(func(i int, selection *goquery.Selection) {
			if selection.HasClass("link-label-item") {
				telegraph.Url = selection.AttrOr("href", "")
			} else {
				tag := &models.Tags{
					Name: selection.Text(),
					Type: "subject",
				}
				db.Dao.Model(tag).Where("name=? and type=?", selection.Text(), "subject").FirstOrCreate(&tag)
				telegraph.SubjectTags = append(telegraph.SubjectTags, selection.Text())
			}
		})
		stocks := selection.Find("div.telegraph-stock-plate-box a")
		stocks.Each(func(i int, selection *goquery.Selection) {
			telegraph.StocksTags = append(telegraph.StocksTags, selection.Text())
		})

		//telegraph = append(telegraph, ReplaceSensitiveWords(selection.Text()))
		if telegraph.Content != "" {
			telegraph.SentimentResult = AnalyzeSentiment(telegraph.Content).Description
			cnt := int64(0)
			db.Dao.Model(telegraph).Where("time=? and content=?", telegraph.Time, telegraph.Content).Count(&cnt)
			if cnt == 0 {
				db.Dao.Create(&telegraph)
				telegraphs = append(telegraphs, telegraph)
				for _, tag := range telegraph.SubjectTags {
					tagInfo := &models.Tags{}
					db.Dao.Model(models.Tags{}).Where("name=? and type=?", tag, "subject").First(&tagInfo)
					if tagInfo.ID > 0 {
						db.Dao.Model(models.TelegraphTags{}).Where("telegraph_id=? and tag_id=?", telegraph.ID, tagInfo.ID).FirstOrCreate(&models.TelegraphTags{
							TelegraphId: telegraph.ID,
							TagId:       tagInfo.ID,
						})
					}
				}
			}

		}
	})
	return &telegraphs
}
func (m MarketNewsApi) GetNewsList(source string, limit int) *[]*models.Telegraph {
	news := &[]*models.Telegraph{}
	if source != "" {
		db.Dao.Model(news).Preload("TelegraphTags").Where("source=?", source).Order("data_time desc,time desc").Limit(limit).Find(news)
	} else {
		db.Dao.Model(news).Preload("TelegraphTags").Order("data_time desc,time desc").Limit(limit).Find(news)
	}
	for _, item := range *news {
		tags := &[]models.Tags{}
		db.Dao.Model(&models.Tags{}).Where("id in ?", lo.Map(item.TelegraphTags, func(item models.TelegraphTags, index int) uint {
			return item.TagId
		})).Find(&tags)
		tagNames := lo.Map(*tags, func(item models.Tags, index int) string {
			return item.Name
		})
		item.SubjectTags = tagNames
		//logger.SugaredLogger.Infof("tagNames %v ，SubjectTags：%s", tagNames, item.SubjectTags)
	}
	return news
}
func (m MarketNewsApi) GetNewsList2(source string, limit int) *[]*models.Telegraph {
	NewMarketNewsApi().TelegraphList(30)
	news := &[]*models.Telegraph{}
	if source != "" {
		db.Dao.Model(news).Preload("TelegraphTags").Where("source=?", source).Order("data_time desc,is_red desc").Limit(limit).Find(news)
	} else {
		db.Dao.Model(news).Preload("TelegraphTags").Order("data_time desc,is_red desc").Limit(limit).Find(news)
	}
	for _, item := range *news {
		tags := &[]models.Tags{}
		db.Dao.Model(&models.Tags{}).Where("id in ?", lo.Map(item.TelegraphTags, func(item models.TelegraphTags, index int) uint {
			return item.TagId
		})).Find(&tags)
		tagNames := lo.Map(*tags, func(item models.Tags, index int) string {
			return item.Name
		})
		item.SubjectTags = tagNames
		//logger.SugaredLogger.Infof("tagNames %v ，SubjectTags：%s", tagNames, item.SubjectTags)
	}
	return news
}

func (m MarketNewsApi) GetTelegraphList(source string) *[]*models.Telegraph {
	news := &[]*models.Telegraph{}
	if source != "" {
		db.Dao.Model(news).Preload("TelegraphTags").Where("source=?", source).Order("data_time desc,time desc").Limit(50).Find(news)
	} else {
		db.Dao.Model(news).Preload("TelegraphTags").Order("data_time desc,time desc").Limit(50).Find(news)
	}
	for _, item := range *news {
		tags := &[]models.Tags{}
		db.Dao.Model(&models.Tags{}).Where("id in ?", lo.Map(item.TelegraphTags, func(item models.TelegraphTags, index int) uint {
			return item.TagId
		})).Find(&tags)
		tagNames := lo.Map(*tags, func(item models.Tags, index int) string {
			return item.Name
		})
		item.SubjectTags = tagNames
		//logger.SugaredLogger.Infof("tagNames %v ，SubjectTags：%s", tagNames, item.SubjectTags)
	}
	return news
}
func (m MarketNewsApi) GetTelegraphListWithPaging(source string, page, pageSize int) *[]*models.Telegraph {
	// 计算偏移量
	offset := (page - 1) * pageSize

	news := &[]*models.Telegraph{}
	if source != "" {
		db.Dao.Model(news).Preload("TelegraphTags").Where("source=?", source).Order("data_time desc,time desc").Limit(pageSize).Offset(offset).Find(news)
	} else {
		db.Dao.Model(news).Preload("TelegraphTags").Order("data_time desc,time desc").Limit(pageSize).Offset(offset).Find(news)
	}
	for _, item := range *news {
		tags := &[]models.Tags{}
		db.Dao.Model(&models.Tags{}).Where("id in ?", lo.Map(item.TelegraphTags, func(item models.TelegraphTags, index int) uint {
			return item.TagId
		})).Find(&tags)
		tagNames := lo.Map(*tags, func(item models.Tags, index int) string {
			return item.Name
		})
		item.SubjectTags = tagNames
		//logger.SugaredLogger.Infof("tagNames %v ，SubjectTags：%s", tagNames, item.SubjectTags)
	}
	return news
}

func (m MarketNewsApi) GetSinaNews(crawlTimeOut uint) *[]models.Telegraph {
	news := &[]models.Telegraph{}
	response, _ := SharedHTTPClient.SetTimeout(time.Duration(crawlTimeOut)*time.Second).R().
		SetHeader("Referer", "https://finance.sina.com.cn").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get("https://zhibo.sina.com.cn/api/zhibo/feed?callback=callback&page=1&page_size=20&zhibo_id=152&tag_id=0&dire=f&dpc=1&pagesize=20&id=4161089&type=0&_=" + strconv.FormatInt(time.Now().Unix(), 10))
	js := string(response.Body())
	js = strutil.ReplaceWithMap(js, map[string]string{
		"try{callback(":  "var data=",
		");}catch(e){};": ";",
	})
	//logger.SugaredLogger.Info(js)
	vm := otto.New()
	_, err := vm.Run(js)
	if err != nil {
		logger.SugaredLogger.Error(err)
	}
	vm.Run("var result = data.result;")
	//vm.Run("var resultStr =JSON.stringify(data);")
	vm.Run("var resultData = result.data;")
	vm.Run("var feed = resultData.feed;")
	vm.Run("var feedStr = JSON.stringify(feed);")

	value, _ := vm.Get("feedStr")
	//resultStr, _ := vm.Get("resultStr")

	//logger.SugaredLogger.Info(resultStr)
	feed := make(map[string]any)
	err = json.Unmarshal([]byte(value.String()), &feed)
	if err != nil {
		logger.SugaredLogger.Errorf("json.Unmarshal error:%v", err.Error())
	}
	var telegraphs []models.Telegraph

	if feed["list"] != nil {
		for _, item := range feed["list"].([]any) {
			telegraph := models.Telegraph{Source: "新浪财经"}
			data := item.(map[string]any)
			//logger.SugaredLogger.Infof("%s:%s", data["create_time"], data["rich_text"])
			telegraph.Content = data["rich_text"].(string)
			telegraph.Title = strutil.SubInBetween(data["rich_text"].(string), "【", "】")
			telegraph.Time = strings.Split(data["create_time"].(string), " ")[1]
			dataTime, _ := time.ParseInLocation("2006-01-02 15:04:05", data["create_time"].(string), time.Local)
			if &dataTime != nil {
				telegraph.DataTime = &dataTime
			}
			tags := data["tag"].([]any)
			telegraph.SubjectTags = lo.Map(tags, func(tagItem any, index int) string {
				name := tagItem.(map[string]any)["name"].(string)
				tag := &models.Tags{
					Name: name,
					Type: "sina_subject",
				}
				db.Dao.Model(tag).Where("name=? and type=?", name, "sina_subject").FirstOrCreate(&tag)
				return name
			})
			if _, ok := lo.Find(telegraph.SubjectTags, func(item string) bool { return item == "焦点" }); ok {
				telegraph.IsRed = true
			}
			//logger.SugaredLogger.Infof("telegraph.SubjectTags:%v %s", telegraph.SubjectTags, telegraph.Content)

			if telegraph.Content != "" {
				telegraph.SentimentResult = AnalyzeSentiment(telegraph.Content).Description
				cnt := int64(0)
				if telegraph.Title == "" {
					db.Dao.Model(telegraph).Where("content=?", telegraph.Content).Count(&cnt)
				} else {
					db.Dao.Model(telegraph).Where("title=?", telegraph.Title).Count(&cnt)
				}
				if cnt == 0 {
					db.Dao.Create(&telegraph)
					telegraphs = append(telegraphs, telegraph)
					for _, tag := range telegraph.SubjectTags {
						tagInfo := &models.Tags{}
						db.Dao.Model(models.Tags{}).Where("name=? and type=?", tag, "sina_subject").First(&tagInfo)
						if tagInfo.ID > 0 {
							db.Dao.Model(models.TelegraphTags{}).Where("telegraph_id=? and tag_id=?", telegraph.ID, tagInfo.ID).FirstOrCreate(&models.TelegraphTags{
								TelegraphId: telegraph.ID,
								TagId:       tagInfo.ID,
							})
						}
					}
				}
			}
		}
		return &telegraphs
	}

	return news

}

func (m MarketNewsApi) GlobalStockIndexes(crawlTimeOut uint) map[string]any {
	response, _ := SharedHTTPClient.SetTimeout(time.Duration(crawlTimeOut)*time.Second).R().
		SetHeader("Referer", "https://stockapp.finance.qq.com/mstats").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get("https://proxy.finance.qq.com/ifzqgtimg/appstock/app/rank/indexRankDetail2")
	js := string(response.Body())
	res := make(map[string]any)
	json.Unmarshal([]byte(js), &res)
	return res["data"].(map[string]any)
}

// GlobalStockIndexesReadable 获取全球指数并转换为 AI 易读的 Markdown 文本。
func (m MarketNewsApi) GlobalStockIndexesReadable(crawlTimeOut uint) string {
	data := m.GlobalStockIndexes(crawlTimeOut)
	return m.GlobalStockIndexesToReadable(data)
}

// GlobalStockIndexesToReadable 将 GlobalStockIndexes 返回的 JSON 转为 AI 易读格式（Markdown）。
//
//	输入示例：map[string]any{
//	  "america": []any{...},
//	  "asia":    []any{...},
//	  "europe":  []any{...},
//	  "other":   []any{...},
//	  "common":  []any{...},
//	}
func (m MarketNewsApi) GlobalStockIndexesToReadable(data map[string]any) string {
	if len(data) == 0 {
		return "暂无全球指数数据。"
	}
	type regionDef struct {
		Key   string
		Title string
	}
	regions := []regionDef{
		{Key: "common", Title: "重点关注"},
		{Key: "asia", Title: "亚洲市场"},
		{Key: "america", Title: "美洲市场"},
		{Key: "europe", Title: "欧洲市场"},
		{Key: "other", Title: "其他市场"},
	}

	stateText := func(v string) string {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "open":
			return "开盘"
		case "close":
			return "收盘"
		default:
			if v == "" {
				return "-"
			}
			return v
		}
	}

	var sb strings.Builder
	sb.WriteString("# 全球主要指数概览\n")
	sb.WriteString("> 数据来源：腾讯财经，已按区域整理。\n\n")

	written := 0
	for _, region := range regions {
		raw, ok := data[region.Key]
		if !ok || raw == nil {
			continue
		}
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			continue
		}
		written++
		sb.WriteString("## ")
		sb.WriteString(region.Title)
		sb.WriteString("\n")
		sb.WriteString("| 指数 | 地区 | 最新点位 | 涨跌幅(%) | 状态 |\n")
		sb.WriteString("| --- | --- | ---: | ---: | --- |\n")

		for _, item := range list {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := convertor.ToString(row["name"])
			location := convertor.ToString(row["location"])
			zxj := convertor.ToString(row["zxj"])
			zdf := convertor.ToString(row["zdf"])
			state := stateText(convertor.ToString(row["state"]))
			if name == "" {
				name = convertor.ToString(row["code"])
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", name, location, zxj, zdf, state))
		}
		sb.WriteString("\n")
	}

	if written == 0 {
		return "暂无可解析的全球指数数据。"
	}
	return sb.String()
}

// CacheGlobalStockIndexes 将全球指数数据缓存到数据库
func (m MarketNewsApi) CacheGlobalStockIndexes(crawlTimeOut uint) error {
	data := m.GlobalStockIndexes(crawlTimeOut)
	if len(data) == 0 {
		return fmt.Errorf("获取全球指数数据失败")
	}

	// 定义区域映射
	regions := map[string]string{
		"america": "美洲",
		"asia":    "亚洲",
		"europe":  "欧洲",
		"common":  "重点关注",
		"other":   "其他",
	}

	for regionKey, regionName := range regions {
		raw, ok := data[regionKey]
		if !ok || raw == nil {
			continue
		}
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			continue
		}

		for _, item := range list {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}

			index := models.GlobalStockIndex{
				Code:       convertor.ToString(row["code"]),
				Name:       convertor.ToString(row["name"]),
				Location:   convertor.ToString(row["location"]),
				Qtcode:     convertor.ToString(row["qtcode"]),
				State:      convertor.ToString(row["state"]),
				Zdf:        convertor.ToString(row["zdf"]),
				Zxj:        convertor.ToString(row["zxj"]),
				Img:        convertor.ToString(row["img"]),
				Region:     regionKey,
				RegionName: regionName,
			}

			// 如果已存在则更新，不存在则创建
			existing := models.GlobalStockIndex{}
			query := db.Dao.Model(&models.GlobalStockIndex{}).Where("qtcode = ?", index.Qtcode)
			if err := query.First(&existing).Error; err == nil {
				// 记录已存在，更新
				db.Dao.Model(&existing).Updates(map[string]any{
					"name":     index.Name,
					"location": index.Location,
					"state":    index.State,
					"zdf":      index.Zdf,
					"zxj":      index.Zxj,
					"img":      index.Img,
					"region":   index.Region,
				})
			} else {
				// 记录不存在，创建
			}
			db.Dao.Where(models.GlobalStockIndex{Qtcode: index.Qtcode}).FirstOrCreate(&index)
		}
	}

	logger.SugaredLogger.Info("全球指数缓存完成")
	return nil
}

// GetCachedGlobalStockIndexes 从数据库获取缓存的全球指数数据
func (m MarketNewsApi) GetCachedGlobalStockIndexes(region string) *[]models.GlobalStockIndex {
	indexes := &[]models.GlobalStockIndex{}
	query := db.Dao.Model(&models.GlobalStockIndex{})
	if region != "" && region != "all" {
		query = query.Where("region = ?", region)
	}
	query.Order("region, zdf desc").Find(indexes)
	return indexes
}

// GetCachedGlobalStockIndexesReadable 获取缓存的全球指数并转换为易读格式
func (m MarketNewsApi) GetCachedGlobalStockIndexesReadable(region string) string {
	data := m.GetCachedGlobalStockIndexes(region)
	if data == nil || len(*data) == 0 {
		return "暂无全球指数数据。"
	}

	type regionDef struct {
		Key   string
		Title string
	}
	regions := []regionDef{
		{Key: "common", Title: "重点关注"},
		{Key: "asia", Title: "亚洲市场"},
		{Key: "america", Title: "美洲市场"},
		{Key: "europe", Title: "欧洲市场"},
		{Key: "other", Title: "其他市场"},
	}

	stateText := func(v string) string {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "open":
			return "开盘"
		case "close":
			return "收盘"
		default:
			if v == "" {
				return "-"
			}
			return v
		}
	}

	var sb strings.Builder
	sb.WriteString("# 全球主要指数概览\n")
	sb.WriteString("> 数据来源：腾讯财经，已按区域整理。\n\n")

	// 按区域分组
	indexesByRegion := make(map[string][]models.GlobalStockIndex)
	for _, idx := range *data {
		indexesByRegion[idx.Region] = append(indexesByRegion[idx.Region], idx)
	}

	written := 0
	for _, regionDef := range regions {
		list, ok := indexesByRegion[regionDef.Key]
		if !ok || len(list) == 0 {
			continue
		}
		written++
		sb.WriteString("## ")
		sb.WriteString(regionDef.Title)
		sb.WriteString("\n")
		sb.WriteString("| 指数 | 地区 | 最新点位 | 涨跌幅(%) | 状态 |\n")
		sb.WriteString("| --- | --- | ---: | ---: | --- |\n")

		for _, idx := range list {
			name := idx.Name
			if name == "" {
				name = idx.Code
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
				name, idx.Location, idx.Zxj, idx.Zdf, stateText(idx.State)))
		}
		sb.WriteString("\n")
	}

	if written == 0 {
		return "暂无可解析的全球指数数据。"
	}
	return sb.String()
}

func (m MarketNewsApi) GetIndustryRank(sort string, cnt int) map[string]any {

	url := fmt.Sprintf("https://proxy.finance.qq.com/ifzqgtimg/appstock/app/mktHs/rank?l=%d&p=1&t=01/averatio&ordertype=&o=%s", cnt, sort)
	response, _ := SharedHTTPClient.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Referer", "https://stockapp.finance.qq.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get(url)
	js := string(response.Body())
	res := make(map[string]any)
	json.Unmarshal([]byte(js), &res)
	return res
}

func (m MarketNewsApi) GetIndustryMoneyRankSina(fenlei, sort string) []map[string]any {
	url := fmt.Sprintf("https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_bkzj_bk?page=1&num=20&sort=%s&asc=0&fenlei=%s", sort, fenlei)

	response, _ := SharedHTTPClient.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Host", "vip.stock.finance.sina.com.cn").
		SetHeader("Referer", "https://finance.sina.com.cn").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get(url)
	js := string(response.Body())
	res := &[]map[string]any{}
	err := json.Unmarshal([]byte(js), &res)
	if err != nil {
		logger.SugaredLogger.Error(err)
		return *res
	}
	return *res
}

func (m MarketNewsApi) GetMoneyRankSina(sort string) []map[string]any {
	if sort == "" {
		sort = "netamount"
	}
	url := fmt.Sprintf("https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_bkzj_ssggzj?page=1&num=20&sort=%s&asc=0&bankuai=&shichang=", sort)
	response, _ := SharedHTTPClient.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Host", "vip.stock.finance.sina.com.cn").
		SetHeader("Referer", "https://finance.sina.com.cn").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get(url)
	js := string(response.Body())
	res := &[]map[string]any{}
	err := json.Unmarshal([]byte(js), &res)
	if err != nil {
		logger.SugaredLogger.Error(err)
		return *res
	}
	return *res
}

func (m MarketNewsApi) GetStockMoneyTrendByDay(stockCode string, days int) []map[string]any {
	url := fmt.Sprintf("http://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?page=1&num=%d&sort=opendate&asc=0&daima=%s", days, stockCode)

	response, _ := SharedHTTPClient.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Host", "vip.stock.finance.sina.com.cn").
		SetHeader("Referer", "https://finance.sina.com.cn").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").Get(url)
	js := string(response.Body())
	res := &[]map[string]any{}
	err := json.Unmarshal([]byte(js), &res)
	if err != nil {
		logger.SugaredLogger.Error(err)
		return *res
	}
	return *res

}

func (m MarketNewsApi) LongTiger(date string) *[]models.LongTigerRankData {
	ranks := &[]models.LongTigerRankData{}
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get"
	//logger.SugaredLogger.Infof("url:%s", url)
	params := make(map[string]string)
	params["callback"] = "callback"
	params["sortColumns"] = "TURNOVERRATE,TRADE_DATE,SECURITY_CODE"
	params["sortTypes"] = "-1,-1,1"
	params["pageSize"] = "500"
	params["pageNumber"] = "1"
	params["reportName"] = "RPT_DAILYBILLBOARD_DETAILSNEW"
	params["columns"] = "SECURITY_CODE,SECUCODE,SECURITY_NAME_ABBR,TRADE_DATE,EXPLAIN,CLOSE_PRICE,CHANGE_RATE,BILLBOARD_NET_AMT,BILLBOARD_BUY_AMT,BILLBOARD_SELL_AMT,BILLBOARD_DEAL_AMT,ACCUM_AMOUNT,DEAL_NET_RATIO,DEAL_AMOUNT_RATIO,TURNOVERRATE,FREE_MARKET_CAP,EXPLANATION,D1_CLOSE_ADJCHRATE,D2_CLOSE_ADJCHRATE,D5_CLOSE_ADJCHRATE,D10_CLOSE_ADJCHRATE,SECURITY_TYPE_CODE"
	params["source"] = "WEB"
	params["client"] = "WEB"
	params["filter"] = fmt.Sprintf("(TRADE_DATE<='%s')(TRADE_DATE>='%s')", date, date)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "datacenter-web.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/stock/tradedetail.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetQueryParams(params).
		Get(url)
	if err != nil {
		return ranks
	}
	js := string(resp.Body())
	//logger.SugaredLogger.Infof("resp:%s", js)

	js = strutil.ReplaceWithMap(js, map[string]string{
		"callback(": "var data=",
		");":        ";",
	})
	//logger.SugaredLogger.Info(js)
	vm := otto.New()
	_, err = vm.Run(js)
	_, err = vm.Run("var data = JSON.stringify(data);")
	value, err := vm.Get("data")
	//logger.SugaredLogger.Infof("resp-json:%s", value.String())
	data := gjson.Get(value.String(), "result.data")
	//logger.SugaredLogger.Infof("resp:%v", data)
	err = json.Unmarshal([]byte(data.String()), ranks)
	if err != nil {
		logger.SugaredLogger.Error(err)
		return ranks
	}
	for _, rankData := range *ranks {
		temp := &models.LongTigerRankData{}
		db.Dao.Model(temp).Where(&models.LongTigerRankData{
			TRADEDATE: rankData.TRADEDATE,
			SECUCODE:  rankData.SECUCODE,
		}).First(temp)
		if temp.SECURITYTYPECODE == "" {
			db.Dao.Model(temp).Create(&rankData)
		}
	}
	return ranks
}

// HKIndustryResearchReport 港股相关研究/资讯
// 数据源：东财 qType=2 海外研报（中文过滤）+ Yahoo Finance 港股相关英文新闻
func (m MarketNewsApi) HKIndustryResearchReport(industryCode string, days int) []any {
	keywords := []string{"港股", "恒生", "腾讯", "阿里", "美团", "百度", "京东", "比亚迪",
		"汇丰", "友邦", "小米", "蔚来", "理想", "网易", "携程", "快手",
		"中海油", "中国移动", "中国联通", "中国电信", "招商局", "中海洋",
		"H股", "Hong Kong", "香港"}
	cn := m.industryResearchReportByQType(industryCode, days, "2", keywords)
	en := m.YahooFinanceNews([]string{
		"Hang Seng Index", "Hong Kong stocks", "China stocks",
		"Tencent", "Alibaba", "BYD", "Xiaomi", "NIO", "Baidu", "JD.com",
	})
	return append(cn, en...)
}

// USIndustryResearchReport 美股相关研究/资讯
// 数据源：东财 qType=2 海外研报（中文过滤）+ Yahoo Finance 美股板块英文新闻
func (m MarketNewsApi) USIndustryResearchReport(industryCode string, days int) []any {
	keywords := []string{"美股", "纳指", "纳斯达克", "道琼斯", "标普", "苹果", "英伟达",
		"特斯拉", "微软", "谷歌", "亚马逊", "Meta", "AMD", "OpenAI",
		"Salesforce", "Netflix", "Nvidia", "Apple", "Tesla", "Google",
		"Amazon", "Microsoft", "Berkshire", "美联储", "美国"}
	cn := m.industryResearchReportByQType(industryCode, days, "2", keywords)
	en := m.YahooFinanceNews([]string{
		"S&P 500", "Nasdaq", "Dow Jones",
		"Technology stocks", "AI stocks", "Semiconductor",
		"Energy stocks", "Healthcare stocks", "Financial stocks",
	})
	return append(cn, en...)
}

// YahooFinanceNews 抓 Yahoo Finance 新闻搜索 API，按多个关键词聚合后转成 IndustryResearchReport 兼容 schema
func (m MarketNewsApi) YahooFinanceNews(queries []string) []any {
	results := []any{}
	seen := map[string]bool{}

	for _, q := range queries {
		apiUrl := fmt.Sprintf("https://query1.finance.yahoo.com/v1/finance/search?q=%s&newsCount=8&quotesCount=0",
			url.QueryEscape(q))
		resp, err := SharedHTTPClient.SetTimeout(time.Duration(10)*time.Second).R().
			SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36").
			SetHeader("Accept", "application/json").
			Get(apiUrl)
		if err != nil {
			logger.SugaredLogger.Warnf("YahooFinanceNews query=%s err: %v", q, err)
			continue
		}
		respMap := map[string]any{}
		if err := json.Unmarshal(resp.Body(), &respMap); err != nil {
			continue
		}
		news, ok := respMap["news"].([]any)
		if !ok {
			continue
		}
		for _, item := range news {
			article, ok := item.(map[string]any)
			if !ok {
				continue
			}
			title, _ := article["title"].(string)
			if title == "" || seen[title] {
				continue
			}
			seen[title] = true

			var publishDate string
			if pt, ok := article["providerPublishTime"].(float64); ok {
				publishDate = time.Unix(int64(pt), 0).Local().Format("2006-01-02 15:04:05")
			} else {
				publishDate = time.Now().Format("2006-01-02 15:04:05")
			}

			link, _ := article["link"].(string)
			publisher, _ := article["publisher"].(string)
			// Yahoo 标题可能有 HTML 实体（&#39; 等），先 unescape
			cleanTitle := html.UnescapeString(title)

			results = append(results, map[string]any{
				"industryName":  q,
				"title":         cleanTitle,
				"emRatingName":  "",
				"ratingChange":  -1,
				"sRatingName":   "",
				"researcher":    "",
				"orgSName":      publisher,
				"publishDate":   publishDate,
				"infoCode":      "",
				"infoLink":      link, // 直接的新闻 URL（区别于 东财 PDF code）
			})
		}
	}

	// 批量翻译所有英文标题为简体中文（中文标题保持原样，自动缓存）
	titles := make([]string, len(results))
	for i, item := range results {
		article := item.(map[string]any)
		titles[i], _ = article["title"].(string)
	}
	translated := m.TranslateBatch(titles)
	for i, item := range results {
		article := item.(map[string]any)
		article["originalTitle"] = titles[i] // 保留原文（前端可选展示）
		article["title"] = translated[i]
	}

	return results
}

// industryResearchReportByQType 通用研报拉取，按 qType 区分；keywords 非空时按标题做关键词过滤
func (m MarketNewsApi) industryResearchReportByQType(industryCode string, days int, qType string, keywords []string) []any {
	beginDate := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02")
	endDate := time.Now().Format("2006-01-02")
	if strutil.Trim(industryCode) != "" {
		beginDate = time.Now().Add(-time.Duration(days) * 365 * time.Hour).Format("2006-01-02")
	}

	params := map[string]string{
		"industry":     "*",
		"industryCode": industryCode,
		"beginTime":    beginDate,
		"endTime":      endDate,
		"pageNo":       "1",
		"pageSize":     "100",
		"p":            "1",
		"pageNum":      "1",
		"pageNumber":   "1",
		"qType":        qType,
	}

	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "reportapi.eastmoney.com").
		SetHeader("Origin", "https://data.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/report/stock.jshtml").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetHeader("Content-Type", "application/json").
		SetQueryParams(params).Get("https://reportapi.eastmoney.com/report/list")
	if err != nil {
		return []any{}
	}
	respMap := map[string]any{}
	json.Unmarshal(resp.Body(), &respMap)

	data, ok := respMap["data"].([]any)
	if !ok || len(data) == 0 {
		return []any{}
	}

	// 无关键词 → 直接返回
	if len(keywords) == 0 {
		return data
	}

	// 按标题关键词过滤
	filtered := []any{}
	for _, item := range data {
		report, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := report["title"].(string)
		for _, kw := range keywords {
			if strings.Contains(title, kw) {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func (m MarketNewsApi) IndustryResearchReport(industryCode string, days int) []any {
	beginDate := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02")
	endDate := time.Now().Format("2006-01-02")
	if strutil.Trim(industryCode) != "" {
		beginDate = time.Now().Add(-time.Duration(days) * 365 * time.Hour).Format("2006-01-02")
	}

	//logger.SugaredLogger.Infof("IndustryResearchReport-name:%s", industryCode)
	params := map[string]string{
		"industry":     "*",
		"industryCode": industryCode,
		"beginTime":    beginDate,
		"endTime":      endDate,
		"pageNo":       "1",
		"pageSize":     "50",
		"p":            "1",
		"pageNum":      "1",
		"pageNumber":   "1",
		"qType":        "1",
	}

	url := "https://reportapi.eastmoney.com/report/list"

	//logger.SugaredLogger.Infof("beginDate:%s endDate:%s", beginDate, endDate)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "reportapi.eastmoney.com").
		SetHeader("Origin", "https://data.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/report/stock.jshtml").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetHeader("Content-Type", "application/json").
		SetQueryParams(params).Get(url)
	respMap := map[string]any{}

	if err != nil {
		return []any{}
	}
	json.Unmarshal(resp.Body(), &respMap)
	//logger.SugaredLogger.Infof("resp:%+v", respMap["data"])
	return respMap["data"].([]any)
}
func (m MarketNewsApi) StockResearchReport(stockCode string, days int) []any {
	beginDate := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02")
	endDate := time.Now().Format("2006-01-02")
	if strutil.ContainsAny(stockCode, []string{"."}) {
		stockCode = strings.Split(stockCode, ".")[0]
		beginDate = time.Now().Add(-time.Duration(days) * 365 * time.Hour).Format("2006-01-02")
	} else {
		stockCode = strutil.ReplaceWithMap(stockCode, map[string]string{
			"sh":  "",
			"sz":  "",
			"gb_": "",
			"us":  "",
			"us_": "",
		})
		beginDate = time.Now().Add(-time.Duration(days) * 365 * time.Hour).Format("2006-01-02")
	}

	//logger.SugaredLogger.Infof("StockResearchReport-stockCode:%s", stockCode)

	type Req struct {
		BeginTime    string      `json:"beginTime"`
		EndTime      string      `json:"endTime"`
		IndustryCode string      `json:"industryCode"`
		RatingChange string      `json:"ratingChange"`
		Rating       string      `json:"rating"`
		OrgCode      interface{} `json:"orgCode"`
		Code         string      `json:"code"`
		Rcode        string      `json:"rcode"`
		PageSize     int         `json:"pageSize"`
		PageNo       int         `json:"pageNo"`
		P            int         `json:"p"`
		PageNum      int         `json:"pageNum"`
		PageNumber   int         `json:"pageNumber"`
	}

	url := "https://reportapi.eastmoney.com/report/list2"

	//logger.SugaredLogger.Infof("beginDate:%s endDate:%s", beginDate, endDate)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "reportapi.eastmoney.com").
		SetHeader("Origin", "https://data.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/report/stock.jshtml").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetHeader("Content-Type", "application/json").
		SetBody(&Req{
			Code:         stockCode,
			IndustryCode: "*",
			BeginTime:    beginDate,
			EndTime:      endDate,
			PageNo:       1,
			PageSize:     50,
			P:            1,
			PageNum:      1,
			PageNumber:   1,
		}).Post(url)
	respMap := map[string]any{}

	if err != nil {
		return []any{}
	}
	json.Unmarshal(resp.Body(), &respMap)
	//logger.SugaredLogger.Infof("resp:%+v", respMap["data"])
	return respMap["data"].([]any)
}

// 默认聚合的热门港股（恒生主要成分 + 热门 ADR）
var defaultHKStocks = []string{
	"00700", // 腾讯
	"09988", // 阿里巴巴
	"01810", // 小米
	"09618", // 京东
	"03690", // 美团
	"02318", // 中国平安
	"01024", // 快手
	"00939", // 建设银行
	"02382", // 舜宇光学
	"00388", // 香港交易所
}

// HKStockNotice 拉取新浪港股公司公告
// stockCode 为空 → 默认并行拉热门 10 只港股，按时间排序混合展示
// 非空 → 按该单只股票拉
func (m MarketNewsApi) HKStockNotice(stockCode string) []any {
	stockCode = strings.TrimSpace(stockCode)

	// 空值 → 聚合默认热门股
	if stockCode == "" {
		return m.hkStockNoticeBatch(defaultHKStocks)
	}

	return m.fetchHKStockNotice(stockCode)
}

// hkStockNoticeBatch 并行抓多只港股公告，按时间排序合并
func (m MarketNewsApi) hkStockNoticeBatch(codes []string) []any {
	var mu sync.Mutex
	var wg sync.WaitGroup
	allResults := []any{}

	for _, c := range codes {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.SugaredLogger.Warnf("hkStockNoticeBatch panic on %s: %v", code, r)
				}
			}()
			r := m.fetchHKStockNotice(code)
			mu.Lock()
			allResults = append(allResults, r...)
			mu.Unlock()
		}(c)
	}
	wg.Wait()

	// 按 display_time 降序排
	sort.SliceStable(allResults, func(i, j int) bool {
		mi, _ := allResults[i].(map[string]any)
		mj, _ := allResults[j].(map[string]any)
		ti, _ := mi["display_time"].(string)
		tj, _ := mj["display_time"].(string)
		return ti > tj
	})

	// 最多 80 条
	if len(allResults) > 80 {
		allResults = allResults[:80]
	}
	return allResults
}

// fetchHKStockNotice 抓单只港股的公告
func (m MarketNewsApi) fetchHKStockNotice(stockCode string) []any {
	results := []any{}
	stockCode = strings.ToUpper(strings.TrimSpace(stockCode))
	// 兼容 "00700.HK" / "HK00700" / "00700" / "700"
	stockCode = strings.TrimSuffix(stockCode, ".HK")
	stockCode = strings.TrimPrefix(stockCode, "HK")
	for len(stockCode) < 5 && len(stockCode) > 0 {
		stockCode = "0" + stockCode
	}
	if stockCode == "" {
		return results
	}

	pageUrl := fmt.Sprintf("https://stock.finance.sina.com.cn/hkstock/notice/%s.html", stockCode)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Referer", "https://stock.finance.sina.com.cn/hkstock/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/117.0.0.0").
		Get(pageUrl)
	if err != nil {
		logger.SugaredLogger.Errorf("HKStockNotice fetch %s err: %v", stockCode, err)
		return results
	}

	// 新浪是 GB18030，自动检测+解码
	utf8Reader, err := charset.NewReader(bytes.NewReader(resp.Body()), resp.Header().Get("Content-Type"))
	if err != nil {
		logger.SugaredLogger.Errorf("HKStockNotice charset %s err: %v", stockCode, err)
		return results
	}
	doc, err := goquery.NewDocumentFromReader(utf8Reader)
	if err != nil {
		logger.SugaredLogger.Errorf("HKStockNotice parse %s err: %v", stockCode, err)
		return results
	}

	// 从 title 提取公司名 - "腾讯控股(00700)公告_港股频道..."
	pageTitle := doc.Find("title").Text()
	companyName := stockCode
	if m := regexp.MustCompile(`^([^()（）_]+?)\s*[\(（]`).FindStringSubmatch(pageTitle); len(m) > 1 {
		companyName = strings.TrimSpace(m[1])
	}

	dateRe := regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`)
	seen := map[string]bool{}

	doc.Find("a[href*='CompanyNoticeDetail']").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if href == "" {
			return
		}
		announceTitle := strings.TrimSpace(s.Text())
		if announceTitle == "" || len([]rune(announceTitle)) < 5 {
			return
		}
		if seen[announceTitle] {
			return
		}
		seen[announceTitle] = true

		// 找日期 - 通常在 a 的同级或 parent 的 sibling
		var dateStr string
		// 1) 看 a 的 parent 节点的 text 是否含日期
		if parent := s.Parent(); parent != nil {
			parentText := parent.Text()
			if m := dateRe.FindString(parentText); m != "" {
				dateStr = m
			}
		}
		// 2) 看 a 的 next sibling
		if dateStr == "" {
			if next := s.Next(); next != nil {
				if m := dateRe.FindString(next.Text()); m != "" {
					dateStr = m
				}
			}
		}

		var dataTime time.Time
		if dateStr != "" {
			if t, e := time.ParseInLocation("2006-01-02", dateStr, time.Local); e == nil {
				dataTime = t
			}
		}
		if dataTime.IsZero() {
			dataTime = time.Now()
		}

		results = append(results, map[string]any{
			"codes": []map[string]any{
				{
					"stock_code":  stockCode,
					"short_name":  companyName,
					"market_code": "hk",
					"ann_type":    "HK",
				},
			},
			"title": announceTitle,
			"columns": []map[string]any{
				{"column_name": "港股公告"},
			},
			"notice_date":  dataTime.Format("2006-01-02 15:04:05"),
			"display_time": dataTime.Format("2006-01-02 15:04:05"),
			"art_code":     "",
			"infoLink":     href,
		})
	})

	return results
}

// USStockNotice 拉取 SEC EDGAR 美股公告流（默认拉 8-K 重大事件 + 10-K 年报 + 10-Q 季报 + 6-K 外国公司）
// tickerFilter: 留空 = 全市场最新；填字符串 = 按公司名/CIK 简单包含过滤
func (m MarketNewsApi) USStockNotice(tickerFilter string) []any {
	results := []any{}
	seen := map[string]bool{}

	// SEC 要求 User-Agent 带联系方式
	secHeaders := map[string]string{
		"User-Agent": "go-stock-research contact@go-stock.local",
		"Accept":     "application/atom+xml, application/xml",
	}

	formTypes := []string{"8-K", "10-K", "10-Q", "6-K"}

	type atomLink struct {
		Href string `xml:"href,attr"`
	}
	type atomEntry struct {
		Title   string   `xml:"title"`
		Link    atomLink `xml:"link"`
		Updated string   `xml:"updated"`
		Summary string   `xml:"summary"`
	}
	type atomFeed struct {
		Entries []atomEntry `xml:"entry"`
	}

	titleRe := regexp.MustCompile(`^([\w\-/]+)\s*-\s*(.+?)\s*\((\d+)\)`)

	for _, formType := range formTypes {
		apiUrl := fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=%s&company=&dateb=&owner=include&count=30&output=atom",
			url.QueryEscape(formType))

		req := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R()
		for k, v := range secHeaders {
			req.SetHeader(k, v)
		}
		resp, err := req.Get(apiUrl)
		if err != nil {
			logger.SugaredLogger.Warnf("USStockNotice fetch %s err: %v", formType, err)
			continue
		}

		var feed atomFeed
		// SEC 的 atom 声明 ISO-8859-1，必须设 CharsetReader 让 xml 解码器认得
		decoder := xml.NewDecoder(bytes.NewReader(resp.Body()))
		decoder.CharsetReader = charset.NewReaderLabel
		if err := decoder.Decode(&feed); err != nil {
			logger.SugaredLogger.Warnf("USStockNotice xml parse %s err: %v", formType, err)
			continue
		}

		for _, entry := range feed.Entries {
			// title 格式: "8-K - Xerox Holdings Corp (0001770450) (Issuer)"
			matches := titleRe.FindStringSubmatch(entry.Title)
			if len(matches) < 4 {
				continue
			}
			fType := matches[1]
			company := strings.TrimSpace(matches[2])
			cik := matches[3]

			key := fType + "|" + cik + "|" + entry.Updated
			if seen[key] {
				continue
			}
			seen[key] = true

			if tickerFilter != "" {
				tf := strings.ToLower(strings.TrimSpace(tickerFilter))
				if !strings.Contains(strings.ToLower(company), tf) && !strings.Contains(cik, tf) {
					continue
				}
			}

			// 解析 updated 时间（RFC3339 带时区）
			var updatedTime time.Time
			if t, e := time.Parse(time.RFC3339, entry.Updated); e == nil {
				updatedTime = t
			} else {
				updatedTime = time.Now()
			}

			// 解析 + 翻译 SEC summary
			filed, _, sizeStr, items := translateSECSummary(entry.Summary)

			// 构造干净的中文标题
			var displayTitle string
			if items != "" {
				displayTitle = items
			} else {
				// 没有 Item 列表（10-K/10-Q/6-K 这类一般不带），就显示申报信息
				parts := []string{}
				if filed != "" {
					parts = append(parts, "申报日期 "+filed)
				}
				if sizeStr != "" {
					parts = append(parts, "大小 "+sizeStr)
				}
				if len(parts) > 0 {
					displayTitle = strings.Join(parts, " · ")
				} else {
					displayTitle = formTypeDescription(fType)
				}
			}

			// 构造前端期望的 schema（兼容 A股 公告 UI）
			results = append(results, map[string]any{
				"codes": []map[string]any{
					{
						"stock_code":  cik,
						"short_name":  company,
						"market_code": "us",
						"ann_type":    fType,
					},
				},
				"title": displayTitle,
				"columns": []map[string]any{
					{"column_name": formTypeDescription(fType)},
				},
				"notice_date":  updatedTime.Local().Format("2006-01-02 15:04:05"),
				"display_time": updatedTime.Local().Format("2006-01-02 15:04:05"),
				"art_code":     "",
				"infoLink":     entry.Link.Href, // SEC EDGAR filing 页面，直接打开
			})
		}
	}
	return results
}

// SEC 8-K Item 代码翻译字典（标准的，所有公司都用这套）
var secItemTranslations = map[string]string{
	"1.01": "签订重大约束性协议",
	"1.02": "终止重大约束性协议",
	"1.03": "破产或接管",
	"1.04": "采矿事故披露",
	"2.01": "完成收购或处置",
	"2.02": "经营业绩与财务状况",
	"2.03": "产生直接财务义务",
	"2.04": "触发条件加速/增加直接财务义务",
	"2.05": "退出或处置相关成本",
	"2.06": "重大资产减值",
	"3.01": "退市通知",
	"3.02": "未注册股权出售",
	"3.03": "重大修改证券持有人权利",
	"4.01": "审计师变更",
	"4.02": "不再依赖之前发布的财务报表",
	"5.01": "公司控制权变更",
	"5.02": "董事或高管离任/任命",
	"5.03": "修改公司章程或细则",
	"5.04": "临时暂停员工股票交易",
	"5.05": "修改公司道德规范",
	"5.07": "提交事项至证券持有人表决",
	"5.08": "股东董事提名",
	"6.01": "ABS 信息材料",
	"7.01": "公平披露规则披露",
	"8.01": "其他重大事件",
	"9.01": "财务报表及附件",
}

// translateSECSummary 解析 SEC summary 字段，提取关键信息并翻译
// 返回：(申报日期, 申报编号, 文件大小, 翻译后的 Item 列表)
func translateSECSummary(summary string) (filed, accNo, size, items string) {
	// 去 HTML 实体
	summary = html.UnescapeString(summary)

	filedRe := regexp.MustCompile(`Filed:\s*</?b>\s*([\d-]+)`)
	accNoRe := regexp.MustCompile(`AccNo:\s*</?b>\s*([\d-]+)`)
	sizeRe := regexp.MustCompile(`Size:\s*</?b>\s*([\d.]+\s*[KMG]?B)`)
	// 兼容 "Item 7.01: Regulation FD Disclosure" 这种格式（可能跟 <br>、</b>、< 等结束）
	itemRe := regexp.MustCompile(`Item\s+(\d+\.\d+):\s*([^<]+?)\s*(?:<|$)`)

	if m := filedRe.FindStringSubmatch(summary); len(m) > 1 {
		filed = m[1]
	}
	if m := accNoRe.FindStringSubmatch(summary); len(m) > 1 {
		accNo = m[1]
	}
	if m := sizeRe.FindStringSubmatch(summary); len(m) > 1 {
		size = m[1]
	}

	var itemTexts []string
	seenItems := map[string]bool{}
	for _, m := range itemRe.FindAllStringSubmatch(summary, -1) {
		itemNum := m[1]
		if seenItems[itemNum] {
			continue
		}
		seenItems[itemNum] = true
		if cn, ok := secItemTranslations[itemNum]; ok {
			itemTexts = append(itemTexts, "Item "+itemNum+" "+cn)
		} else {
			// 未知 item 保留英文原文
			itemTexts = append(itemTexts, "Item "+itemNum+" "+strings.TrimSpace(m[2]))
		}
	}
	items = strings.Join(itemTexts, " · ")
	return
}

// formTypeDescription 把 SEC form type 翻译成中文说明
func formTypeDescription(formType string) string {
	switch formType {
	case "8-K":
		return "8-K 重大事件"
	case "10-K":
		return "10-K 年报"
	case "10-Q":
		return "10-Q 季报"
	case "6-K":
		return "6-K 外国公司报告"
	case "20-F":
		return "20-F 外国年报"
	case "DEF 14A":
		return "DEF 14A 委托书"
	case "S-1":
		return "S-1 招股说明书"
	case "4":
		return "4 内部人交易"
	default:
		return formType + " 公告"
	}
}

func (m MarketNewsApi) StockNotice(stock_list string) []any {
	var stockCodes []string
	for _, stockCode := range strings.Split(stock_list, ",") {
		if strutil.ContainsAny(stockCode, []string{"."}) {
			stockCode = strings.Split(stockCode, ".")[0]
			stockCodes = append(stockCodes, stockCode)
		} else {
			stockCode = strutil.ReplaceWithMap(stockCode, map[string]string{
				"sh":  "",
				"sz":  "",
				"gb_": "",
				"us":  "",
				"us_": "",
			})
			stockCodes = append(stockCodes, stockCode)
		}
	}

	url := "https://np-anotice-stock.eastmoney.com/api/security/ann?page_size=50&page_index=1&ann_type=SHA%2CCYB%2CSZA%2CBJA%2CINV&client_source=web&f_node=0&stock_list=" + strings.Join(stockCodes, ",")
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "np-anotice-stock.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/notices/hsa/5.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	respMap := map[string]any{}

	if err != nil {
		return []any{}
	}
	json.Unmarshal(resp.Body(), &respMap)
	//logger.SugaredLogger.Infof("resp:%+v", respMap["data"])
	return (respMap["data"].(map[string]any))["list"].([]any)
}

func (m MarketNewsApi) EMDictCode(code string, cache *freecache.Cache) []any {
	respMap := map[string]any{}

	d, _ := cache.Get([]byte(code))
	if d != nil {
		json.Unmarshal(d, &respMap)
		return respMap["data"].([]any)
	}

	url := "https://reportapi.eastmoney.com/report/bk"

	params := map[string]string{
		"bkCode": code,
	}
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "reportapi.eastmoney.com").
		SetHeader("Origin", "https://data.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/report/industry.jshtml").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetHeader("Content-Type", "application/json").
		SetQueryParams(params).Get(url)

	if err != nil {
		return []any{}
	}
	json.Unmarshal(resp.Body(), &respMap)
	//logger.SugaredLogger.Infof("resp:%+v", respMap["data"])
	cache.Set([]byte(code), resp.Body(), 60*60*24)
	return respMap["data"].([]any)
}

// TradingViewNewsByMarket 通用 TradingView 拉取，按市场过滤
// market: "HK" 港股, "US" 美股, "WLD" 全球
// source: 写入数据库的 source 字段名，便于按市场区分
func (m MarketNewsApi) TradingViewNewsByMarket(market, source string) *[]models.Telegraph {
	client := SharedHTTPClient
	config := GetSettingConfig()
	if config.HttpProxyEnabled && config.HttpProxy != "" {
		client.SetProxy(config.HttpProxy)
	}
	TVNews := &[]models.TVNews{}
	news := &[]models.Telegraph{}
	url := fmt.Sprintf("https://news-mediator.tradingview.com/news-flow/v2/news?filter=lang%%3Azh-Hans&filter=market_country%%3A%s&client=screener&streaming=false", market)

	resp, err := client.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "news-mediator.tradingview.com").
		SetHeader("Origin", "https://cn.tradingview.com").
		SetHeader("Referer", "https://cn.tradingview.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		return news
	}
	respMap := map[string]any{}
	if err := json.Unmarshal(resp.Body(), &respMap); err != nil {
		return news
	}
	items, err := json.Marshal(respMap["items"])
	if err != nil {
		return news
	}
	json.Unmarshal(items, TVNews)

	for i, a := range *TVNews {
		if i > 15 {
			break
		}
		detail := NewMarketNewsApi().TradingViewNewsDetail(a.Id)
		dataTime := time.Unix(int64(a.Published), 0).Local()
		description := ""
		sentimentResult := ""
		if detail != nil {
			description = detail.ShortDescription
			sentimentResult = AnalyzeSentiment(description).Description
		}
		if a.Title == "" {
			continue
		}
		telegraph := &models.Telegraph{
			Title:           a.Title,
			Content:         description,
			DataTime:        &dataTime,
			IsRed:           false,
			Time:            dataTime.Format("15:04:05"),
			Source:          source,
			Url:             fmt.Sprintf("https://cn.tradingview.com/news/%s", a.Id),
			SentimentResult: sentimentResult,
		}
		cnt := int64(0)
		if telegraph.Title == "" {
			db.Dao.Model(telegraph).Where("content=? and source=?", telegraph.Content, source).Count(&cnt)
		} else {
			db.Dao.Model(telegraph).Where("title=? and source=?", telegraph.Title, source).Count(&cnt)
		}
		if cnt > 0 {
			continue
		}
		db.Dao.Model(&models.Telegraph{}).Where("time=? and title=? and source=?", telegraph.Time, telegraph.Title, source).FirstOrCreate(&telegraph)
		*news = append(*news, *telegraph)
	}
	return news
}

// EastmoneyHKUSNews 拉取东方财富港股/美股资讯
// column: "104" 港股资讯 / "105" 美股资讯
// source: 写库 source 字段名
func (m MarketNewsApi) EastmoneyHKUSNews(column, source string) *[]models.Telegraph {
	news := &[]models.Telegraph{}
	url := fmt.Sprintf("https://newsapi.eastmoney.com/kuaixun/v2/api/list?column=%s&pageindex=1&pagesize=30&_=%d", column, time.Now().UnixMilli())
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "newsapi.eastmoney.com").
		SetHeader("Referer", "https://kuaixun.eastmoney.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("EastmoneyHKUSNews err: %v", err)
		return news
	}
	body := string(resp.Body())
	// 接口可能返回 JSONP（callback(...)）或纯 JSON，统一处理
	if idx := strings.Index(body, "("); idx >= 0 && strings.HasSuffix(strings.TrimSpace(body), ")") {
		body = body[idx+1 : len(body)-1]
	}
	parsed := gjson.Parse(body)
	items := parsed.Get("LivesList")
	if !items.Exists() {
		items = parsed.Get("data")
	}
	if !items.Exists() || !items.IsArray() {
		return news
	}
	items.ForEach(func(_, v gjson.Result) bool {
		title := v.Get("title").String()
		if title == "" {
			title = v.Get("digest").String()
		}
		content := v.Get("digest").String()
		if content == "" {
			content = title
		}
		showTime := v.Get("showtime").String()
		if showTime == "" {
			showTime = v.Get("ctime").String()
		}
		var dataTime time.Time
		if t, e := time.ParseInLocation("2006-01-02 15:04:05", showTime, time.Local); e == nil {
			dataTime = t
		} else {
			dataTime = time.Now()
		}
		shareUrl := v.Get("url_unique").String()
		if shareUrl == "" {
			shareUrl = v.Get("url").String()
		}
		telegraph := &models.Telegraph{
			Title:           title,
			Content:         content,
			Time:            dataTime.Format("15:04:05"),
			DataTime:        &dataTime,
			Url:             shareUrl,
			Source:          source,
			IsRed:           false,
			SentimentResult: AnalyzeSentiment(content).Description,
		}
		cnt := int64(0)
		if telegraph.Title == "" {
			db.Dao.Model(telegraph).Where("content=? and source=?", telegraph.Content, source).Count(&cnt)
		} else {
			db.Dao.Model(telegraph).Where("title=? and source=?", telegraph.Title, source).Count(&cnt)
		}
		if cnt > 0 {
			return true
		}
		db.Dao.Model(&models.Telegraph{}).Where("time=? and title=? and source=?", telegraph.Time, telegraph.Title, source).FirstOrCreate(&telegraph)
		*news = append(*news, *telegraph)
		return true
	})
	return news
}

// SinaHKUSNews 抓取新浪港股/美股专题页新闻
// market: "hk" 港股 / "us" 美股
// source: 写库的 source 字段名（如 "新浪-港股" / "新浪-美股"）
func (m MarketNewsApi) SinaHKUSNews(market, source string) *[]models.Telegraph {
	news := &[]models.Telegraph{}

	var pageUrl, pathFilter string
	switch strings.ToLower(market) {
	case "hk":
		pageUrl = "https://finance.sina.com.cn/stock/hkstock/"
		pathFilter = "/hkstock/"
	case "us":
		pageUrl = "https://finance.sina.com.cn/stock/usstock/"
		pathFilter = "/usstock/"
	default:
		return news
	}

	resp, err := SharedHTTPClient.SetTimeout(15*time.Second).R().
		SetHeader("Referer", "https://finance.sina.com.cn/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36").
		Get(pageUrl)
	if err != nil {
		logger.SugaredLogger.Errorf("SinaHKUSNews fetch %s err: %v", source, err)
		return news
	}

	// 新浪是 GB18030 编码，用 charset.NewReader 自动检测 <meta charset> 并转 UTF-8
	contentType := resp.Header().Get("Content-Type")
	utf8Reader, err := charset.NewReader(bytes.NewReader(resp.Body()), contentType)
	if err != nil {
		logger.SugaredLogger.Errorf("SinaHKUSNews charset %s err: %v", source, err)
		return news
	}

	doc, err := goquery.NewDocumentFromReader(utf8Reader)
	if err != nil {
		logger.SugaredLogger.Errorf("SinaHKUSNews parse %s err: %v", source, err)
		return news
	}

	seen := map[string]bool{}
	datePattern := regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`)

	doc.Find("a").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, ok := s.Attr("href")
		if !ok {
			return true
		}
		// 必须是 .shtml 结尾、必须在港股/美股 path 下
		if !strings.HasSuffix(href, ".shtml") {
			return true
		}
		if !strings.Contains(href, pathFilter) {
			return true
		}
		title := strings.TrimSpace(s.Text())
		runes := []rune(title)
		if len(runes) < 6 || len(runes) > 200 {
			return true
		}
		if seen[title] {
			return true
		}
		seen[title] = true

		// 从 URL 提取日期
		var dataTime time.Time
		if matches := datePattern.FindStringSubmatch(href); len(matches) > 1 {
			if t, e := time.ParseInLocation("2006-01-02", matches[1], time.Local); e == nil {
				dataTime = t
			}
		}
		if dataTime.IsZero() {
			dataTime = time.Now()
		}

		telegraph := &models.Telegraph{
			Title:           title,
			Content:         title, // 列表页只有标题
			Time:            dataTime.Format("15:04:05"),
			DataTime:        &dataTime,
			Url:             href,
			Source:          source,
			IsRed:           false,
			SentimentResult: AnalyzeSentiment(title).Description,
		}
		cnt := int64(0)
		db.Dao.Model(telegraph).Where("title=? and source=?", telegraph.Title, source).Count(&cnt)
		if cnt > 0 {
			return true
		}
		db.Dao.Model(&models.Telegraph{}).Where("time=? and title=? and source=?", telegraph.Time, telegraph.Title, source).FirstOrCreate(&telegraph)
		*news = append(*news, *telegraph)
		if len(*news) >= 30 {
			return false
		}
		return true
	})

	return news
}

func (m MarketNewsApi) TradingViewNews() *[]models.Telegraph {
	client := SharedHTTPClient
	config := GetSettingConfig()
	if config.HttpProxyEnabled && config.HttpProxy != "" {
		client.SetProxy(config.HttpProxy)
	}
	TVNews := &[]models.TVNews{}
	news := &[]models.Telegraph{}
	//	url := "https://news-mediator.tradingview.com/news-flow/v2/news?filter=lang:zh-Hans&filter=area:WLD&client=screener&streaming=false"
	//url := "https://news-mediator.tradingview.com/news-flow/v2/news?filter=area%3AWLD&filter=lang%3Azh-Hans&client=screener&streaming=false"
	url := "https://news-mediator.tradingview.com/news-flow/v2/news?filter=lang%3Azh-Hans&client=screener&streaming=false"

	resp, err := client.SetTimeout(time.Duration(15)*time.Second).R().
		SetHeader("Host", "news-mediator.tradingview.com").
		SetHeader("Origin", "https://cn.tradingview.com").
		SetHeader("Referer", "https://cn.tradingview.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		//logger.SugaredLogger.Errorf("TradingViewNews err:%s", err.Error())
		return news
	}
	respMap := map[string]any{}
	err = json.Unmarshal(resp.Body(), &respMap)
	if err != nil {
		return news
	}
	items, err := json.Marshal(respMap["items"])
	if err != nil {
		return news
	}
	json.Unmarshal(items, TVNews)

	for i, a := range *TVNews {
		if i > 10 {
			break
		}
		detail := NewMarketNewsApi().TradingViewNewsDetail(a.Id)
		dataTime := time.Unix(int64(a.Published), 0).Local()
		description := ""
		sentimentResult := ""
		if detail != nil {
			description = detail.ShortDescription
			sentimentResult = AnalyzeSentiment(description).Description
		}
		if a.Title == "" {
			continue
		}
		telegraph := &models.Telegraph{
			Title:           a.Title,
			Content:         description,
			DataTime:        &dataTime,
			IsRed:           false,
			Time:            dataTime.Format("15:04:05"),
			Source:          "外媒",
			Url:             fmt.Sprintf("https://cn.tradingview.com/news/%s", a.Id),
			SentimentResult: sentimentResult,
		}
		cnt := int64(0)
		if telegraph.Title == "" {
			db.Dao.Model(telegraph).Where("content=?", telegraph.Content).Count(&cnt)
		} else {
			db.Dao.Model(telegraph).Where("title=?", telegraph.Title).Count(&cnt)
		}
		if cnt > 0 {
			continue
		}
		db.Dao.Model(&models.Telegraph{}).Where("time=? and title=? and source=?", telegraph.Time, telegraph.Title, "外媒").FirstOrCreate(&telegraph)
		*news = append(*news, *telegraph)
	}
	return news
}
func (m MarketNewsApi) TradingViewNewsDetail(id string) *models.TVNewsDetail {
	//https://news-headlines.tradingview.com/v3/story?id=panews%3A9be7cf057e3f9%3A0&lang=zh-Hans
	newsDetail := &models.TVNewsDetail{}
	newsUrl := fmt.Sprintf("https://news-headlines.tradingview.com/v3/story?id=%s&lang=zh-Hans", url.QueryEscape(id))

	client := SharedHTTPClient
	config := GetSettingConfig()
	if config.HttpProxyEnabled && config.HttpProxy != "" {
		client.SetProxy(config.HttpProxy)
	}
	request := client.SetTimeout(time.Duration(3) * time.Second).R()
	_, err := request.
		SetHeader("Host", "news-headlines.tradingview.com").
		SetHeader("Origin", "https://cn.tradingview.com").
		SetHeader("Referer", "https://cn.tradingview.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:146.0) Gecko/20100101 Firefox/146.0").
		//SetHeader("TE", "trailers").
		//SetHeader("Priority", "u=4").
		//SetHeader("Connection", "keep-alive").
		SetResult(newsDetail).
		Get(newsUrl)
	if err != nil {
		logger.SugaredLogger.Errorf("TradingViewNewsDetail err:%s", err.Error())
		return newsDetail
	}
	//logger.SugaredLogger.Infof("resp:%+v", newsDetail)
	return newsDetail
}

func (m MarketNewsApi) XUEQIUHotStock(size int, marketType string) *[]models.HotItem {
	cookieHeader, cookieErr := FetchXueqiuCookiesViaChromedp("", 30*time.Second, "https://xueqiu.com/hq#hot")
	if cookieErr != nil {
		logger.SugaredLogger.Warnf("雪球 chromedp 获取 cookie 失败: %v", cookieErr)
	}

	url := fmt.Sprintf("https://stock.xueqiu.com/v5/stock/hot_stock/list.json?page=1&size=%d&_type=%s&type=%s", size, marketType, marketType)
	res := &models.XUEQIUHot{}
	request := SharedHTTPClient.SetTimeout(time.Duration(30) * time.Second).R()
	request.SetHeader("Host", "stock.xueqiu.com").
		SetHeader("Origin", "https://xueqiu.com").
		SetHeader("Referer", "https://xueqiu.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0")
	if cookieErr == nil && cookieHeader != "" {
		request.SetHeader("Cookie", cookieHeader)
	}
	_, err := request.SetResult(res).Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("XUEQIUHotStock err:%s", err.Error())
		return &[]models.HotItem{}
	}
	if res.ErrorCode != 0 {
		logger.SugaredLogger.Errorf("XUEQIUHotStock API error: code=%d, desc=%s", res.ErrorCode, res.ErrorDescription)
		return &[]models.HotItem{}
	}
	return &res.Data.Items
}

func (m MarketNewsApi) HotEvent(size int) *[]models.HotEvent {
	cookieHeader, cookieErr := FetchXueqiuCookiesViaChromedp("", 30*time.Second, "https://xueqiu.com/hq#hot")
	if cookieErr != nil {
		logger.SugaredLogger.Warnf("雪球 chromedp 获取 cookie 失败: %v", cookieErr)
	}

	events := &[]models.HotEvent{}
	sprintf := fmt.Sprintf("https://xueqiu.com/hot_event/list.json?count=%d", size)
	request := SharedHTTPClient.SetTimeout(time.Duration(30) * time.Second).R()
	request.SetHeader("Host", "xueqiu.com").
		SetHeader("Referer", "https://xueqiu.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0")
	if cookieErr == nil && cookieHeader != "" {
		request.SetHeader("Cookie", cookieHeader)
	}
	resp, err := request.Get(sprintf)
	if err != nil {
		logger.SugaredLogger.Errorf("HotEvent err:%s", err.Error())
		return events
	}
	respMap := map[string]any{}
	err = json.Unmarshal(resp.Body(), &respMap)
	if err != nil {
		logger.SugaredLogger.Errorf("HotEvent json unmarshal err:%s", err.Error())
		return events
	}
	items, err := json.Marshal(respMap["list"])
	if err != nil {
		return events
	}
	json.Unmarshal(items, events)
	return events
}

func (m MarketNewsApi) HotTopic(size int) []any {
	url := "https://gubatopic.eastmoney.com/interface/GetData.aspx?path=newtopic/api/Topic/HomePageListRead"
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "gubatopic.eastmoney.com").
		SetHeader("Origin", "https://gubatopic.eastmoney.com").
		SetHeader("Referer", "https://gubatopic.eastmoney.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetFormData(map[string]string{
			"param": fmt.Sprintf("ps=%d&p=1&type=0", size),
			"path":  "newtopic/api/Topic/HomePageListRead",
			"env":   "2",
		}).
		Post(url)
	if err != nil {
		logger.SugaredLogger.Errorf("HotTopic err:%s", err.Error())
		return []any{}
	}
	//logger.SugaredLogger.Infof("HotTopic:%s", resp.Body())
	respMap := map[string]any{}
	err = json.Unmarshal(resp.Body(), &respMap)
	return respMap["re"].([]any)

}

func (m MarketNewsApi) InvestCalendar(yearMonth string) []any {
	if yearMonth == "" {
		yearMonth = time.Now().Format("2006-01")
	}

	url := "https://app.jiuyangongshe.com/jystock-app/api/v1/timeline/list"
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "app.jiuyangongshe.com").
		SetHeader("Origin", "https://www.jiuyangongshe.com").
		SetHeader("Referer", "https://www.jiuyangongshe.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetHeader("Content-Type", "application/json").
		SetHeader("token", "1cc6380a05c652b922b3d85124c85473").
		SetHeader("platform", "3").
		SetHeader("Cookie", "SESSION=NDZkNDU2ODYtODEwYi00ZGZkLWEyY2ItNjgxYzY4ZWMzZDEy").
		SetHeader("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10)).
		SetBody(map[string]string{
			"date":  yearMonth,
			"grade": "0",
		}).
		Post(url)
	if err != nil {
		logger.SugaredLogger.Errorf("InvestCalendar err:%s", err.Error())
		return []any{}
	}
	//logger.SugaredLogger.Infof("InvestCalendar:%s", resp.Body())
	respMap := map[string]any{}
	err = json.Unmarshal(resp.Body(), &respMap)
	return respMap["data"].([]any)

}

func (m MarketNewsApi) ClsCalendar() []any {
	url := "https://www.cls.cn/api/calendar/web/list?app=CailianpressWeb&flag=0&os=web&sv=8.4.6&type=0&sign=4b839750dc2f6b803d1c8ca00d2b40be"
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "www.cls.cn").
		SetHeader("Origin", "https://www.cls.cn").
		SetHeader("Referer", "https://www.cls.cn/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("ClsCalendar err:%s", err.Error())
		return []any{}
	}
	respMap := map[string]any{}
	err = json.Unmarshal(resp.Body(), &respMap)
	return respMap["data"].([]any)
}

func (m MarketNewsApi) GetGDP() *models.GDPResp {
	res := &models.GDPResp{}

	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?callback=data&columns=REPORT_DATE%2CTIME%2CDOMESTICL_PRODUCT_BASE%2CFIRST_PRODUCT_BASE%2CSECOND_PRODUCT_BASE%2CTHIRD_PRODUCT_BASE%2CSUM_SAME%2CFIRST_SAME%2CSECOND_SAME%2CTHIRD_SAME&pageNumber=1&pageSize=20&sortColumns=REPORT_DATE&sortTypes=-1&source=WEB&client=WEB&reportName=RPT_ECONOMY_GDP&p=1&pageNo=1&pageNum=1&_=" + strconv.FormatInt(time.Now().Unix(), 10)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "datacenter-web.eastmoney.com").
		SetHeader("Origin", "https://datacenter.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/cjsj/gdp.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("GDP err:%s", err.Error())
		return res
	}
	body := resp.Body()
	////logger.SugaredLogger.Debugf("GDP:%s", body)
	vm := otto.New()
	vm.Run("function data(res){return res};")

	val, err := vm.Run(body)
	if err != nil {
		logger.SugaredLogger.Errorf("GDP err:%s", err.Error())
		return res
	}
	data, _ := val.Object().Value().Export()
	//logger.SugaredLogger.Infof("GDP:%v", data)
	marshal, err := json.Marshal(data)
	if err != nil {
		return res
	}
	json.Unmarshal(marshal, &res)
	//logger.SugaredLogger.Infof("GDP:%+v", res)
	return res
}

func (m MarketNewsApi) GetCPI() *models.CPIResp {
	res := &models.CPIResp{}

	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?callback=data&columns=REPORT_DATE%2CTIME%2CNATIONAL_SAME%2CNATIONAL_BASE%2CNATIONAL_SEQUENTIAL%2CNATIONAL_ACCUMULATE%2CCITY_SAME%2CCITY_BASE%2CCITY_SEQUENTIAL%2CCITY_ACCUMULATE%2CRURAL_SAME%2CRURAL_BASE%2CRURAL_SEQUENTIAL%2CRURAL_ACCUMULATE&pageNumber=1&pageSize=20&sortColumns=REPORT_DATE&sortTypes=-1&source=WEB&client=WEB&reportName=RPT_ECONOMY_CPI&p=1&pageNo=1&pageNum=1&_=" + strconv.FormatInt(time.Now().Unix(), 10)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "datacenter-web.eastmoney.com").
		SetHeader("Origin", "https://datacenter.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/cjsj/gdp.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("GetCPI err:%s", err.Error())
		return res
	}
	body := resp.Body()
	////logger.SugaredLogger.Debugf("GetCPI:%s", body)
	vm := otto.New()
	vm.Run("function data(res){return res};")

	val, err := vm.Run(body)
	if err != nil {
		logger.SugaredLogger.Errorf("GetCPI err:%s", err.Error())
		return res
	}
	data, _ := val.Object().Value().Export()
	//logger.SugaredLogger.Infof("GetCPI:%v", data)
	marshal, err := json.Marshal(data)
	if err != nil {
		return res
	}
	json.Unmarshal(marshal, &res)
	//logger.SugaredLogger.Infof("GetCPI:%+v", res)
	return res
}

// GetPPI PPI
func (m MarketNewsApi) GetPPI() *models.PPIResp {
	res := &models.PPIResp{}
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?callback=data&columns=REPORT_DATE,TIME,BASE,BASE_SAME,BASE_ACCUMULATE&pageNumber=1&pageSize=20&sortColumns=REPORT_DATE&sortTypes=-1&source=WEB&client=WEB&reportName=RPT_ECONOMY_PPI&p=1&pageNo=1&pageNum=1&_=" + strconv.FormatInt(time.Now().Unix(), 10)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "datacenter-web.eastmoney.com").
		SetHeader("Origin", "https://datacenter.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/cjsj/gdp.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("GetPPI err:%s", err.Error())
		return res
	}
	body := resp.Body()
	vm := otto.New()
	vm.Run("function data(res){return res};")

	val, err := vm.Run(body)
	if err != nil {
		return res
	}
	data, _ := val.Object().Value().Export()
	marshal, err := json.Marshal(data)
	if err != nil {
		return res
	}
	json.Unmarshal(marshal, &res)
	return res
}

func (m MarketNewsApi) GetPMI() *models.PMIResp {
	res := &models.PMIResp{}
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?callback=data&columns=REPORT_DATE%2CTIME%2CMAKE_INDEX%2CMAKE_SAME%2CNMAKE_INDEX%2CNMAKE_SAME&pageNumber=1&pageSize=20&sortColumns=REPORT_DATE&sortTypes=-1&source=WEB&client=WEB&reportName=RPT_ECONOMY_PMI&p=1&pageNo=1&pageNum=1&_=" + strconv.FormatInt(time.Now().Unix(), 10)
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "datacenter-web.eastmoney.com").
		SetHeader("Origin", "https://datacenter.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/cjsj/gdp.html").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		return res
	}
	body := resp.Body()
	vm := otto.New()
	vm.Run("function data(res){return res};")

	val, err := vm.Run(body)
	if err != nil {
		return res
	}
	data, _ := val.Object().Value().Export()
	marshal, err := json.Marshal(data)
	if err != nil {
		return res
	}
	json.Unmarshal(marshal, &res)
	return res

}
func (m MarketNewsApi) GetIndustryReportInfo(infoCode string) string {
	url := "https://data.eastmoney.com/report/zw_industry.jshtml?infocode=" + infoCode
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "data.eastmoney.com").
		SetHeader("Origin", "https://data.eastmoney.com").
		SetHeader("Referer", "https://data.eastmoney.com/report/industry.jshtml").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("GetIndustryReportInfo err:%s", err.Error())
		return ""
	}
	body := resp.Body()
	////logger.SugaredLogger.Debugf("GetIndustryReportInfo:%s", body)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	title, _ := doc.Find("div.c-title").Html()
	content, _ := doc.Find("div.ctx-content").Html()
	//logger.SugaredLogger.Infof("GetIndustryReportInfo:\n%s\n%s", title, content)
	markdown, err := util.HTMLToMarkdown(title + content)
	if err != nil {
		return ""
	}
	//logger.SugaredLogger.Infof("GetIndustryReportInfo markdown:\n%s", markdown)
	return markdown
}

func (receiver MarketNewsApi) GetSecuritiesCompanyOpinion(startDate string, endDate string) *models.SecuritiesCompanyOpinionResp {
	res := models.SecuritiesCompanyOpinionResp{}

	url := fmt.Sprintf("https://reportapi.eastmoney.com/report/jg?cb=data&pageSize=50&beginTime=%s&endTime=%s&pageNo=1&fields=&qType=4&orgCode=&author=&p=1&pageNum=1&pageNumber=1&_=%d", startDate, endDate, time.Now().Unix())
	resp, err := SharedHTTPClient.SetTimeout(time.Duration(30)*time.Second).R().
		SetHeader("Host", "reportapi.eastmoney.com").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("GetSecuritiesCompanyOpinion err:%s", err.Error())
		return &res
	}
	body := resp.Body()
	vm := otto.New()
	vm.Run("function data(res){return res};")

	val, _ := vm.Run(body)

	data, _ := val.Object().Value().Export()
	marshal, _ := json.Marshal(data)

	json.Unmarshal(marshal, &res)

	for _, d := range (&res).Data {
		//logger.SugaredLogger.Debugf("PublishDate: %s,OrgSName: %s,Title: %s,EncodeUrl: %s", d.PublishDate, d.OrgSName, d.Title, d.EncodeUrl)
		markdown := receiver.GetSecuritiesCompanyOpinionContent(d.OrgSName, d.EncodeUrl)
		d.OpinionData = markdown
	}
	return &res
}

func (m MarketNewsApi) GetSecuritiesCompanyOpinionContent(OrgSName, encodeUrl string) string {
	url := "https://data.eastmoney.com/report/zw_brokerreport.jshtml?encodeUrl=" + encodeUrl
	resp, _ := SharedHTTPClient.R().
		SetHeader("Host", "data.eastmoney.com").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		Get(url)
	body := resp.Body()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	title, _ := doc.Find("div.c-title").Html()
	content, _ := doc.Find("div.ctx-content").Html()
	markdown, err := util.HTMLToMarkdown("<h1>" + OrgSName + "</h1>" + title + content)
	if err != nil {
	}
	return markdown
}

func (m MarketNewsApi) ReutersNew() *models.ReutersNews {
	client := SharedHTTPClient
	config := GetSettingConfig()
	if config.HttpProxyEnabled && config.HttpProxy != "" {
		client.SetProxy(config.HttpProxy)
	}
	news := &models.ReutersNews{}
	//url := "https://www.reuters.com/pf/api/v3/content/fetch/articles-by-section-alias-or-id-v1?query={\"arc-site\":\"reuters\",\"fetch_type\":\"collection\",\"offset\":0,\"section_id\":\"/world/\",\"size\":9,\"uri\":\"/world/\",\"website\":\"reuters\"}&d=300&mxId=00000000&_website=reuters"
	url := "https://www.reuters.com/pf/api/v3/content/fetch/recent-stories-by-sections-v1?query=%7B%22section_ids%22%3A%22%2Fworld%2F%22%2C%22size%22%3A4%2C%22website%22%3A%22reuters%22%7D&d=334&mxId=00000000&_website=reuters"
	_, err := client.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Host", "www.reuters.com").
		SetHeader("Origin", "https://www.reuters.com").
		SetHeader("Referer", "https://www.reuters.com/world/china/").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0").
		SetResult(news).
		Get(url)
	if err != nil {
		logger.SugaredLogger.Errorf("ReutersNew err:%s", err.Error())
		return news
	}
	//logger.SugaredLogger.Infof("Articles:%+v", news.Result.Articles)
	return news
}

func (m MarketNewsApi) InteractiveAnswer(page int, pageSize int, keyWord string) *models.InteractiveAnswer {
	client := SharedHTTPClient
	config := GetSettingConfig()
	if config.HttpProxyEnabled && config.HttpProxy != "" {
		client.SetProxy(config.HttpProxy)
	}
	url := fmt.Sprintf("https://irm.cninfo.com.cn/newircs/index/search?_t=%d", time.Now().Unix())
	answers := &models.InteractiveAnswer{}
	//logger.SugaredLogger.Infof("请求url:%s", url)
	_, err := client.SetTimeout(time.Duration(5)*time.Second).R().
		SetHeader("Host", "irm.cninfo.com.cn").
		SetHeader("Origin", "https://irm.cninfo.com.cn").
		SetHeader("Referer", "https://irm.cninfo.com.cn/views/interactiveAnswer").
		SetHeader("handleError", "true").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0").
		SetFormData(map[string]string{
			"pageNo":      convertor.ToString(page),
			"pageSize":    convertor.ToString(pageSize),
			"searchTypes": "11",
			"highLight":   "true",
			"keyWord":     keyWord,
		}).
		SetResult(answers).
		Post(url)
	if err != nil {
		logger.SugaredLogger.Errorf("InteractiveAnswer-err:%+v", err)
	}
	//logger.SugaredLogger.Debugf("InteractiveAnswer-resp:%s", resp.Body())
	return answers

}

func (m MarketNewsApi) CailianpressWeb(searchWords string) *models.CailianpressWeb {
	res := &models.CailianpressWeb{}
	_, err := SharedHTTPClient.SetTimeout(time.Second*10).R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Host", "www.cls.cn").
		SetHeader("Origin", "https://www.cls.cn").
		SetHeader("Referer", "https://www.cls.cn/telegraph").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.60").
		SetBody(fmt.Sprintf(`{"app":"CailianpressWeb","os":"web","sv":"8.4.6","category":"","keyword":"%s"}`, searchWords)).
		SetResult(res).
		Post("https://www.cls.cn/api/csw?app=CailianpressWeb&os=web&sv=8.4.6&sign=9f8797a1f4de66c2370f7a03990d2737")
	if err != nil {
		return nil
	}
	logger.SugaredLogger.Debug(res)

	return res
}

// GetNews24HoursListBySources 按 source 列表（多源）过滤近24小时新闻
func (m MarketNewsApi) GetNews24HoursListBySources(sources []string, limit int) *[]*models.Telegraph {
	news := &[]*models.Telegraph{}
	if len(sources) == 0 {
		return news
	}
	db.Dao.Model(news).Preload("TelegraphTags").
		Where("source IN ? AND created_at > ?", sources, time.Now().Add(-24*time.Hour)).
		Order("data_time desc, is_red desc").
		Limit(limit).
		Find(news)

	// 内容去重
	uniqueNews := make([]*models.Telegraph, 0, len(*news))
	seen := make(map[string]bool)
	for _, item := range *news {
		key := item.Content
		if key == "" {
			key = item.Title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		uniqueNews = append(uniqueNews, item)
	}
	return &uniqueNews
}

func (m MarketNewsApi) GetNews24HoursList(source string, limit int) *[]*models.Telegraph {
	news := &[]*models.Telegraph{}
	if source != "" {
		db.Dao.Model(news).Preload("TelegraphTags").Where("source=? and created_at>?", source, time.Now().Add(-24*time.Hour)).Order("data_time desc,is_red desc").Limit(limit).Find(news)
	} else {
		db.Dao.Model(news).Preload("TelegraphTags").Where("created_at>?", time.Now().Add(-24*time.Hour)).Order("data_time desc,is_red desc").Limit(limit).Find(news)
	}
	// 内容去重
	uniqueNews := make([]*models.Telegraph, 0)
	seenContent := make(map[string]bool)
	for _, item := range *news {
		tags := &[]models.Tags{}
		db.Dao.Model(&models.Tags{}).Where("id in ?", lo.Map(item.TelegraphTags, func(item models.TelegraphTags, index int) uint {
			return item.TagId
		})).Find(&tags)
		tagNames := lo.Map(*tags, func(item models.Tags, index int) string {
			return item.Name
		})
		item.SubjectTags = tagNames
		//logger.SugaredLogger.Infof("tagNames %v ，SubjectTags：%s", tagNames, item.SubjectTags)
		// 使用内容作为去重键值，可以考虑只使用内容的前几个字符或哈希值
		contentKey := strings.TrimSpace(item.Content)
		if contentKey != "" && !seenContent[contentKey] {
			seenContent[contentKey] = true
			uniqueNews = append(uniqueNews, item)
		}
	}
	return &uniqueNews
}

// GetNewsListData 分页获取新闻列表，page 从 1 开始，pageSize 为每页条数（<=0 时默认 20）。返回本页去重后的列表与总条数。
func (m MarketNewsApi) GetNewsListData(keyWord string, startTime time.Time, page, pageSize int) (*[]*models.Telegraph, int64) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	whereCond := "created_at>? and (title like ? or content like ?)"
	args := []any{startTime, "%" + keyWord + "%", "%" + keyWord + "%"}
	var total int64
	db.Dao.Model(&models.Telegraph{}).Where(whereCond, args...).Count(&total)
	offset := (page - 1) * pageSize
	news := &[]*models.Telegraph{}
	db.Dao.Model(news).Preload("TelegraphTags").Where(whereCond, args...).Order("data_time desc,is_red desc").Offset(offset).Limit(pageSize).Find(news)
	// 内容去重
	uniqueNews := make([]*models.Telegraph, 0)
	seenContent := make(map[string]bool)
	for _, item := range *news {
		contentKey := strings.TrimSpace(item.Content)
		if item.Title != "" {
			contentKey = strings.TrimSpace(item.Title)
		}
		if contentKey != "" && !seenContent[contentKey] {
			seenContent[contentKey] = true
			uniqueNews = append(uniqueNews, item)
		}
	}
	return &uniqueNews, total
}

func (m MarketNewsApi) GetUplimitHot(date string, limit int) map[string]any {
	if limit <= 0 {
		limit = 20
	}
	if date == "" {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		date = time.Now().In(loc).Format("2006-01-02")
	}
	apiUrl := fmt.Sprintf("https://api.zizizaizai.com/v3/open/review/uplimit/hot?date1=%s&limit=%d", date, limit)
	resp, err := SharedHTTPClient.SetTimeout(15*time.Second).R().
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36").
		SetHeader("Accept", "application/json").
		Get(apiUrl)
	if err != nil {
		logger.SugaredLogger.Errorf("GetUplimitHot error: %v", err)
		return map[string]any{"code": 50000, "message": "请求失败"}
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		logger.SugaredLogger.Errorf("GetUplimitHot unmarshal error: %v", err)
		return map[string]any{"code": 50000, "message": "数据解析失败"}
	}
	return result
}
