<script setup>
import { AnalyzeSentimentByMarket } from "../../wailsjs/go/main/App";
import * as echarts from "echarts";
import { onMounted, onUnmounted, ref } from "vue";

const props = defineProps({
  market: {
    type: String,
    default: 'hk' // 'hk' / 'us' / 'a-share'
  },
  name: {
    type: String,
    default: '24 小时热词'
  },
  chartHeight: {
    type: Number,
    default: 300
  },
  darkTheme: {
    type: Boolean,
    default: false
  }
})

const treemapRef = ref(null)
let treemapChart = null
let refreshInterval = null

function render() {
  AnalyzeSentimentByMarket(props.market).then(res => {
    if (!treemapRef.value) return

    const frequencies = res?.frequencies || []
    if (!frequencies.length) {
      // 无数据时显示提示
      if (treemapChart) {
        treemapChart.clear()
      } else {
        treemapChart = echarts.init(treemapRef.value)
      }
      treemapChart.setOption({
        title: {
          text: props.name + '（暂无数据）',
          left: 'center',
          textStyle: { color: props.darkTheme ? '#999' : '#aaa', fontSize: 13 }
        }
      })
      return
    }

    if (!treemapChart) {
      treemapChart = echarts.init(treemapRef.value)
    }

    const dataByScore = frequencies.map(item => ({
      name: item.Word,
      frequency: item.Frequency,
      weight: item.Weight,
      value: item.Score
    }))
    const dataByFreq = frequencies.map(item => ({
      name: item.Word,
      value: item.Frequency,
      frequency: item.Frequency,
      weight: item.Weight
    }))
    const dataByWeight = frequencies.map(item => ({
      name: item.Word,
      value: item.Weight,
      frequency: item.Frequency,
      weight: item.Weight
    }))

    const formatUtil = echarts.format

    const option = {
      darkMode: props.darkTheme,
      title: {
        text: props.name,
        left: 'center',
        textStyle: { color: props.darkTheme ? '#ccc' : '#456' }
      },
      legend: { show: false },
      toolbox: {
        left: '20px',
        feature: {
          saveAsImage: { title: '保存图片' },
          restore: { title: '默认' },
          myToolWeight: {
            show: true,
            title: '按权重',
            icon: 'path://M393.8816 148.1216a29.3376 29.3376 0 0 1-15.2576 38.0928c-43.776 17.152-81.92 43.8272-114.2784 76.2368A345.7536 345.7536 0 0 0 159.5392 512 352.8704 352.8704 0 0 0 512 864.4608a351.744 351.744 0 0 0 249.5488-102.912 353.536 353.536 0 0 0 76.2368-114.2784c5.6832-15.2576 22.8352-20.992 38.0928-15.2576 15.2576 5.7344 20.992 22.8864 15.2576 38.0928a421.2224 421.2224 0 0 1-89.6 133.376A412.6208 412.6208 0 0 1 512 921.6c-226.7136 0-409.6-182.8864-409.6-409.6 0-108.544 41.9328-211.456 120.0128-289.5872A421.2224 421.2224 0 0 1 355.84 132.864a29.3376 29.3376 0 0 1 38.0928 15.2576z',
            onclick: () => treemapChart.setOption({ series: { data: dataByWeight } })
          },
          myToolFreq: {
            show: true,
            title: '按频次',
            icon: 'path://M895.466667 476.8l-87.424-87.424v-123.626667a49.770667 49.770667 0 0 0-49.770667-49.770666h-123.626667L547.2 128.533333a49.792 49.792 0 0 0-70.4 0l-87.424 87.424h-123.626667a49.770667 49.770667 0 0 0-49.770666 49.770667v123.626667L128.533333 476.8a49.792 49.792 0 0 0 0 70.4l87.424 87.424v123.626667a49.770667 49.770667 0 0 0 49.770667 49.770666h123.626667l87.424 87.424a49.792 49.792 0 0 0 70.4 0l87.424-87.424h123.626666a49.770667 49.770667 0 0 0 49.770667-49.770666v-123.626667l87.424-87.424a49.749333 49.749333 0 0 0 0.042667-70.4z',
            onclick: () => treemapChart.setOption({ series: { data: dataByFreq } })
          }
        }
      },
      tooltip: {
        formatter: function (info) {
          const value = (info.value || 0).toFixed(2)
          return [
            '<div class="tooltip-title">' + info.name + '</div>',
            '热度: ' + formatUtil.addCommas(value),
            '<div class="tooltip-title">频次: ' + formatUtil.addCommas(info.data.frequency) + '</div>',
            '<div class="tooltip-title">权重: ' + formatUtil.addCommas(info.data.weight) + '</div>'
          ].join('')
        }
      },
      series: [
        {
          type: 'treemap',
          breadcrumb: { show: false },
          left: '0',
          top: '40',
          right: '0',
          bottom: '0',
          tooltip: { show: true },
          data: dataByScore
        }
      ]
    }
    treemapChart.setOption(option)
  })
}

onMounted(() => {
  render()
  // 每 30 秒刷新一次
  refreshInterval = setInterval(render, 30 * 1000)
})

onUnmounted(() => {
  if (refreshInterval) clearInterval(refreshInterval)
  if (treemapChart) {
    treemapChart.dispose()
    treemapChart = null
  }
})
</script>

<template>
  <div ref="treemapRef"
       style="width: 100%; --wails-draggable:no-drag"
       :style="{ height: chartHeight + 'px' }"></div>
</template>

<style scoped>
</style>
