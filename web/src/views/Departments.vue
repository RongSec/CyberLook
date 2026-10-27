<template>
  <div>
    <n-card title="组织结构（部门）">
      <p class="tip">预先设定公司组织结构，确认扫描资产时可从这里选择部门。</p>
      <div class="toolbar">
        <n-input v-model:value="form.name" placeholder="部门名称，如 信息中心" style="width: 180px" />
        <n-input v-model:value="form.owners" placeholder="预设负责人，逗号分隔，如 张三,李四" style="width: 240px" />
        <n-select v-model:value="form.parentId" :options="parentOptions" placeholder="上级部门（可选）" clearable style="width: 180px" />
        <n-button type="primary" @click="add">添加部门</n-button>
        <n-button secondary :disabled="checked.length < 2" @click="openMerge">合并部门</n-button>
        <n-button secondary :disabled="checked.length !== 1" @click="openSplit">拆分部门</n-button>
        <n-input v-model:value="keyword" placeholder="搜索部门/负责人" clearable style="width: 200px" />
        <n-input v-model:value="keyword" placeholder="搜索部门/负责人" clearable style="width: 200px" />
        <ExportButton type="departments" title="组织结构" :columns="exportCols" />
      </div>
      <n-data-table :columns="columns" :data="filteredRows" size="small" :row-key="r => r.id" :checked-row-keys="checked" @update:checked-row-keys="keys => checked = keys" />
    </n-card>

    <!-- 编辑部门 -->
    <n-modal v-model:show="showEdit" preset="card" title="编辑部门" style="width: 460px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="部门名称"><n-input v-model:value="editForm.name" /></n-form-item>
        <n-form-item label="负责人"><n-input v-model:value="editForm.owners" placeholder="多个用逗号分隔" /></n-form-item>
        <n-form-item label="上级部门">
          <n-select v-model:value="editForm.parentId" :options="parentOptions" clearable placeholder="无（根级）" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showEdit = false">取消</n-button>
          <n-button type="primary" @click="saveEdit">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 负责人编辑抽屉 -->
    <n-drawer v-model:show="showOwners" :width="440" placement="right">
      <n-drawer-content :title="'负责人 — ' + ownerDeptName" closable>
        <p class="tip" style="margin-bottom:12px">点击输入框直接改名，点「删除」移除；底部新增后点保存。</p>
        <div v-for="(o, idx) in ownerList" :key="idx"
          style="display:flex;gap:8px;margin-bottom:8px;align-items:center">
          <n-input v-model:value="o.name" size="small" placeholder="负责人姓名" />
          <n-button size="small" text type="error" @click="ownerList.splice(idx, 1)">删除</n-button>
        </div>
        <div style="display:flex;gap:8px;margin-top:16px;padding-top:12px;border-top:1px dashed #e5e7eb">
          <n-input v-model:value="newOwner" size="small" placeholder="新增负责人，回车或点添加"
            @keyup.enter="addOwner" />
          <n-button size="small" @click="addOwner">添加</n-button>
        </div>
        <template #footer>
          <n-space justify="end">
            <n-button @click="showOwners = false">取消</n-button>
            <n-button type="primary" @click="saveOwners">保存</n-button>
          </n-space>
        </template>
      </n-drawer-content>
    </n-drawer>

    <!-- 负责人编辑抽屉 -->
    <n-drawer v-model:show="showOwners" :width="440" placement="right">
      <n-drawer-content :title="'负责人 — ' + ownerDeptName" closable>
        <p class="tip" style="margin-bottom:12px">点击输入框直接改名，点「删除」移除；底部新增后点保存。</p>
        <div v-for="(o, idx) in ownerList" :key="idx"
          style="display:flex;gap:8px;margin-bottom:8px;align-items:center">
          <n-input v-model:value="o.name" size="small" placeholder="负责人姓名" />
          <n-button size="small" text type="error" @click="ownerList.splice(idx, 1)">删除</n-button>
        </div>
        <div style="display:flex;gap:8px;margin-top:16px;padding-top:12px;border-top:1px dashed #e5e7eb">
          <n-input v-model:value="newOwner" size="small" placeholder="新增负责人，回车或点添加"
            @keyup.enter="addOwner" />
          <n-button size="small" @click="addOwner">添加</n-button>
        </div>
        <template #footer>
          <n-space justify="end">
            <n-button @click="showOwners = false">取消</n-button>
            <n-button type="primary" @click="saveOwners">保存</n-button>
          </n-space>
        </template>
      </n-drawer-content>
    </n-drawer>

    <!-- 合并 -->
    <n-modal v-model:show="showMerge" preset="card" title="合并部门" style="width: 420px">
      <p class="tip">将选中的 {{ checked.length }} 个部门合并为一个：</p>
      <n-input v-model:value="mergeTarget" placeholder="目标部门名称（可新建）" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="showMerge = false">取消</n-button>
          <n-button type="primary" @click="doMerge">确认合并</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 拆分 -->
    <n-modal v-model:show="showSplit" preset="card" title="拆分部门" style="width: 560px">
      <p class="tip">将「{{ splitSource }}」拆分为多个新部门，勾选负责人分配到各部门。</p>
      <n-input v-model:value="splitName1" placeholder="新部门 1 名称" style="margin-bottom:8px" />
      <n-select v-model:value="splitOwners1" multiple :options="ownerOpts" placeholder="选择负责人" style="margin-bottom:12px" />
      <n-input v-model:value="splitName2" placeholder="新部门 2 名称" style="margin-bottom:8px" />
      <n-select v-model:value="splitOwners2" multiple :options="ownerOpts" placeholder="选择负责人（已被选的不可再选）" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="showSplit = false">取消</n-button>
          <n-button type="primary" @click="doSplit">确认拆分</n-button>
        </n-space>
      </template>
    </n-modal>
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
const checked = ref([])
const keyword = ref('')
const showOwners = ref(false)
const ownerDeptId = ref(null)
const ownerDeptName = ref('')
const ownerParentId = ref(null)
const ownerList = ref([])
const newOwner = ref('')

const filteredRows = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return rows.value
  return rows.value.filter(r =>
    (r.name || '').toLowerCase().includes(k) || (r.owners || '').toLowerCase().includes(k))
})

function openOwners(row) {
  ownerDeptId.value = row.id
  ownerDeptName.value = row.name
  ownerParentId.value = row.parentId || null
  ownerList.value = (row.owners || '').split(',').map(o => o.trim()).filter(Boolean).map(o => ({ name: o }))
  newOwner.value = ''
  showOwners.value = true
}

function addOwner() {
  const n = newOwner.value.trim()
  if (!n) return
  if (ownerList.value.some(o => o.name === n)) { message.warning('该负责人已存在'); return }
  ownerList.value.push({ name: n })
  newOwner.value = ''
}

async function saveOwners() {
  const names = ownerList.value.map(o => (o.name || '').trim()).filter(Boolean)
  try {
    await api.put(`/departments/${ownerDeptId.value}`, {
      name: ownerDeptName.value,
      owners: names.join(','),
      parentId: ownerParentId.value
    })
    message.success('负责人已保存')
    showOwners.value = false
    load()
  } catch (e) { message.error(e.response?.data?.error || '保存失败') }
}
const showMerge = ref(false)
const mergeTarget = ref('')
const showEdit = ref(false)
const editForm = ref({ id: null, name: '', owners: '', parentId: null })
const showSplit = ref(false)
const splitSource = ref('')
const splitName1 = ref('')
const splitName2 = ref('')
const splitOwners1 = ref([])
const splitOwners2 = ref([])
const ownerOpts = ref([])
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
  { type: 'selection', width: 44, fixed: 'left' },
  { title: '部门名称', key: 'name' },
  {
    title: '负责人', key: 'owners', width: 130,
    render: r => {
      const n = (r.owners || '').split(',').filter(Boolean).length
      return h(NButton, { size: 'small', secondary: true, onClick: () => openOwners(r) },
        { default: () => '负责人' + (n > 0 ? ' (' + n + ')' : '') })
    }
  },
  { title: '上级部门', key: 'parentName', width: 180, render: r => r.parentId ? h(NTag, { size: 'small' }, { default: () => r.parentName }) : '—' },
  { title: '操作', key: 'actions', width: 150, render: row => h('div', { style: 'display:flex;gap:6px' }, [
    h(NButton, { size: 'small', text: true, type: 'info', onClick: () => openEdit(row) }, { default: () => '编辑' }),
    h(NPopconfirm, { onPositiveClick: () => del(row.id) },
      { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
        default: () => '确认删除？子部门会提升到根级' })
  ]) }
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

function openEdit(row) {
  editForm.value = { id: row.id, name: row.name, owners: row.owners || '', parentId: row.parentId || null }
  showEdit.value = true
}

async function saveEdit() {
  if (!editForm.value.name) { message.warning('部门名称必填'); return }
  try {
    await api.put(`/departments/${editForm.value.id}`, {
      name: editForm.value.name,
      owners: editForm.value.owners,
      parentId: editForm.value.parentId
    })
    message.success('已保存')
    showEdit.value = false
    load()
  } catch (e) { message.error(e.response?.data?.error || '保存失败') }
}

function openMerge() {
  const first = departments.value.find(d => d.id === checked.value[0])
  mergeTarget.value = first ? first.name : ''
  showMerge.value = true
}

async function doMerge() {
  const target = (mergeTarget.value || '').trim()
  if (!target) { message.warning('请输入目标部门名称'); return }
  const names = checked.value.map(id => (departments.value.find(d => d.id === id) || {}).name).filter(Boolean)
  try {
    await api.post('/departments/merge', { sourceNames: names, targetName: target })
    message.success('已合并')
    showMerge.value = false
    checked.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '合并失败') }
}

async function openSplit() {
  const src = departments.value.find(d => d.id === checked.value[0])
  splitSource.value = src ? src.name : ''
  splitName1.value = splitSource.value + '1'
  splitName2.value = splitSource.value + '2'
  splitOwners1.value = []
  splitOwners2.value = []
  // 负责人直接取部门自己的 owners（不再查资产，避免分页/过滤导致为空）
  const dept = departments.value.find(d => d.name === splitSource.value)
  const owners = (dept && dept.owners ? dept.owners.split(',') : [])
    .map(o => o.trim()).filter(Boolean)
  ownerOpts.value = owners.map(o => ({ label: o, value: o }))
  showSplit.value = true
}

async function doSplit() {
  if (!splitName1.value.trim() || !splitName2.value.trim()) {
    message.warning('请输入两个新部门名称'); return
  }
  try {
    await api.post('/departments/split', {
      sourceName: splitSource.value,
      targets: [
        { name: splitName1.value.trim(), owners: splitOwners1.value },
        { name: splitName2.value.trim(), owners: splitOwners2.value }
      ]
    })
    message.success('已拆分（未分配的负责人资产已清除部门归属）')
    showSplit.value = false
    checked.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '拆分失败') }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: #94a3b8; font-size: 13px; margin: 0 0 12px; }
</style>
