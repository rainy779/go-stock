<script setup>
import {onBeforeMount, onBeforeUnmount, ref, computed, h} from 'vue'
import {GetConfig, GetUplimitHot, IsTradingTime, IsTradingDay, GetLatestTradingDay, AnalyzeUplimitWithAI, GetAiConfigs, GetUplimitAISummaries, GetUplimitAISummaryDetail, DeleteUplimitAISummary} from "../../wailsjs/go/main/App";
import {NButton, NText, NTag, NTooltip, NProgress, useMessage} from "naive-ui";
import StockLightweightKlineChart from "./StockLightweightKlineChart.vue";

const message = useMessage()
const loading = ref(false)
const rawData = ref(null)
const selectedPlate = ref(null)
const showPlateModal = ref(false)
const showKlineModal = ref(false)
const klineCode = ref('')
const klineName = ref('')
const activeView = ref('ladder')
const expandedLadders = ref([])
const darkTheme = ref(false)

function getCalendarTodayStr() {
  const t = new Date()
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`
}

/** 自然日「今天」，用于打开页默认选中日与仅在查看当天时自动刷新 */
const todayYMD = ref(getCalendarTodayStr())
const selectedDate = ref(getCalendarTodayStr())
let refreshTimer = null

function startAutoRefresh() {
  stopAutoRefresh()
  refreshTimer = setInterval(() => {
    if (selectedDate.value !== todayYMD.value) return
    IsTradingTime().then(trading => {
      if (trading) {
        fetchData(selectedDate.value)
      }
    }).catch(() => {})
  }, 60000)
}

function stopAutoRefresh() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

onBeforeMount(() => {
  GetConfig().then(result => {
    if (result.darkTheme) {
      darkTheme.value = true
    }
  })

  const cal = getCalendarTodayStr()
  todayYMD.value = cal

  IsTradingDay(cal)
    .then(isTd => {
      if (isTd) {
        return cal
      }
      return GetLatestTradingDay()
        .then(last => {
          const s = (last && String(last).trim()) || ''
          return s || cal
        })
        .catch(() => cal)
    })
    .catch(() => cal)
    .then(initial => {
      selectedDate.value = initial
      fetchData(initial)
      startAutoRefresh()
    })
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})

function fetchData(date, retryCount = 0) {
  if (!date) return
  loading.value = true
  const d = typeof date === 'string' ? date : formatDate(date)
  selectedDate.value = d
  const loadingMsg = message.loading('正在获取涨停梯队数据...', { duration: 0 })
  GetUplimitHot(d, 20).then(res => {
    if (res && res.code === 20000) {
      const data = res.data
      const hasData = data && data.plate?.length > 0 && data.stocks && data.stocks.trim() !== ''
      if (hasData) {
        rawData.value = data
        loadingMsg.destroy()
        if (data.ban_info && data.max_count) {
          const expanded = []
          for (let i = data.max_count; i >= 1 && expanded.length < 3; i--) {
            const info = data.ban_info[String(i)]
            if (info && info.count > 0) {
              expanded.push(String(i))
            }
          }
          expandedLadders.value = expanded
        }
      } else if (retryCount < 7) {
        const prevDate = new Date(d)
        prevDate.setDate(prevDate.getDate() - 1)
        const prevDateStr = formatDate(prevDate)
        message.info(`当前日期 ${d} 暂无数据，尝试查询前一日：${prevDateStr}`)
        loadingMsg.destroy()
        fetchData(prevDateStr, retryCount + 1)
        return
      } else {
        rawData.value = data
        loadingMsg.destroy()
        message.info('暂无历史数据')
      }
    } else {
      loadingMsg.destroy()
      message.error(res?.message || '获取数据失败')
    }
  }).catch(err => {
    loadingMsg.destroy()
    message.error('请求失败')
    console.error(err)
  }).finally(() => {
    if (retryCount === 0 || rawData.value) {
      loading.value = false
    }
  })
}

function formatDate(d) {
  if (!d) return ''
  const date = new Date(d)
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

const plateList = computed(() => {
  if (!rawData.value?.plate) return []
  return rawData.value.plate.map(p => ({
    name: p[0],
    code: p[1],
    score: p[2]
  }))
})

const plateInfo = computed(() => rawData.value?.plate_info || {})

const banInfo = computed(() => {
  if (!rawData.value?.ban_info) return []
  const result = []
  for (let i = rawData.value.max_count || 7; i >= 1; i--) {
    const info = rawData.value.ban_info[String(i)]
    if (info) {
      result.push({level: i, count: info.count})
    }
  }
  return result
})

const totalZtCount = computed(() => {
  if (!rawData.value?.stocks) return 0
  return rawData.value.stocks.split(',').filter(s => s.trim()).length
})

const maxCount = computed(() => rawData.value?.max_count || 0)

const ladderData = computed(() => {
  if (!rawData.value?.plate_stocks || !rawData.value?.stock_info) return {}
  const stocksStr = rawData.value.stocks || ''
  const allCodes = stocksStr.split(',').filter(s => s.trim())
  const stockInfo = rawData.value.stock_info || {}

  const ladder = {}
  for (let i = maxCount.value; i >= 1; i--) {
    ladder[i] = []
  }

  const seen = new Set()
  for (const code of allCodes) {
    if (seen.has(code)) continue
    seen.add(code)

    let stockData = null
    for (const plateCode of Object.keys(rawData.value.plate_stocks)) {
      const found = rawData.value.plate_stocks[plateCode].find(s => s.stock_code === code)
      if (found) {
        stockData = found
        break
      }
    }
    if (!stockData) continue

    const keepTimes = stockData.up_limit_keep_times || 0
    if (keepTimes >= 1 && ladder[keepTimes]) {
      const info = stockInfo[code]
      ladder[keepTimes].push({
        ...stockData,
        plates: info?.plates || []
      })
    }
  }

  for (const key of Object.keys(ladder)) {
    ladder[key].sort((a, b) => {
      if (a.up_limit_keep_times !== b.up_limit_keep_times) {
        return b.up_limit_keep_times - a.up_limit_keep_times
      }
      return a.up_limit_time?.localeCompare(b.up_limit_time || '') || 0
    })
  }

  return ladder
})

const stockDetailMap = computed(() => {
  const map = {}
  if (!rawData.value?.plate_stocks) return map
  for (const stocks of Object.values(rawData.value.plate_stocks)) {
    for (const s of stocks) {
      if (s.stock_code && !map[s.stock_code]) {
        map[s.stock_code] = s
      }
    }
  }
  return map
})

const explodedStocks = computed(() => {
  if (!rawData.value?.plate_stocks_zb) return []
  const result = []
  const seen = new Set()
  for (const [plateCode, stocks] of Object.entries(rawData.value.plate_stocks_zb)) {
    for (const s of stocks) {
      if (!seen.has(s.stock_code)) {
        seen.add(s.stock_code)
        const info = rawData.value?.stock_info?.[s.stock_code]
        const detail = stockDetailMap.value[s.stock_code] || {}
        result.push({
          ...s,
          fd_max: s.fd_max || detail.fd_max || '',
          fd_close: s.fd_close || detail.fd_close || '',
          amount: s.amount || detail.amount || '',
          market_c: s.market_c || detail.market_c || '',
          plates: info?.plates || []
        })
      }
    }
  }
  result.sort((a, b) => (a.up_limit_time || '').localeCompare(b.up_limit_time || ''))
  return result
})

const plateStocksFiltered = computed(() => {
  if (!rawData.value?.plate_stocks) return []
  if (!selectedPlate.value) return []
  const stocks = rawData.value.plate_stocks[selectedPlate.value] || []
  const stockInfo = rawData.value?.stock_info || {}
  return stocks.map(s => ({
    ...s,
    plates: stockInfo[s.stock_code]?.plates || []
  }))
})

const stockNameMap = computed(() => {
  const map = {}
  for (const [code, s] of Object.entries(stockDetailMap.value)) {
    map[code] = s.stock_name || ''
  }
  return map
})

const stocksHot = computed(() => {
  if (!rawData.value?.stocks_hot) return []
  const hot = rawData.value.stocks_hot
  const stockInfo = rawData.value?.stock_info || {}
  const result = Object.entries(hot).map(([code, score]) => ({
    code,
    name: stockNameMap.value[code] || '',
    score,
    plates: stockInfo[code]?.plates || []
  }))
  result.sort((a, b) => b.score - a.score)
  return result
})

const hotThreshold = computed(() => rawData.value?.stocks_hot_n || 7)

const relayPlates = computed(() => {
  if (!rawData.value?.relay?.area) return []
  return rawData.value.relay.area.map(item => {
    const info = plateInfo.value[item.p_code]
    return {
      ...item,
      name: info?.name || item.p_code
    }
  })
})

// =============== 今日复盘 - 聚合指标 ===============
// 炸板率: 炸板数 / (涨停数 + 炸板数)
const explodedRatio = computed(() => {
  const zt = totalZtCount.value
  const exp = explodedStocks.value.length
  if (zt + exp === 0) return 0
  return Math.round((exp / (zt + exp)) * 100)
})

// 市场温度判断
const marketTemperature = computed(() => {
  const max = maxCount.value
  const zt = totalZtCount.value
  const ratio = explodedRatio.value
  // 综合判断：最高板 + 涨停数 + 炸板率
  if (max >= 6 && zt >= 80 && ratio < 20) return { label: '极强', type: 'error', desc: '高度+广度+抱团三好，可激进' }
  if (max >= 5 && zt >= 60) return { label: '强', type: 'error', desc: '主线明确，关注龙头' }
  if (max >= 4 && zt >= 50) return { label: '中性偏强', type: 'warning', desc: '中度热度，跟随主线' }
  if (max >= 3 && zt >= 30) return { label: '中性', type: 'warning', desc: '机会一般，控制仓位' }
  if (ratio >= 40) return { label: '情绪降温', type: 'success', desc: '炸板率高，慎追高' }
  return { label: '偏冷', type: 'success', desc: '观望为主' }
})

// 连板分布（4板/3板/2板/1板...）
const ladderDistribution = computed(() => {
  return banInfo.value.map(b => `${b.level}板(${b.count})`).join(' · ')
})

// 主线板块 TOP 3（带涨停数 + 炸板数 + 板块内炸板率）
const mainPlates = computed(() => {
  return plateList.value.slice(0, 3).map(p => {
    const ztArr = rawData.value?.plate_stocks?.[p.code] || []
    const zbArr = rawData.value?.plate_stocks_zb?.[p.code] || []
    const total = ztArr.length + zbArr.length
    const plateExpRatio = total === 0 ? 0 : Math.round((zbArr.length / total) * 100)
    return {
      ...p,
      ztCount: ztArr.length,
      zbCount: zbArr.length,
      plateExplodedRatio: plateExpRatio,
    }
  })
})

// 龙头候选（综合 = 热度*0.6 + 连板天数*权重*0.4 + 主线匹配加分）
const leaderCandidates = computed(() => {
  if (!stocksHot.value.length) return []
  const mainPlateCodes = new Set(mainPlates.value.map(p => p.code))
  const mainPlateNames = new Set(mainPlates.value.map(p => p.name))

  // 合并 stocksHot 热度 + 连板信息
  const merged = stocksHot.value.slice(0, 30).map(h => {
    const detail = stockDetailMap.value[h.code] || {}
    const ktTimes = detail.up_limit_keep_times || 0
    const inMain = h.plates.some(pName => mainPlateNames.has(pName))
    // 综合分数：热度归一化 + 连板加成 + 主线加分
    const hotNorm = stocksHot.value[0]?.score ? (h.score / stocksHot.value[0].score) * 60 : 0
    const ladderScore = Math.min(ktTimes * 10, 40)
    const mainBonus = inMain ? 15 : 0
    const totalScore = Math.round(hotNorm + ladderScore + mainBonus)
    return {
      ...h,
      ktTimes,
      inMain,
      totalScore,
      fd_close: detail.fd_close || 0,
      market_c: detail.market_c || '',
      amount: detail.amount || '',
    }
  })
  merged.sort((a, b) => b.totalScore - a.totalScore)
  return merged.slice(0, 8)
})

// 前期连板今日炸板（重点警示）
const dangerExploded = computed(() => {
  return explodedStocks.value.filter(s => (s.up_limit_keep_times || 0) >= 2).slice(0, 5)
})

// =============== AI 深度复盘 ===============
const aiAnalyzing = ref(false)
const aiResult = ref(null) // {marketSentiment, summary, recommendations, savedCount, modelName, analyzeDate, analyzeTime}
const aiResultModal = ref(false)
const aiConfigs = ref([])
const selectedAiConfigId = ref(0)

const aiConfigOptions = computed(() => {
  return aiConfigs.value.map(c => ({
    label: c.name || c.modelName || `AI-${getCfgId(c)}`,
    value: getCfgId(c), // 兼容大写/小写 id 字段
  }))
})

function onSelectAiConfig(v) {
  console.log('[AI 配置切换] 新值:', v, '类型:', typeof v)
  selectedAiConfigId.value = v
  const cfg = aiConfigs.value.find(c => getCfgId(c) === v)
  message.success('已切换到: ' + (cfg?.name || cfg?.modelName || v))
}

// 进入复盘 tab 时加载 AI 配置列表
// 兼容字段名（Go 大写 ID / 小写 id 都接住）
function getCfgId(c) {
  return c?.ID ?? c?.id ?? 0
}

async function loadAiConfigs() {
  try {
    const res = await GetAiConfigs()
    aiConfigs.value = res || []
    console.log('[AI 配置加载] 共', aiConfigs.value.length, '条:', aiConfigs.value)
    // 优先选名字包含 claude 的
    const claude = aiConfigs.value.find(c => /claude/i.test(c.name || '') || /claude/i.test(c.modelName || ''))
    if (claude) selectedAiConfigId.value = getCfgId(claude)
    else if (aiConfigs.value.length > 0) selectedAiConfigId.value = getCfgId(aiConfigs.value[0])
    console.log('[AI 配置加载] 默认选中 ID:', selectedAiConfigId.value)
  } catch (e) {
    console.warn('加载 AI 配置失败', e)
  }
}

// =============== 历史复盘列表 ===============
const historySummaries = ref([])

async function loadHistorySummaries() {
  try {
    const list = await GetUplimitAISummaries(20)
    historySummaries.value = list || []
  } catch (e) {
    console.warn('加载历史复盘失败', e)
  }
}

async function viewHistorySummary(id) {
  try {
    const result = await GetUplimitAISummaryDetail(id)
    aiResult.value = result
    aiResultModal.value = true
  } catch (e) {
    message.error('加载复盘详情失败: ' + (e?.message || e))
  }
}

async function deleteHistorySummary(id) {
  try {
    await DeleteUplimitAISummary(id)
    message.success('已删除')
    loadHistorySummaries()
  } catch (e) {
    message.error('删除失败')
  }
}

async function runAIAnalysis() {
  if (aiAnalyzing.value) return
  if (!selectedDate.value) {
    message.warning('请先选择日期')
    return
  }
  if (!aiConfigs.value.length) {
    message.error('未配置任何 AI，请先在「设置 → AI 设置」中配置 Claude/LiteLLM')
    return
  }
  aiAnalyzing.value = true
  const loadingMsg = message.loading('🤖 AI 正在深度分析涨停梯队...（约 30-60 秒）', { duration: 0 })
  try {
    const result = await AnalyzeUplimitWithAI(selectedDate.value, selectedAiConfigId.value || 0)
    loadingMsg.destroy()
    aiResult.value = result
    aiResultModal.value = true
    message.success(`分析完成，已生成 ${result.savedCount} 条推荐入库`)
    // 刷新历史列表
    loadHistorySummaries()
  } catch (e) {
    loadingMsg.destroy()
    message.error('AI 分析失败: ' + (e?.message || e))
  } finally {
    aiAnalyzing.value = false
  }
}

// 进入页面时加载 AI 配置 + 历史复盘
onBeforeMount(() => {
  loadAiConfigs()
  loadHistorySummaries()
})

// 操作建议（基于客观数据生成）
const actionAdvice = computed(() => {
  const temp = marketTemperature.value.label
  const advices = []
  if (temp === '极强' || temp === '强') {
    advices.push({ icon: '✅', text: `市场温度【${temp}】，可正常加仓` })
    advices.push({ icon: '🎯', text: '优先选择主线龙头（高热度+多连板）' })
    advices.push({ icon: '⚠️', text: '炸板率 ' + explodedRatio.value + '%，留意分歧日' })
  } else if (temp === '中性偏强' || temp === '中性') {
    advices.push({ icon: '⚖️', text: `市场温度【${temp}】，半仓为宜` })
    advices.push({ icon: '🎯', text: '只追主线，避免跟风' })
  } else {
    advices.push({ icon: '🛑', text: `市场温度【${temp}】，控制仓位` })
    advices.push({ icon: '👀', text: '观望为主，等待回暖' })
  }
  if (dangerExploded.value.length > 0) {
    advices.push({ icon: '🚨', text: `${dangerExploded.value.length} 只前期连板今日炸板，警惕跟风` })
  }
  if (relayPlates.value.length > 0) {
    advices.push({ icon: '🔥', text: '接力主线: ' + relayPlates.value.slice(0, 2).map(p => p.name).join('、') })
  }
  return advices
})

function getTypeColor(type) {
  if (!type) return 'default'
  if (type === '一') return '#e03030'
  if (type === 'T') return '#f0a020'
  if (type === '自') return '#2080f0'
  if (type.startsWith('烂')) return '#f0a020'
  if (type === '炸') return '#999'
  return 'default'
}

function getTypeLabel(type) {
  if (!type) return ''
  if (type === '一') return '一字'
  if (type === 'T') return 'T字'
  if (type === '自') return '自然'
  if (type.startsWith('烂')) return '烂' + type.slice(1) + '板'
  if (type === '炸') return '炸板'
  return type
}

function getFdCloseColor(val) {
  if (val >= 3) return '#18a058'
  if (val >= 1) return '#2080f0'
  if (val > 0) return '#f0a020'
  return '#e03030'
}

function getScoreBarWidth(score, maxScore) {
  if (!maxScore) return 0
  return Math.min(100, (score / maxScore) * 100)
}

const plateTableMaxHeight = computed(() => Math.max(300, window.innerHeight * 0.7))

const plateStockColumns = [
  {title: '代码', key: 'stock_code', width: 90, render: (row) => h(NText, {depth: 3, style: 'font-size:12px'}, () => row.stock_code)},
  {title: '名称', key: 'stock_name', width: 80, render: (row) => h(NText, {strong: true, style: 'cursor:pointer;color:#2080f0;text-decoration:underline;', onClick: () => showKline(row.stock_code, row.stock_name)}, () => row.stock_name)},
  {title: '类型', key: 'up_limit_type', width: 70, render: (row) => h(NTag, {color: {color: getTypeColor(row.up_limit_type), textColor: '#fff'}, size: 'tiny', round: true}, () => getTypeLabel(row.up_limit_type))},
  {title: '描述', key: 'up_limit_desc', width: 70, render: (row) => row.up_limit_desc ? h(NTag, {size: 'tiny', type: row.up_limit_keep_times >= 3 ? 'error' : 'default', round: true}, () => row.up_limit_desc) : ''},
  {title: '时间', key: 'up_limit_time', width: 70, render: (row) => h(NText, {depth: 3, style: 'font-size:12px'}, () => row.up_limit_time || '')},
  {title: '封单比', key: 'fd_max', width: 65, render: (row) => h(NText, {style: 'font-size:12px'}, () => row.fd_max + '%')},
  {title: '收盘封单', key: 'fd_close', width: 75, render: (row) => h(NText, {style: 'color:' + getFdCloseColor(row.fd_close) + ';font-size:12px;font-weight:bold'}, () => row.fd_close + '%')},
  {title: '成交额', key: 'amount', width: 70, render: (row) => h(NText, {style: 'font-size:12px'}, () => row.amount + '亿')},
  {title: '市值', key: 'market_c', width: 70, render: (row) => h(NText, {style: 'font-size:12px'}, () => row.market_c + '亿')},
]

function selectPlate(code) {
  selectedPlate.value = code
  showPlateModal.value = true
}

function toEastMoneyCode(code) {
  if (!code) return ''
  const c = String(code).trim()
  if (/\.(SH|SZ|BJ|HK|SS)$/i.test(c)) return c.toUpperCase()
  const lower = c.toLowerCase()
  if (lower.startsWith('sh')) return lower.slice(2) + '.SH'
  if (lower.startsWith('sz')) return lower.slice(2) + '.SZ'
  if (lower.startsWith('bj')) return lower.slice(2) + '.BJ'
  if (lower.startsWith('hk')) return lower.slice(2).toUpperCase() + '.HK'
  if (/^\d+$/.test(c)) {
    const d = c[0]
    if (d === '6') return c + '.SH'
    if (d === '0' || d === '3') return c + '.SZ'
    if (d === '8' || d === '9') return c + '.BJ'
    return c + '.SZ'
  }
  return c.toUpperCase()
}

function showKline(code, name) {
  const em = toEastMoneyCode(code)
  if (!em) {
    message.warning('当前代码暂不支持K线图')
    return
  }
  klineCode.value = em
  klineName.value = name || ''
  showKlineModal.value = true
}
</script>

<template>
  <div style="padding: 0 4px;">
    <n-spin :show="loading">
      <n-space vertical :size="16">
        <n-card size="small" :bordered="true">
          <n-space justify="space-between" align="center">
            <n-space align="center" :size="16">
              <n-date-picker
                v-model:formatted-value="selectedDate"
                value-format="yyyy-MM-dd"
                type="date"
                size="small"
                style="width: 150px"
                :on-update:formatted-value="(v) => { if(v) fetchData(v) }"
              />
              <n-tag type="success" size="small" round>涨停 {{ totalZtCount }} 只</n-tag>
              <n-tag type="warning" size="small" round>最高 {{ maxCount }} 连板</n-tag>
              <n-tag v-if="rawData?.today" type="info" size="small" round>实时数据</n-tag>
            </n-space>
            <n-space>
              <n-button :type="activeView==='ladder'?'primary':'default'" size="small" @click="activeView='ladder'">涨停高度</n-button>
              <n-button :type="activeView==='plate'?'primary':'default'" size="small" @click="activeView='plate'">板块热度</n-button>
              <n-button :type="activeView==='hot'?'primary':'default'" size="small" @click="activeView='hot'">个股热度</n-button>
              <n-button :type="activeView==='exploded'?'primary':'default'" size="small" @click="activeView='exploded'">炸板股</n-button>
              <n-button :type="activeView==='summary'?'primary':'default'" size="small" @click="activeView='summary'" strong>📊 今日复盘</n-button>
            </n-space>
          </n-space>
        </n-card>

        <template v-if="activeView==='ladder'">
          <n-collapse v-model:expanded-names="expandedLadders" :accordion="false">
            <n-collapse-item v-for="level in banInfo" :key="level.level" :name="String(level.level)">
              <template #header>
                <n-space align="center" :size="8">
                  <n-tag :type="level.level>=5?'error':level.level>=3?'warning':'info'" round size="small" style="font-weight:bold;">
                    {{ level.level }} 板
                  </n-tag>
                  <n-text depth="3" style="font-size:12px;">{{ level.count }}只</n-text>
                </n-space>
              </template>
              <n-space vertical :size="8">
                <n-card v-for="stock in ladderData[level.level]" :key="stock.stock_code"
                  size="small" :bordered="true" embedded
                  :style="'border-left: 3px solid ' + getTypeColor(stock.up_limit_type)">
                  <n-space justify="space-between" align="center" wrap :size="8">
                    <n-space align="center" :size="8" wrap>
                      <n-text strong style="font-size:15px;cursor:pointer;color:#2080f0;text-decoration:underline;" @click="showKline(stock.stock_code, stock.stock_name)">{{ stock.stock_name }}</n-text>
                      <n-text depth="3" style="font-size:12px;">{{ stock.stock_code }}</n-text>
                      <n-tag :color="{color: getTypeColor(stock.up_limit_type), textColor:'#fff'}" size="tiny" round>
                        {{ getTypeLabel(stock.up_limit_type) }}
                      </n-tag>
                      <n-tag v-if="stock.up_limit_desc" type="primary" size="tiny" round>{{ stock.up_limit_desc }}</n-tag>
                      <n-text depth="3" style="font-size:12px;">{{ stock.up_limit_time }}</n-text>
                    </n-space>
                    <n-space align="center" :size="12" wrap>
                      <n-space align="center" :size="4">
                        <n-text depth="3" style="font-size:11px;">封单</n-text>
                        <n-text :style="'color:'+getFdCloseColor(stock.fd_close)+';font-weight:bold;font-size:13px;'">{{ stock.fd_close }}%</n-text>
                      </n-space>
                      <n-space align="center" :size="4">
                        <n-text depth="3" style="font-size:11px;">成交</n-text>
                        <n-text style="font-size:13px;">{{ stock.amount }}亿</n-text>
                      </n-space>
                      <n-space align="center" :size="4">
                        <n-text depth="3" style="font-size:11px;">市值</n-text>
                        <n-text style="font-size:13px;">{{ stock.market_c }}亿</n-text>
                      </n-space>
                    </n-space>
                  </n-space>
                  <n-space :size="4" style="margin-top:4px;" v-if="stock.plates && stock.plates.length">
                    <n-tag v-for="p in stock.plates.slice(0,6)" :key="p" size="tiny" :bordered="false" type="info">{{ p }}</n-tag>
                  </n-space>
                </n-card>
              </n-space>
            </n-collapse-item>
          </n-collapse>
          <n-card v-if="banInfo.length === 0 && !loading" size="small">
            <n-empty description="暂无连板数据"/>
          </n-card>
        </template>

        <template v-if="activeView==='plate'">
          <n-space vertical :size="12">
            <n-card size="small" :bordered="true" v-if="relayPlates.length">
              <template #header>
                <n-text style="font-weight:bold;">🔥 接力主线</n-text>
              </template>
              <n-space :size="8" wrap>
                <n-tag v-for="rp in relayPlates" :key="rp.p_code" round
                  :type="rp.p_score > 5000 ? 'error' : rp.p_score > 2000 ? 'warning' : 'info'"
                  style="cursor:pointer;font-size:13px;"
                  @click="selectPlate(rp.p_code)">
                  {{ rp.name }}
                  <template #avatar>
                    <n-text style="font-size:11px;opacity:0.7;margin-right:2px;">{{ rp.count }}只</n-text>
                  </template>
                </n-tag>
              </n-space>
            </n-card>

            <n-grid :cols="2" :x-gap="12" :y-gap="12" responsive="screen">
              <n-gi v-for="plate in plateList" :key="plate.code">
                <n-card size="small" :bordered="true"
                  style="cursor:pointer;" @click="selectPlate(plate.code)">
                  <n-space justify="space-between" align="center">
                    <n-space align="center" :size="8">
                      <n-text strong style="font-size:14px;">{{ plate.name }}</n-text>
                      <n-tag size="tiny" round :type="plate.score>5000?'error':plate.score>2000?'warning':'info'">
                        热度 {{ plate.score }}
                      </n-tag>
                    </n-space>
                    <n-text depth="3" style="font-size:12px;">
                      涨停{{ rawData?.plate_stocks?.[plate.code]?.length || 0 }}只
                      炸板{{ rawData?.plate_stocks_zb?.[plate.code]?.length || 0 }}只
                    </n-text>
                  </n-space>
                  <n-progress
                    :percentage="getScoreBarWidth(plate.score, plateList[0]?.score || 1)"
                    :show-indicator="false"
                    :color="plate.score>5000?'#e03030':plate.score>2000?'#f0a020':'#2080f0'"
                    :height="4"
                    style="margin-top:6px;"
                  />
                </n-card>
              </n-gi>
            </n-grid>
          </n-space>
        </template>

        <template v-if="activeView==='hot'">
          <n-card size="small" :bordered="true">
            <template #header>
              <n-text style="font-weight:bold;">个股热度排行</n-text>
              <n-text depth="3" style="font-size:12px;margin-left:8px;">热度≥{{ hotThreshold }}为超级热门</n-text>
            </template>
            <n-table :single-line="false" striped size="small" style="font-size:13px;">
              <n-thead>
                <n-tr>
                  <n-th width="50px">排名</n-th>
                  <n-th>代码</n-th>
                  <n-th>名称</n-th>
                  <n-th>热度</n-th>
                  <n-th>概念板块</n-th>
                </n-tr>
              </n-thead>
              <n-tbody>
                <n-tr v-for="(item, idx) in stocksHot" :key="item.code">
                  <n-td>
                    <n-tag v-if="idx<3" type="error" size="tiny" round>{{ idx+1 }}</n-tag>
                    <n-text v-else depth="3">{{ idx+1 }}</n-text>
                  </n-td>
                  <n-td><n-text strong>{{ item.code }}</n-text></n-td>
                  <n-td><n-text strong style="cursor:pointer;color:#2080f0;text-decoration:underline;" @click="showKline(item.code, item.name)">{{ item.name }}</n-text></n-td>
                  <n-td>
                    <n-space align="center" :size="4">
                      <n-progress
                        type="line"
                        :percentage="Math.min(100, (item.score / (stocksHot[0]?.score || 1)) * 100)"
                        :show-indicator="false"
                        :color="item.score >= hotThreshold ? '#e03030' : '#2080f0'"
                        :height="8"
                        style="width:80px;"
                      />
                      <n-text :type="item.score >= hotThreshold ? 'error' : 'default'" strong>{{ item.score }}</n-text>
                    </n-space>
                  </n-td>
                  <n-td>
                    <n-space :size="4" wrap>
                      <n-tag v-for="p in item.plates.slice(0,6)" :key="p" size="tiny" :bordered="false" type="info">{{ p }}</n-tag>
                    </n-space>
                  </n-td>
                </n-tr>
              </n-tbody>
            </n-table>
          </n-card>
        </template>

        <template v-if="activeView==='exploded'">
          <n-card size="small" :bordered="true">
            <template #header>
              <n-space align="center" :size="8">
                <n-text style="font-weight:bold;">炸板股</n-text>
                <n-tag type="warning" size="small" round>{{ explodedStocks.length }}只</n-tag>
              </n-space>
            </template>
            <n-table v-if="explodedStocks.length" :single-line="false" striped size="small" style="font-size:13px;">
              <n-thead>
                <n-tr>
                  <n-th>代码</n-th>
                  <n-th>名称</n-th>
                  <n-th>时间</n-th>
                  <n-th>最高封单</n-th>
                  <n-th>成交额</n-th>
                  <n-th>市值</n-th>
                  <n-th>概念板块</n-th>
                </n-tr>
              </n-thead>
              <n-tbody>
                <n-tr v-for="s in explodedStocks" :key="s.stock_code + s.plate_code">
                  <n-td><n-text depth="3" style="font-size:12px;">{{ s.stock_code }}</n-text></n-td>
                  <n-td><n-text type="error" style="cursor:pointer;text-decoration:underline;" @click="showKline(s.stock_code, s.stock_name)">{{ s.stock_name }}</n-text></n-td>
                  <n-td><n-text depth="3" style="font-size:12px;">{{ s.up_limit_time }}</n-text></n-td>
                  <n-td><n-text style="font-size:12px;">{{ s.fd_max }}%</n-text></n-td>
                  <n-td><n-text style="font-size:12px;">{{ s.amount }}亿</n-text></n-td>
                  <n-td><n-text style="font-size:12px;">{{ s.market_c }}亿</n-text></n-td>
                  <n-td>
                    <n-space :size="4" wrap>
                      <n-tag v-for="p in s.plates.slice(0,5)" :key="p" size="tiny" :bordered="false" type="info">{{ p }}</n-tag>
                    </n-space>
                  </n-td>
                </n-tr>
              </n-tbody>
            </n-table>
            <n-empty v-else description="暂无炸板数据"/>
          </n-card>
        </template>

        <template v-if="activeView==='summary'">
          <n-space vertical :size="12">
            <!-- 第一行：市场温度 -->
            <n-card size="small" :bordered="true">
              <template #header>
                <n-space align="center" :size="8">
                  <n-text style="font-weight:bold;">🌡️ 市场温度</n-text>
                  <n-tag :type="marketTemperature.type" round size="small" style="font-weight:bold;">
                    {{ marketTemperature.label }}
                  </n-tag>
                  <n-text depth="3" style="font-size:12px;">{{ marketTemperature.desc }}</n-text>
                </n-space>
              </template>
              <n-grid :cols="4" :x-gap="12">
                <n-gi>
                  <n-statistic label="总涨停" :value="totalZtCount">
                    <template #suffix>只</template>
                  </n-statistic>
                </n-gi>
                <n-gi>
                  <n-statistic label="最高板" :value="maxCount">
                    <template #suffix>板</template>
                  </n-statistic>
                </n-gi>
                <n-gi>
                  <n-statistic label="炸板数" :value="explodedStocks.length">
                    <template #suffix>只</template>
                  </n-statistic>
                </n-gi>
                <n-gi>
                  <n-statistic label="炸板率" :value="explodedRatio">
                    <template #suffix>%</template>
                  </n-statistic>
                </n-gi>
              </n-grid>
              <n-divider style="margin: 8px 0" />
              <n-text style="font-size:13px;" depth="2">连板分布: </n-text>
              <n-text style="font-size:13px;font-weight:bold;">{{ ladderDistribution || '暂无数据' }}</n-text>
            </n-card>

            <!-- 第二行：主线方向 -->
            <n-card size="small" :bordered="true">
              <template #header>
                <n-text style="font-weight:bold;">🎯 主线方向 TOP 3</n-text>
              </template>
              <n-grid :cols="3" :x-gap="12">
                <n-gi v-for="(p, idx) in mainPlates" :key="p.code">
                  <n-card size="small" :bordered="true" embedded :style="'border-left: 4px solid ' + (idx===0?'#e03030':idx===1?'#f0a020':'#2080f0')">
                    <n-space justify="space-between" align="center">
                      <n-text strong style="font-size:14px;">
                        {{ idx===0?'🥇':idx===1?'🥈':'🥉' }} {{ p.name }}
                      </n-text>
                      <n-tag size="tiny" round :type="idx===0?'error':'warning'">热度 {{ p.score }}</n-tag>
                    </n-space>
                    <n-space :size="12" style="margin-top: 6px; font-size: 12px;">
                      <n-text>涨停 <n-text type="error" strong>{{ p.ztCount }}</n-text> 只</n-text>
                      <n-text>炸板 <n-text type="warning">{{ p.zbCount }}</n-text> 只</n-text>
                      <n-text>炸板率 <n-text :type="p.plateExplodedRatio>30?'error':p.plateExplodedRatio>15?'warning':'success'">{{ p.plateExplodedRatio }}%</n-text></n-text>
                    </n-space>
                  </n-card>
                </n-gi>
              </n-grid>
              <n-divider style="margin: 10px 0" v-if="relayPlates.length" />
              <n-space v-if="relayPlates.length" align="center" :size="6" wrap>
                <n-text style="font-weight:bold;font-size:13px;">🔥 接力主线:</n-text>
                <n-tag v-for="rp in relayPlates.slice(0, 5)" :key="rp.p_code" size="small" type="error" round>{{ rp.name }}</n-tag>
              </n-space>
            </n-card>

            <!-- 第三行：龙头候选 -->
            <n-card size="small" :bordered="true">
              <template #header>
                <n-space align="center" :size="8">
                  <n-text style="font-weight:bold;">⭐ 龙头候选 TOP 8</n-text>
                  <n-text depth="3" style="font-size:12px;">综合 = 热度 + 连板 + 主线匹配</n-text>
                </n-space>
              </template>
              <n-table :single-line="false" striped size="small" style="font-size:13px;">
                <n-thead>
                  <n-tr>
                    <n-th width="40px">#</n-th>
                    <n-th>名称</n-th>
                    <n-th>代码</n-th>
                    <n-th>连板</n-th>
                    <n-th>热度</n-th>
                    <n-th>主线</n-th>
                    <n-th>封单</n-th>
                    <n-th>市值</n-th>
                    <n-th>综合分</n-th>
                    <n-th>概念</n-th>
                  </n-tr>
                </n-thead>
                <n-tbody>
                  <n-tr v-for="(s, idx) in leaderCandidates" :key="s.code">
                    <n-td>
                      <n-tag v-if="idx<3" type="error" size="tiny" round>{{ idx+1 }}</n-tag>
                      <n-text v-else depth="3">{{ idx+1 }}</n-text>
                    </n-td>
                    <n-td>
                      <n-text strong style="cursor:pointer;color:#2080f0;text-decoration:underline;" @click="showKline(s.code, s.name)">
                        {{ s.name }}
                      </n-text>
                    </n-td>
                    <n-td><n-text depth="3" style="font-size:11px;">{{ s.code }}</n-text></n-td>
                    <n-td>
                      <n-tag v-if="s.ktTimes >= 1" :type="s.ktTimes>=4?'error':s.ktTimes>=2?'warning':'info'" size="tiny" round>
                        {{ s.ktTimes }}板
                      </n-tag>
                    </n-td>
                    <n-td><n-text :type="s.score >= hotThreshold ? 'error' : 'default'" strong>{{ s.score }}</n-text></n-td>
                    <n-td>
                      <n-tag v-if="s.inMain" type="error" size="tiny" round>主线✓</n-tag>
                      <n-text v-else depth="3" style="font-size:11px;">非主线</n-text>
                    </n-td>
                    <n-td>
                      <n-text :style="'color:'+getFdCloseColor(s.fd_close)+';font-weight:bold;font-size:12px;'">{{ s.fd_close }}%</n-text>
                    </n-td>
                    <n-td><n-text style="font-size:12px;">{{ s.market_c }}亿</n-text></n-td>
                    <n-td>
                      <n-progress
                        type="line"
                        :percentage="Math.min(100, s.totalScore)"
                        :show-indicator="true"
                        :color="s.totalScore>=80?'#e03030':s.totalScore>=60?'#f0a020':'#2080f0'"
                        :height="14"
                        :indicator-text-color="darkTheme?'#fff':'#000'"
                      />
                    </n-td>
                    <n-td>
                      <n-space :size="2" wrap>
                        <n-tag v-for="p in s.plates.slice(0,4)" :key="p" size="tiny" :bordered="false" type="info">{{ p }}</n-tag>
                      </n-space>
                    </n-td>
                  </n-tr>
                </n-tbody>
              </n-table>
            </n-card>

            <!-- 第四行：炸板预警 -->
            <n-card v-if="dangerExploded.length > 0" size="small" :bordered="true">
              <template #header>
                <n-space align="center" :size="8">
                  <n-text style="font-weight:bold;color:#e03030;">🚨 前期连板今日炸板</n-text>
                  <n-text depth="3" style="font-size:12px;">这些股反映高度被压制，跟风需谨慎</n-text>
                </n-space>
              </template>
              <n-space :size="8" wrap>
                <n-card v-for="s in dangerExploded" :key="s.stock_code" size="small" :bordered="true" embedded style="border-left:3px solid #e03030">
                  <n-space align="center" :size="8">
                    <n-text strong style="cursor:pointer;color:#2080f0;text-decoration:underline;" @click="showKline(s.stock_code, s.stock_name)">{{ s.stock_name }}</n-text>
                    <n-tag type="error" size="tiny" round>{{ s.up_limit_keep_times }} 连板 炸</n-tag>
                  </n-space>
                </n-card>
              </n-space>
            </n-card>

            <!-- 第五行：操作建议 + AI 复盘按钮 -->
            <n-card size="small" :bordered="true">
              <template #header>
                <n-space justify="space-between" align="center" style="width:100%">
                  <n-text style="font-weight:bold;">📋 操作建议（基于客观数据）</n-text>
                  <n-space align="center" :size="6">
                    <n-select
                      v-if="aiConfigs.length > 1"
                      :value="selectedAiConfigId"
                      :options="aiConfigOptions"
                      size="small"
                      style="width:200px"
                      placeholder="选 AI 模型"
                      @update:value="onSelectAiConfig"
                    />
                    <n-button type="primary" size="small" :loading="aiAnalyzing" @click="runAIAnalysis">
                      🤖 AI 一键写分析
                    </n-button>
                  </n-space>
                </n-space>
              </template>
              <n-space vertical :size="6">
                <n-text v-for="(a, idx) in actionAdvice" :key="idx" style="font-size:13px;">
                  {{ a.icon }} {{ a.text }}
                </n-text>
              </n-space>
              <n-divider style="margin: 10px 0" />
              <n-text depth="3" style="font-size:11px;">
                ⚠️ 以上建议基于客观数据规则推演。点「AI 一键写分析」让 AI 综合分析输出 200 字复盘 + 3-5 只推荐自动入库（可在「研究中心 → AI 推荐股票」追踪）。
              </n-text>
            </n-card>

            <!-- 第六行：历史 AI 复盘记录（持久化保存，可重新查看） -->
            <n-card size="small" :bordered="true" v-if="historySummaries.length > 0">
              <template #header>
                <n-space align="center" :size="8">
                  <n-text style="font-weight:bold;">📜 历史 AI 复盘</n-text>
                  <n-tag type="info" size="small" round>{{ historySummaries.length }} 条</n-tag>
                  <n-text depth="3" style="font-size:11px;">点条目可重新打开弹窗查看</n-text>
                </n-space>
              </template>
              <n-table :single-line="false" striped size="small" style="font-size:13px;">
                <n-thead>
                  <n-tr>
                    <n-th>复盘日期</n-th>
                    <n-th>生成时间</n-th>
                    <n-th>市场情绪</n-th>
                    <n-th>模型</n-th>
                    <n-th>推荐数</n-th>
                    <n-th>摘要预览</n-th>
                    <n-th width="120px">操作</n-th>
                  </n-tr>
                </n-thead>
                <n-tbody>
                  <n-tr v-for="h in historySummaries" :key="h.id">
                    <n-td><n-tag size="small" type="default">{{ h.analyzeDate }}</n-tag></n-td>
                    <n-td><n-text depth="3" style="font-size:11px;">{{ h.analyzeTime }}</n-text></n-td>
                    <n-td>
                      <n-tag size="small" round :type="h.marketSentiment === '极强' || h.marketSentiment === '强' ? 'error' : h.marketSentiment === '偏冷' ? 'success' : 'warning'">
                        {{ h.marketSentiment }}
                      </n-tag>
                    </n-td>
                    <n-td><n-text style="font-size:11px;" depth="3">{{ h.modelName }}</n-text></n-td>
                    <n-td><n-tag size="tiny" type="info">{{ h.savedCount }} 只</n-tag></n-td>
                    <n-td><n-ellipsis :line-clamp="2" style="max-width: 360px;">{{ h.summary }}</n-ellipsis></n-td>
                    <n-td>
                      <n-space :size="4">
                        <n-button size="tiny" type="primary" @click="viewHistorySummary(h.id)">查看</n-button>
                        <n-popconfirm @positive-click="deleteHistorySummary(h.id)">
                          <template #trigger>
                            <n-button size="tiny" type="error" ghost>删除</n-button>
                          </template>
                          确认删除这条复盘？
                        </n-popconfirm>
                      </n-space>
                    </n-td>
                  </n-tr>
                </n-tbody>
              </n-table>
            </n-card>
          </n-space>
        </template>

      </n-space>
    </n-spin>

    <n-modal v-model:show="showPlateModal" preset="card"
      :title="plateInfo[selectedPlate]?.name + ' - 涨停股详情' || '涨停股详情'"
      style="width: 900px; max-width: 95vw;"
      :bordered="true" :segmented="{content:true}">
      <n-data-table :columns="plateStockColumns" :data="plateStocksFiltered"
        :max-height="plateTableMaxHeight" virtual-scroll
        size="small" :bordered="false" striped />
    </n-modal>

    <n-modal v-model:show="aiResultModal" preset="card"
      :title="'🤖 AI 涨停梯队复盘 - ' + (aiResult?.analyzeDate || '')"
      style="width: 900px; max-width: 95vw;"
      :bordered="true" :segmented="{content:true}">
      <n-space vertical :size="12" v-if="aiResult">
        <n-card size="small" :bordered="true" embedded>
          <n-space justify="space-between" align="center" style="margin-bottom:8px">
            <n-space align="center" :size="8">
              <n-text style="font-weight:bold;font-size:14px;">📊 市场情绪：</n-text>
              <n-tag :type="aiResult.marketSentiment === '极强' || aiResult.marketSentiment === '强' ? 'error' : aiResult.marketSentiment === '偏冷' ? 'success' : 'warning'" round size="medium" style="font-weight:bold">
                {{ aiResult.marketSentiment }}
              </n-tag>
            </n-space>
            <n-text depth="3" style="font-size:11px;">{{ aiResult.modelName }} · {{ aiResult.analyzeTime }}</n-text>
          </n-space>
          <n-text style="line-height:1.7;font-size:14px;white-space:pre-wrap">{{ aiResult.summary }}</n-text>
        </n-card>

        <n-card size="small" :bordered="true" embedded>
          <template #header>
            <n-space align="center" :size="8">
              <n-text style="font-weight:bold;">🎯 推荐股票</n-text>
              <n-tag type="success" size="small" round>{{ aiResult.recommendations?.length || 0 }} 只</n-tag>
              <n-tag type="info" size="small" round>已保存 {{ aiResult.savedCount }} 条到 AI 推荐</n-tag>
            </n-space>
          </template>
          <n-space vertical :size="8">
            <n-card v-for="(r, idx) in aiResult.recommendations" :key="r.stockCode + idx"
              size="small" :bordered="true" embedded
              :style="'border-left: 4px solid ' + (r.rating === '买入' ? '#e03030' : r.rating === '增持' ? '#f0a020' : '#2080f0')">
              <n-space justify="space-between" align="center" wrap>
                <n-space align="center" :size="8">
                  <n-text strong style="font-size:15px;cursor:pointer;color:#2080f0;text-decoration:underline" @click="showKline(r.stockCode, r.stockName)">
                    {{ r.stockName }}
                  </n-text>
                  <n-text depth="3" style="font-size:12px">{{ r.stockCode }}</n-text>
                  <n-tag :type="r.rating === '买入' ? 'error' : r.rating === '增持' ? 'warning' : 'info'" size="small" round>{{ r.rating }}</n-tag>
                </n-space>
              </n-space>
              <n-text style="display:block;margin-top:6px;font-size:13px;line-height:1.6">{{ r.reason }}</n-text>
              <n-space :size="12" style="margin-top:8px;font-size:12px" wrap>
                <n-text>💰 买入: <n-text type="error" strong>{{ r.buyPriceMin }} ~ {{ r.buyPriceMax }}</n-text></n-text>
                <n-text>📈 止盈: <n-text type="success" strong>{{ r.stopProfitMin }} ~ {{ r.stopProfitMax }}</n-text></n-text>
                <n-text>📉 止损: <n-text type="warning" strong>{{ r.stopLoss }}</n-text></n-text>
              </n-space>
              <n-text v-if="r.risk" depth="3" style="display:block;margin-top:6px;font-size:11px">⚠️ 风险: {{ r.risk }}</n-text>
            </n-card>
          </n-space>
        </n-card>

        <n-text depth="3" style="font-size:11px">
          💡 这些推荐已自动写入数据库，可在「研究中心 → AI 推荐股票」查看完整列表并跟踪当前价。
          AI 输出仅供参考，请结合自身判断，投资有风险。
        </n-text>
      </n-space>
    </n-modal>

    <n-modal v-model:show="showKlineModal" preset="card"
      :title="(klineName || '') + ' — 多周期K线'"
      style="width: 95vw; max-width: 1200px;"
      :bordered="true">
      <stock-lightweight-kline-chart
        v-if="showKlineModal"
        :dark-theme="darkTheme"
        :key="'kline-' + klineCode"
        :code="klineCode"
        :stock-name="klineName"
        :chart-height="500"
      />
    </n-modal>
  </div>
</template>

<style scoped>
:deep(.n-card) {
  transition: all 0.2s ease;
}
:deep(.n-tag) {
  font-size: 12px;
}
</style>
