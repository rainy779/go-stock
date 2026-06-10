<script setup>
import {computed, h, onBeforeMount, onBeforeUnmount, onMounted,onUnmounted, ref,reactive} from 'vue'
import {
  GetAiRecommendStocksList,
  GetConfig,
  GetSponsorInfo,
  DeleteAiRecommendStocks,
  UpdateAiRecommendStocksAlert,
  ShareAnalysis
} from "../../wailsjs/go/main/App";
import {NAvatar, NButton, NEllipsis, NSwitch, NTag, NText, useMessage, useNotification} from "naive-ui";
import StockLightweightKlineChart from "./StockLightweightKlineChart.vue";
import sparkLine from "./stockSparkLine.vue"
import {format} from "date-fns";
import * as XLSX from 'xlsx';
import {saveAs} from 'file-saver';

const notify = useNotification()
const vipLevel=ref("");
const vipStartTime=ref("");
const vipEndTime=ref("");
const expired=ref(false)
const isValidVip=ref(false) // 是否是会员

onBeforeMount(()=> {
  GetConfig().then(result => {
    if (result.darkTheme) {
      editorDataRef.darkTheme = true
    }
  })

  GetSponsorInfo().then((res) => {
   // console.log(res)
    vipLevel.value = res.vipLevel;
    vipStartTime.value = res.vipStartTime;
    vipEndTime.value = res.vipEndTime;
    //判断时间是否到期
    if (res.vipLevel) {
      if (res.vipEndTime < format(new Date(), 'yyyy-MM-dd HH:mm:ss')) {
        //notify.warning({content: 'VIP已到期'})
        expired.value = true;
      }
    }else{
      //notify.success({content: '未开通VIP'})
    }
    isValidVip.value = !(vipLevel.value === "" || Number(vipLevel.value) <= 0);
  })
})
onMounted(() => {
  query({
    page: 1,
    pageSize: paginationReactive.pageSize,
    order: "desc",
    keyword: paginationReactive.keyword,
    startDate: paginationReactive.range[0],
    endDate: paginationReactive.range[1]
  }).then((data) => {
    console.log( data)
    dataRef.value = data.data
    paginationReactive.page = 1
    paginationReactive.pageCount = data.pageCount
    paginationReactive.itemCount = data.total
    loadingRef.value = false
  })
})
const message = useMessage()
const mdPreviewRef = ref(null)
const mdEditorRef = ref(null)
const editorDataRef = reactive({
  show: false,
  loading: false,
  darkTheme: false,
  chatId: "",
  modelName: "",
  CreatedAt: "",
  stockName: "",
  stockCode: "",
  question: "",
  content: "",
})
const dataRef = ref([])
const loadingRef = ref(true)

// StockClosePrice          string     `json:"StockClosePrice" md:"推荐时股票收盘价格"`
// StockPrePrice            string     `json:"stockPrePricePrice" md:"前一交易日股票价格"`
// RecommendReason          string     `json:"recommendReason" md:"推荐理由/驱动因素/逻辑"`
// RecommendBuyPrice        string     `json:"recommendBuyPrice" md:"ai建议买入价"`
// RecommendStopProfitPrice string     `json:"recommendStopProfitPrice" md:"ai建议止盈价"`
// RecommendStopLossPrice   string     `json:"recommendStopLossPrice" md:"ai建议止损价"`
// RiskRemarks              string     `json:"riskRemarks" md:"风险提示"`
// Remarks                  string     `json:"remarks" md:"备注"`
const columnsRef = ref([
  {
    title: '推荐模型',
    key: 'modelName',
    render(row, index) {
      return h(NText, { type: "info" }, { default: () => row.modelName })
    }
  },
  {
    title: '评级',
    key: 'rating',
    render(row, index) {
      return h(NText, { type: "info" }, { default: () => row.rating || '-' })
    }
  },
  {
    title: '推荐时间',
    key: 'dataTime',
    render(row, index) {
      //2026-01-14T22:13:27.2693252+08:00 格式化为常用时间格式
      return row.CreatedAt.substring(0, 19).replace('T', ' ')
    }
  },
  {
    title: '板块概念',
    key: 'bkName'
  },
  {
    title: '股票名称',
    key: 'stockName',
    render(row, index) {
      return h(NText, { type: "info" }, { default: () => row.stockName })
    }
  },
  {
    title: '股票代码',
    key: 'stockCode'
  },
  {
    title: '最新分时',
    key: 'stockCode',
    render(row, index) {
      return h(sparkLine, { idSuffix:row.ID, stockName: row.stockName, stockCode: row.stockCode, lastPrice: row.stockCurrentPrice, openPrice: row.stockPrePrice, tooltip: true }, )
    }
  },
  {
    title: '最新',
    key: 'stockCurrentPrice',
    minWidth: 120,
    render(row, index) {

      let diff = ((Number(row.stockCurrentPrice) - Number(row.stockPrePrice))/ Number(row.stockPrePrice)*100).toFixed(2)

      if(Number(row.stockCurrentPrice)< Number(row.stockPrePrice)) {
        return [h(NText, { type: "success", bordered: false }, { default: () => row.stockCurrentPrice+` |  ${diff}%` })]
      } else {
        return [h(NText, { type: "error" , bordered: false}, { default: () => row.stockCurrentPrice+` |  ${diff}%` })]
      }
    }
  },
  {
    title: '推荐时',
    key: 'stockPrice',
    render(row, index) {

      if(vipLevel.value===""|| Number(vipLevel.value) <=0){
        return h(NText, { type: "info" }, { default: () => row.stockPrice })
      }

      let diff = ((Number(row.stockCurrentPrice) - Number(row.stockPrice))/ Number(row.stockPrice)*100).toFixed(2)
      let flagStr="暂平"
      let flag="info"
      if(Number(row.stockCurrentPrice)>Number(row.stockPrice)) {
        flagStr="暂赢 "+diff+"%"
        flag="error"
      }else if(Number(row.stockCurrentPrice)===Number(row.stockPrice)){
        flagStr="暂平"
        flag="info"
      }else{
        flagStr="暂亏 "+ diff+"%"
        flag="success"
      }

      return [h(NText, { type: "info" }, { default: () => row.stockPrice }),h(NTag, { type: flag,size: "tiny", bordered: false }, { default: () => flagStr })]
    }
  },
  {
    title: '昨收',
    key: 'stockPrePrice',
    render(row, index) {
      return h(NText, { type: "info" }, { default: () => row.stockPrePrice })
    }
  },
  {
    title: '开仓价',
    key: 'recommendBuyPrice',
    render(row, index) {
      if(vipLevel.value===""|| Number(vipLevel.value) <=0){
        return h(NText, { type: "info" }, { default: () => row.recommendBuyPrice })
      }


      if(row.recommendBuyPrice.includes("-")){
        let prices= row.recommendBuyPrice.split("-")
        if(Number(row.stockCurrentPrice)>=Number(prices[0])&&Number(row.stockCurrentPrice)<=Number(prices[1])){
          return [h(NText, { type: "success" }, { default: () => row.recommendBuyPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Buy" })]
        }
      }
      if(row.recommendBuyPriceMin&&row.recommendBuyPriceMax&&Number(row.stockCurrentPrice)<Number(row.recommendBuyPriceMax)&&Number(row.stockCurrentPrice)>Number(row.recommendBuyPriceMin)){
        return [h(NText, { type: "success" }, { default: () => row.recommendBuyPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Buy" })]
      }
      return h(NText, { type: "info" }, { default: () => row.recommendBuyPrice })

    }
  },
  {
    title: '止盈价',
    key: 'recommendStopProfitPrice',
    render(row, index) {
      if(vipLevel.value===""|| Number(vipLevel.value) <=0){
        return h(NText, { type: "info" }, { default: () => row.recommendStopProfitPrice })
      }
      if(row.recommendStopProfitPrice.includes("-")){
        let prices= row.recommendStopProfitPrice.split("-")
        if(Number(row.stockCurrentPrice)>=Number(prices[0])&&Number(row.stockCurrentPrice)<=Number(prices[1])){
          return [h(NText, { type: "success" }, { default: () => row.recommendStopProfitPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Sell" })]
        }
      }
      if(row.recommendStopProfitPriceMin&&Number(row.stockCurrentPrice)>row.recommendStopProfitPriceMin){
        return [h(NText, { type: "success" }, { default: () => row.recommendStopProfitPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Sell" })]
      }

      return h(NText, { type: "info" }, { default: () => row.recommendStopProfitPrice })
    }
  },
  {
    title: '止损价',
    key: 'recommendStopLossPrice',
    render(row, index) {
      if(vipLevel.value===""|| Number(vipLevel.value) <=0){
        return h(NText, { type: "info" }, { default: () => row.recommendStopLossPrice })
      }
      if(row.recommendStopLossPrice.includes("-")){
        let prices= row.recommendStopLossPrice.split("-")
        if(Number(row.stockCurrentPrice)<=Number(prices[0])){
          return [h(NText, { type: "success" }, { default: () => row.recommendStopLossPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Sell" })]
        }
      }else{
        let prices=row.recommendStopLossPrice
        if(Number(row.stockCurrentPrice)<=Number(prices)){
          return [h(NText, { type: "success" }, { default: () => row.recommendStopLossPrice }),h(NTag, { type: "error", size: "tiny", bordered: false }, { default: () => "Sell" })]
        }
      }
      return h(NText, { type: "info" }, { default: () => row.recommendStopLossPrice })

    }
  },
  {
    title: '推荐理由',
    key: 'recommendReason',
    ellipsis: {
      tooltip: isValidVip
    }
  },
  {
    title: '风险提示',
    key: 'riskRemarks',
    ellipsis: {
      tooltip: isValidVip
    }
  },
  {
    title: '备注',
    key: 'remarks',
    ellipsis: {
      tooltip: isValidVip
    }
  },
  {
    title: '监控预警',
    key: 'enableAlert',
    width: 80,
    render(row, index) {
      return h(NSwitch, {
        value: row.enableAlert,
        onUpdateValue: (newValue) => toggleAlert(row, newValue)
      })
    }
  },
  {
    title: '操作',
    render(row, index) {
      return [h(
          NTag,
          {
            strong: true,
            tertiary: true,
            //size: 'small',
            type: 'warning', // 橙色按钮
            onClick: () => showDetail(row)
          },
          { default: () => '查看' }
      ),h(NTag, { strong: true,
        tertiary: true, type: 'error',  onClick: () => deleteAiRecommendStocks(row.ID) }, { default: () => '删除' })]
    }
  },
])
const paginationReactive = reactive({
  page: 1,
  pageCount: 1,
  pageSize: 12,
  itemCount: 0,
  keyword: "",
  enableAlert: null, // null 表示全部，true 表示已开启，false 表示未开启
  range: [
    new Date(new Date().getTime() - 3 * 24 * 60 * 60 * 1000), // 前3天
    new Date() // 当天
  ],
  prefix({ itemCount }) {
    return `${itemCount} 条记录`
  }
})

const enableAlertOptions = [
  { label: '全部', value: null },
  { label: '已开启预警', value: true },
  { label: '未开启预警', value: false }
]

const modalDataRef = reactive({
  visible: false,
  title: "",
  content: "",
  riskRemarks: "",
  stockCode: "",
  stockName: "",
  remarks: "",
  /** 传给 K 线组件的多单价位（与 StockLightweightKlineChart v-model 同步） */
  longEntryPrice: '',
  longStopLossPrice: '',
  longTakeProfitPrice: '',
})

const theme = computed(() => {
  return editorDataRef.darkTheme ? 'dark' : 'light'
})


// 拉取当前筛选条件下的全量数据（不分页）用于导出
function fetchAllForExport() {
  return new Promise((resolve, reject) => {
    GetAiRecommendStocksList({
      page: 1,
      pageSize: 10000,
      modelName: paginationReactive.keyword,
      stockName: paginationReactive.keyword,
      stockCode: paginationReactive.keyword,
      bkName: paginationReactive.keyword,
      startDate: formatDate(paginationReactive.range[0]),
      endDate: formatDate(paginationReactive.range[1]),
      enableAlert: paginationReactive.enableAlert
    }).then(res => {
      resolve(res.list || []);
    }).catch(reject);
  });
}

// 把记录转成中文表头的扁平 row（用于 xlsx / json）
function toExportRow(item) {
  return {
    '股票代码': item.stockCode || '',
    '股票名称': item.stockName || '',
    '行业/板块': item.bkName || '',
    '评级': item.rating || '',
    '模型': item.modelName || '',
    '推荐时价': item.stockPrice || '',
    '当前价': item.stockCurrentPrice || '',
    '前日收盘价': item.stockPrePrice || '',
    '当前价时间': item.stockCurrentPriceTime || '',
    '建议买入价': item.recommendBuyPrice || '',
    '买入下限': item.recommendBuyPriceMin || '',
    '买入上限': item.recommendBuyPriceMax || '',
    '建议止盈价': item.recommendStopProfitPrice || '',
    '止盈下限': item.recommendStopProfitPriceMin || '',
    '止盈上限': item.recommendStopProfitPriceMax || '',
    '建议止损价': item.recommendStopLossPrice || '',
    '推荐理由': item.recommendReason || '',
    '风险提示': item.riskRemarks || '',
    '备注': item.remarks || '',
    '已开启预警': item.enableAlert ? '是' : '否',
    '推荐时间': item.dataTime ? format(new Date(item.dataTime), 'yyyy-MM-dd HH:mm:ss') : '',
  };
}

function downloadFileName(ext) {
  return `AI股票推荐_${format(new Date(), 'yyyyMMdd_HHmmss')}.${ext}`;
}

async function exportXlsx() {
  try {
    message.loading('正在导出 XLSX...', { duration: 0 });
    const list = await fetchAllForExport();
    if (!list.length) {
      message.destroyAll();
      message.warning('当前筛选条件下没有数据可导出');
      return;
    }
    const rows = list.map(toExportRow);
    const ws = XLSX.utils.json_to_sheet(rows);

    // 自适应列宽（按表头/值长度估算）
    const colWidths = Object.keys(rows[0]).map(key => {
      const maxLen = Math.max(
        key.length * 2, // 中文表头按 2 倍宽
        ...rows.slice(0, 200).map(r => String(r[key] ?? '').length)
      );
      return { wch: Math.min(60, Math.max(10, maxLen + 2)) };
    });
    ws['!cols'] = colWidths;

    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, ws, 'AI推荐');
    const buf = XLSX.write(wb, { bookType: 'xlsx', type: 'array' });
    saveAs(new Blob([buf], { type: 'application/octet-stream' }), downloadFileName('xlsx'));
    message.destroyAll();
    message.success(`已导出 ${list.length} 条记录`);
  } catch (e) {
    message.destroyAll();
    message.error('导出 XLSX 失败: ' + (e?.message || e));
  }
}

async function exportJson() {
  try {
    message.loading('正在导出 JSON...', { duration: 0 });
    const list = await fetchAllForExport();
    if (!list.length) {
      message.destroyAll();
      message.warning('当前筛选条件下没有数据可导出');
      return;
    }
    const payload = {
      exportedAt: format(new Date(), 'yyyy-MM-dd HH:mm:ss'),
      filter: {
        keyword: paginationReactive.keyword,
        startDate: formatDate(paginationReactive.range[0]),
        endDate: formatDate(paginationReactive.range[1]),
        enableAlert: paginationReactive.enableAlert,
      },
      total: list.length,
      list,
    };
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' });
    saveAs(blob, downloadFileName('json'));
    message.destroyAll();
    message.success(`已导出 ${list.length} 条记录`);
  } catch (e) {
    message.destroyAll();
    message.error('导出 JSON 失败: ' + (e?.message || e));
  }
}

function query({
                 page,
                 pageSize = 10,
                 order = 'desc',
                 keyword = "",
                 startDate = "",
                 endDate = "",
                 enableAlert = null
               }) {
  return new Promise((resolve) => {

    GetAiRecommendStocksList({
      "page": page,
      "pageSize": pageSize,
      "modelName":keyword,
      "stockName":keyword,
      "stockCode":keyword,
      "bkName":keyword,
      "startDate": startDate,
      "endDate": endDate,
      "enableAlert": enableAlert
    }).then((res) => {
      const pagedData =res.list
      const total = res.total
      const pageCount =res.totalPages
      resolve({
        pageCount,
        data: pagedData,
        total
      })
    })
  })
}

function handlePageChange(currentPage) {
  if (!loadingRef.value) {
    loadingRef.value = true
    query({
      page: currentPage,
      pageSize: paginationReactive.pageSize,
      order: "desc",
      keyword: paginationReactive.keyword,
      startDate: formatDate(paginationReactive.range[0]), // Format date to string
      endDate: formatDate(paginationReactive.range[1]), // Format date to string
      enableAlert: paginationReactive.enableAlert
    }).then((data) => {
      dataRef.value = data.data
      paginationReactive.page = currentPage
      paginationReactive.pageCount = data.pageCount
      paginationReactive.itemCount = data.total
      loadingRef.value = false
    })
  }
}
function handleSearch() {
  if (!loadingRef.value) {
    loadingRef.value = true
    query({
      page: paginationReactive?.page ?? 1,
      pageSize: paginationReactive.pageSize,
      order: "desc",
      keyword: paginationReactive.keyword,
      startDate: formatDate(paginationReactive.range[0]),
      endDate: formatDate(paginationReactive.range[1]),
      enableAlert: paginationReactive.enableAlert
    }).then((data) => {
      dataRef.value = data.data
      paginationReactive.page = data.page
      paginationReactive.pageCount = data.pageCount
      paginationReactive.itemCount = data.total
      loadingRef.value = false
    })
  }
}
function formatDate(dateString) {
  const date = new Date(dateString)
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  // const hours = String(date.getHours()).padStart(2, '0')
  // const minutes = String(date.getMinutes()).padStart(2, '0')
  // const seconds = String(date.getSeconds()).padStart(2, '0')
  //return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
  return `${year}-${month}-${day}`
}
function getStockCode(stockCode) {
  if(stockCode.indexOf( ".")>0){
    stockCode=stockCode.split(".")[1]+stockCode.split(".")[0]
  }
  //转化为小写
  stockCode=stockCode.toLowerCase()
  return stockCode

}

/** 推荐价可能为区间 "a-b"，取左侧作为图上开仓/止损/止盈线参考价 */
function recommendRangeToSinglePrice(p) {
  if (p == null || String(p).trim() === '') return ''
  const s = String(p).trim()
  const i = s.indexOf('-')
  if (i > 0) return s.slice(0, i).trim()
  return s
}

function showDetail(row) {
  if(vipLevel.value===""|| Number(vipLevel.value) <=0){
    notify.warning({content: '未开通VIP或者已经过期'})
    return
  }
  modalDataRef.title = row.stockName
  modalDataRef.content = row.recommendReason
  modalDataRef.riskRemarks = row.riskRemarks
  modalDataRef.stockCode = getStockCode(row.stockCode)
  modalDataRef.stockName = row.stockName
  modalDataRef.visible = true
  modalDataRef.remarks = row.remarks
  modalDataRef.longEntryPrice = recommendRangeToSinglePrice(row.recommendBuyPrice)
  modalDataRef.longStopLossPrice = recommendRangeToSinglePrice(row.recommendStopLossPrice)
  modalDataRef.longTakeProfitPrice = recommendRangeToSinglePrice(row.recommendStopProfitPrice)
}
function rowProps(row) {
  return {
    style: 'cursor: pointer;',
    onClick: () => {
      showDetail(row)
    }
  }
}
function deleteAiRecommendStocks(id) {
  DeleteAiRecommendStocks(id).then((res) => {
    notify.info({content: res, duration: 2000})
    handleSearch()
  })
}

function toggleAlert(row, newEnableAlert) {
  UpdateAiRecommendStocksAlert(row.ID, newEnableAlert).then((res) => {
    notify.info({content: res, duration: 2000})
    // 更新本地数据
    row.enableAlert = newEnableAlert
  })
}

</script>

<template>
  <n-input-group>
    <n-date-picker  v-model:value="paginationReactive.range" type="daterange"   style="width: 40%"/>
    <n-select v-model:value="paginationReactive.enableAlert" :options="enableAlertOptions" placeholder="预警状态" style="width: 15%" clearable />
    <n-input clearable placeholder="输入关键词搜索" v-model:value="paginationReactive.keyword"/>
    <n-button type="primary" ghost @click="handleSearch"  @input="handleSearch">
      搜索
    </n-button>
    <n-button type="success" ghost @click="exportXlsx" title="导出当前筛选条件下的全部记录为 XLSX">
      导出 XLSX
    </n-button>
    <n-button type="info" ghost @click="exportJson" title="导出当前筛选条件下的全部记录为 JSON">
      导出 JSON
    </n-button>
  </n-input-group>
        <n-data-table
            remote
            size="small"
            :columns="columnsRef"
            :data="dataRef"
            :loading="loadingRef"
            :pagination="paginationReactive"
            :row-key="(rowData)=>rowData.ID"
            @update:page="handlePageChange"
            flex-height
            style="height: calc(100vh - 210px);margin-top: 10px"
        />

  <n-modal v-model:show="modalDataRef.visible" :title="modalDataRef.title" preset="card" style="max-width: 1400px;">
    <n-gradient-text :size="16" type="warning">{{modalDataRef.remarks}}</n-gradient-text>
    <n-card size="small">
      <StockLightweightKlineChart
        style="width: 100%;"
        :code="modalDataRef.stockCode"
        :chart-height="350"
        :stock-name="modalDataRef.stockName"
        :dark-theme="editorDataRef.darkTheme"
        v-model:long-entry-price="modalDataRef.longEntryPrice"
        v-model:long-stop-loss-price="modalDataRef.longStopLossPrice"
        v-model:long-take-profit-price="modalDataRef.longTakeProfitPrice"
      />
    </n-card>
    <n-card size="small">
    <n-text type="info">{{modalDataRef.content}}</n-text>
    <n-divider><n-gradient-text type="error">风险提示</n-gradient-text></n-divider>
    <n-text type="error">{{modalDataRef.riskRemarks}}</n-text>
    </n-card>
  </n-modal>
</template>

<style scoped>

</style>