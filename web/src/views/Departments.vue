<template>
  <div>
    <n-card title="组织结构（部门）">
      <p class="tip">预先设定公司组织结构，确认扫描资产时可从这里选择部门。</p>
      <div class="toolbar">
        <n-input v-model:value="form.name" placeholder="部门名称，如 信息中心" style="width: 180px" />
        <n-input v-model:value="form.owners" placeholder="预设负责人，逗号分隔，如 张三,李四" style="width: 240px" />
        <n-select v-model:value="form.parentId" :options="parentOptions" placeholder="上级部门（可选）" clearable style="width: 180px" />
        <n-button type="primary" @click="add">添加部门</n-button>
        <ExportButton type="departments" title="组织结构" :columns="exportCols" />
      </div>
      <n-data-table :columns="columns" :data="rows" size="small" />
    </n-card>
  </div>
</template>

<script setup>
import { ref, h, computed, onMounted } from 'vue'
import { NButton, NPopconfirm, NTag, useMessage } from 'naive-ui'
import api from '../api'
import ExportButton from '../components/ExportButton.vue'

// 导出列
const exportCols = [
  { key: 'name', label: '部门' },
  { key: 'owners', label: '预设负责人' },
  { key: 'parentName', label: '上级部门' }
]

const message = useMessage()
const departments = ref([])
const form = ref({ name: '', owners: '', parentId: null })

const parentOptions = computed(() => departments.value.map(d => ({ label: d.name, value: d.id })))

const rows = computed(() => {
  const nameMap = {}
  departments.value.forEach(d => { nameMap[d.id] = d.name })
  return departments.value.map(d => ({
    ...d,
    parentName: d.parentId ? (nameMap[d.parentId] || '-') : '—'
  }))
})

const columns = [
  { title: '部门名称', key: 'name' },
  {
    title: '预设负责人', key: 'owners', width: 260,
    render: r => {
      if (!r.owners) return '—'
      return h('div', { style: 'display:flex;gap:6px;flex-wrap:wrap' },
        r.owners.split(',').filter(Boolean).map(o => h(NTag, { size: 'small', type: 'info' }, { default: () => o.trim() })))
    }
  },
  { title: '上级部门', key: 'parentName', width: 180, render: r => r.parentId ? h(NTag, { size: 'small' }, { default: () => r.parentName }) : '—' },
  { title: '操作', key: 'actions', width: 90, render: row => h(NPopconfirm, { onPositiveClick: () => del(row.id) }, { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除？子部门会提升到根级' }) }
]

async function load() { departments.value = await api.get('/departments') }

async function add() {
  if (!form.value.name) { message.warning('部门名称必填'); return }
  await api.post('/departments', { name: form.value.name, parentId: form.value.parentId, owners: form.value.owners })
  message.success('已添加')
  form.value = { name: '', owners: '', parentId: null }
  load()
}

async function del(id) { await api.delete(`/departments/${id}`); message.success('已删除'); load() }

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: #94a3b8; font-size: 13px; margin: 0 0 12px; }
</style>
