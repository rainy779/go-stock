<script setup lang="ts">
import {onBeforeMount, ref} from 'vue'
import {GetStockList, StockNotice, USStockNotice, HKStockNotice} from "../../wailsjs/go/main/App";
import {BrowserOpenURL} from "../../wailsjs/runtime";
import {RefreshCircleSharp} from "@vicons/ionicons5";
import _ from "lodash";
import KLineChart from "./KLineChart.vue";
import MoneyTrend from "./moneyTrend.vue";
import {useMessage} from "naive-ui";

const props = defineProps({
  stockCode: {
    type: String,
    default: ''
  },
  market: {
    type: String,
    default: 'a-share' // 'a-share' / 'us' / 'hk'
  }
})
const stockCode = props.stockCode

const list  = ref([])
const options =  ref([])
const message=useMessage()
function getNotice(stockCodes) {
  let promise
  if (props.market === 'us') {
    promise = USStockNotice(stockCodes)
  } else if (props.market === 'hk') {
    // 港股：空 → 后端聚合 10 只热门港股；非空 → 查单股
    promise = HKStockNotice(stockCodes || '')
  } else {
    promise = StockNotice(stockCodes)
  }
  promise.then(result => {
    console.log(`[${props.market}] notice result`, result)
    list.value = result || []
  })
}

onBeforeMount (()=>{
  getNotice(stockCode);
})

function findStockList(query){
  // 美股 / 港股不查 A 股代码字典，直接走文本搜索
  if (props.market === 'us' || props.market === 'hk') {
    getNotice(query || '')
    return
  }
  if (query){
    GetStockList(query).then(result => {
      options.value=result.map(item => {
        return {
          label: item.name+" - "+item.ts_code,
          value: item.ts_code
        }
      })
    })
  }else{
    getNotice("")
  }
}
function handleSearch(value) {
  getNotice(value)
}
function openWin(item) {
  // 美股 SEC EDGAR 直接打开 filing URL
  if (item && item.infoLink) {
    BrowserOpenURL(item.infoLink)
    return
  }
  // A股 东财 PDF
  const code = (item && item.art_code) ? item.art_code : item
  if (code) {
    BrowserOpenURL("https://pdf.dfcfw.com/pdf/H2_"+code+"_1.pdf?1750092081000.pdf")
  }
}
function getTypeColor(name){
  if(name.includes("质押")||name.includes("冻结")||name.includes("解冻")||name.includes("解押")||name.includes("解禁")){
    return "error"
  }
  if(name.includes("异常")||name.includes("减持")||name.includes("增发")||name.includes("重大")){
    return "error"
  }
  if(name.includes("季度报告")||name.includes("年度报告")||name.includes("澄清公告")||name.includes("风险")){
    return "error"
  }
  if(name.includes("终止")||name.includes("复牌")||name.includes("停牌")||name.includes("退市")){
    return "error"
  }
  if(name.includes("破产")||name.includes("清算")){
    return "error"
  }
  if(name.includes("回购")||name.includes("重组")||name.includes("诉讼")||name.includes("仲裁")||name.includes("转让")||name.includes("收购")){
    return "warning"
  }
  if(name.includes("调研")||name.includes("募集")){
    return "warning"
  }

  return "info"

}
function getmMarketCode(market,code) {
  if(market==="0"){
    return "sz"+code
  }else if(market==="1"){
    return "sh"+code
  }else if(market==="2"){
    return "bj"+code
  }else if(market==="3"){
    return "hk"+code
  }else{
    return code
  }
}
</script>

<template>
  <n-card>
    <n-auto-complete v-if="market === 'a-share'"
                     :options="options"
                     placeholder="请输入A股名称或者代码"
                     clearable filterable
                     :on-select="handleSearch"
                     :on-update:value="findStockList" />
    <n-input v-else-if="market === 'us'"
             placeholder="按公司名/CIK 过滤（留空看全市场最新）"
             clearable
             @update:value="handleSearch" />
    <n-input v-else
             placeholder="留空看热门 10 只港股聚合，或输入代码查单只（如 00700 腾讯、09988 阿里）"
             clearable
             @update:value="handleSearch" />
  </n-card>
  <n-table striped size="small">
    <n-thead>
      <n-tr>
        <n-th>{{ market === 'us' ? 'CIK' : (market === 'hk' ? '港股代码' : '股票代码') }}</n-th>
        <n-th>{{ market === 'us' ? '公司名称' : (market === 'hk' ? '公司名称' : '股票名称') }}</n-th>
        <n-th>公告标题</n-th>
        <n-th>公告类型</n-th>
        <n-th>公告日期</n-th>
        <n-th><n-flex>数据更新时间<n-icon @click="getNotice('')" color="#409EFF" :size="20"  :component="RefreshCircleSharp"/></n-flex></n-th>
      </n-tr>
    </n-thead>
    <n-tbody>
      <n-tr v-for="(item, idx) in list" :key="(item.art_code || item.infoLink || idx)">
        <n-td>
          <!-- A 股：hover 显示资金流向；美股：CIK 只显示 tag -->
          <n-popover v-if="market === 'a-share'" trigger="hover" placement="right">
            <template #trigger>
              <n-tag type="info"  :bordered="false">{{item.codes[0].stock_code }}</n-tag>
            </template>
            <money-trend style="width: 800px" :code="getmMarketCode(item.codes[0].market_code,item.codes[0].stock_code)" :name="item.codes[0].short_name"  :days="360" :dark-theme="true" :chart-height="500"></money-trend>
          </n-popover>
          <n-tag v-else type="info" :bordered="false">{{item.codes[0].stock_code}}</n-tag>
        </n-td>
        <n-td>
          <!-- A 股：hover 显示 K线；美股：直接显示名字 -->
          <n-popover v-if="market === 'a-share'" trigger="hover" placement="right">
            <template #trigger>
              <n-tag type="info"  :bordered="false">{{item.codes[0].short_name }}</n-tag>
            </template>
            <k-line-chart style="width: 800px" :code="getmMarketCode(item.codes[0].market_code,item.codes[0].stock_code)" :chart-height="500" :stockName="item.codes[0].short_name" :k-days="20" :dark-theme="true"></k-line-chart>
          </n-popover>
          <n-tag v-else type="info" :bordered="false">{{item.codes[0].short_name}}</n-tag>
        </n-td>
        <n-td>
          <n-a type="info"  @click="openWin(item)"><n-text  :type="getTypeColor(item.columns[0].column_name)"> {{item.title}}</n-text></n-a>
        </n-td>
        <n-td>
          <n-text :type="getTypeColor(item.columns[0].column_name)">{{item.columns[0].column_name }}</n-text>
        </n-td>
        <n-td>
          <n-tag type="info">{{(item.notice_date || '').substring(0,10) }}</n-tag>
        </n-td>
        <n-td>
          <n-tag type="info">{{(item.display_time || '').substring(0,19)}}</n-tag>
        </n-td>
      </n-tr>
    </n-tbody>
  </n-table>
</template>

<style scoped>

</style>