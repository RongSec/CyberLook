<template>
  <n-button secondary @click="open">导出</n-button>
  <n-modal v-model:show="show" preset="card" :title="`导出 ${title || ''}（自选列）`" style="width: 460px">
    <div class="head">
      <n-checkbox :checked="allChecked" :indeterminate="someChecked" @update:checked="toggleAll">全选</n-checkbox>
    </div>
    <n-checkbox-group v-model:value="selected">
      <n-space vertical :size="8" style="margin-top: 8px">
        <n-checkbox v-for="c in columns" :key="c.key" :value="c.key" :label="c.label" />
      </n-space>
    </n-checkbox-group>
    <template #footer>
      <n-space justify="end">
        <n-button @click="show = false">取消</n-button>
        <n-button type="primary" @click="doExport">导出 Excel</n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useMessage } from 'naive-ui'
import api from '../api'

const props = defineProps({
  type: { type: String, required: true },
  title: { type: String, default: '' },
  columns: { type: Array, required: true },
  filters: { type: Object, default: () => ({}) },
  conds: { type: Array, default: () => [] }   // 当前 SPL 筛选条件
})

const message = useMessage()
const show = ref(false)
const selected = ref([])

const allChecked = computed(() => selected.value.length === props.columns.length)
const someChecked = computed(() => selected.value.length > 0 && selected.value.length < props.columns.length)

function open() {
  selected.value = props.columns.map(c => c.key)
  show.value = true
}
function toggleAll(v) {
  selected.value = v ? props.columns.map(c => c.key) : []
}

async function doExport() {
  if (selected.value.length === 0) { message.warning('请至少选择一列'); return }
  try {
    const res = await api.post('/export/excel', {
      type: props.type,
      columns: selected.value,
      filters: props.filters,
      conds: props.conds,
      title: props.title
    }, { responseType: 'blob' })
    const url = URL.createObjectURL(res)
    const a = document.createElement('a')
    a.href = url
    a.download = `${props.title || props.type}.xlsx`
    a.click()
    URL.revokeObjectURL(url)
    show.value = false
    message.success('已导出')
  } catch (e) {
    message.error('导出失败')
  }
}
</script>

<style scoped>
.head { padding-bottom: 8px; border-bottom: 1px solid var(--border-subtle); }
</style>
