<script setup>
import {onBeforeMount, ref} from 'vue'
import {GetStockList, IndustryResearchReport, HKIndustryResearchReport, USIndustryResearchReport, EMDictCode} from "../../wailsjs/go/main/App";
import {ArrowDownOutline, CaretDown, CaretUp, PulseOutline, Refresh, RefreshCircleSharp,} from "@vicons/ionicons5";

import {useMessage} from "naive-ui";
import {BrowserOpenURL} from "../../wailsjs/runtime";

const props = defineProps({
  market: {
    type: String,
    default: 'a-share' // 'a-share' / 'hk' / 'us'
  }
})

const message=useMessage()
const allList = ref([])    // 完整数据集
const list  = ref([])       // 当前展示（可能被搜索过滤）
const searchKeyword = ref('')

const options =  ref([])

function getIndustryResearchReport(value) {
  message.loading("正在刷新数据...")
  let promise
  if (props.market === 'hk') {
    promise = HKIndustryResearchReport(value)
  } else if (props.market === 'us') {
    promise = USIndustryResearchReport(value)
  } else {
    promise = IndustryResearchReport(value)
  }
  promise.then(result => {
    console.log(`[${props.market}] result`, result)
    allList.value = result || []
    applyClientFilter()  // 应用当前搜索关键词
  })
}

// 港股美股专用：前端关键词过滤（标题/行业/机构/英文原文）
function applyClientFilter() {
  const kw = (searchKeyword.value || '').trim().toLowerCase()
  if (!kw) {
    list.value = allList.value
    return
  }
  list.value = allList.value.filter(item => {
    const fields = [item.title, item.industryName, item.orgSName, item.originalTitle]
    return fields.some(f => f && String(f).toLowerCase().includes(kw))
  })
}

function onClientSearch(value) {
  searchKeyword.value = value
  applyClientFilter()
}

onBeforeMount(()=>{
  getIndustryResearchReport('');
})

function ratingChangeName(ratingChange){
  if(ratingChange===0){
    return '调高'
  }else if(ratingChange===1){
    return '调低'
  }else if(ratingChange===2){
    return '首次'
  }else if(ratingChange===3){
    return '维持'
  }else if (ratingChange===4){
    return '无变化'
  }else{
    return ''
  }
}
function openWin(item) {
  // Yahoo / 英文源直接有 infoLink → 打开新闻链接
  if (item && item.infoLink) {
    BrowserOpenURL(item.infoLink)
    return
  }
  // 东财研报 → 用 infoCode 拼 PDF URL
  const code = (item && item.infoCode) ? item.infoCode : item
  if (code) {
    BrowserOpenURL("https://pdf.dfcfw.com/pdf/H3_"+code+"_1.pdf?1749744888000.pdf")
  }
}

function EMDictCodeList(keyVal){
  if (keyVal){
    EMDictCode('016').then(result => {
      console.log(result)
        options.value=result.filter((value,index,array) => value.bkName.includes(keyVal)||value.firstLetter.includes(keyVal)||value.bkCode.includes(keyVal)).map(item => {
          return {
            label: item.bkName+" - "+item.bkCode,
            value: item.bkCode
          }
        })
    })
  }else{
    getIndustryResearchReport('')
  }

}
function handleSearch(value) {
  getIndustryResearchReport(value)
}
</script>

<template>
  <n-card>
    <!-- A 股：按行业字典自动补全 -->
    <n-auto-complete v-if="market === 'a-share'"
                     :options="options"
                     placeholder="请输入行业名称关键词搜索"
                     clearable filterable
                     :on-select="handleSearch"
                     :on-update:value="EMDictCodeList" />
    <!-- 港股/美股：前端关键词过滤（搜标题/行业/机构/英文原文） -->
    <n-input v-else
             :value="searchKeyword"
             placeholder="搜索关键词（标题/行业/机构，英文原文也支持）"
             clearable
             @update:value="onClientSearch" />
  </n-card>
  <n-table striped size="small">
    <n-thead>
      <n-tr>
<!--        <n-th>代码</n-th>-->
<!--        <n-th>名称</n-th>-->
        <n-th>行业</n-th>
        <n-th>标题</n-th>
        <n-th v-if="market === 'a-share'">东财评级</n-th>
        <n-th v-if="market === 'a-share'">评级变动</n-th>
        <n-th v-if="market === 'a-share'">机构评级</n-th>
        <n-th v-if="market === 'a-share'">分析师</n-th>
        <n-th>机构</n-th>
        <n-th> <n-flex justify="space-between">日期<n-icon @click="getIndustryResearchReport" color="#409EFF" :size="20"  :component="RefreshCircleSharp"/></n-flex></n-th>
      </n-tr>
    </n-thead>
    <n-tbody>
      <n-tr v-for="(item, idx) in list" :key="(item.infoCode || item.infoLink || idx)">
<!--        <n-td>{{item.stockCode}}</n-td>-->
<!--        <n-td :title="item.stockCode">-->
<!--          <n-popover trigger="hover" placement="right">-->
<!--            <template #trigger>-->
<!--              <n-tag type="info"  :bordered="false">{{item.stockName}}</n-tag>-->
<!--            </template>-->
<!--            <k-line-chart style="width: 800px" :code="getmMarketCode(item.market,item.stockCode)" :chart-height="500" :name="item.stockName" :k-days="20" :dark-theme="true"></k-line-chart>-->
<!--          </n-popover>-->
<!--        </n-td>-->
        <n-td><n-tag type="info"  :bordered="false">{{item.industryName}}</n-tag></n-td>
        <n-td>
          <n-a type="info"  @click="openWin(item)"><n-text type="success">{{item.title}}</n-text></n-a>
        </n-td>
        <n-td><n-text :type="item.emRatingName==='增持'?'error':'info'">
          {{item.emRatingName}}
        </n-text></n-td>
        <n-td><n-text :type="item.ratingChange===0?'error':'info'">{{ratingChangeName(item.ratingChange)}}</n-text></n-td>
        <n-td>{{item.sRatingName	}}</n-td>
        <n-td><n-ellipsis style="max-width: 120px">{{item.researcher}}</n-ellipsis></n-td>
        <n-td>{{item.orgSName}}</n-td>
        <n-td>{{(item.publishDate || '').substring(0,10)}}</n-td>
      </n-tr>
    </n-tbody>
</n-table>
</template>

<style scoped>

</style>