<template>
  <div>
    <n-card style="margin-bottom: 16px">
      <template #header>
        <div style="display:flex;align-items:center;gap:12px">
          <span>网段管理（机房）</span>
          <n-button size="small" type="info" @click="openPreview">解析预览</n-button>
          <span class="tip" style="margin:0">展开全部网段，绿色为有效 IP，红色为被免扫描排除</span>
        </div>
      </template>
      <div class="toolbar">
        <n-input v-model:value="segForm.name" placeholder="机房名（可空）" style="width: 180px" />
        <n-input v-model:value="segForm.cidr" placeholder="网段或 IP，如 10.24.1.0/24" style="width: 200px" />
        <n-input v-model:value="segForm.desc" placeholder="描述（可选）" style="width: 200px" />
        <n-button type="primary" @click="addSegment">添加网段</n-button>
        <n-button @click="openImport('seg')">批量导入</n-button>
      </div>
      <n-data-table :columns="segColumns" :data="segments" size="small" />
    </n-card>

    <n-card title="免扫描清单（避开范围）">
      <p class="tip">这些 IP 范围会在扫描时由平台自动排除（如电子设备受不了端口扫描），请务必填写原因。</p>
      <div class="toolbar">
        <n-input v-model:value="excForm.ipRange" placeholder="IP 范围，如 10.24.5.10-20" style="width: 240px" />
        <n-input v-model:value="excForm.reason" placeholder="原因（必填）" style="width: 240px" />
        <n-button type="warning" @click="addExclude">加入免扫描</n-button>
        <n-button @click="openImport('exc')">批量导入</n-button>
      </div>
      <n-data-table :columns="excColumns" :data="excludes" size="small" />
    </n-card>
    <n-modal v-model:show="showPreview" preset="card" title="IP 解析预览" style="width: 960px">
      <div v-if="previewLoading" style="padding:24px;text-align:center;color:#6b7280">解析中…</div>
      <template v-else-if="preview">
        <div class="pv-stats">
          共 <b>{{ preview.total }}</b> 个 IP
          <span class="sep">·</span>
          有效 <b class="ok">{{ preview.kept }}</b>
          <span class="sep">·</span>
          排除 <b class="bad">{{ preview.excluded }}</b>
          <span class="sep">·</span>
          <span class="muted">{{ preview.items.length }} 个输入段</span>
        </div>
        <n-scrollbar style="max-height: 62vh">
          <div v-for="it in preview.items" :key="it.input" class="pv-seg">
            <div class="pv-head">
              <code>{{ it.input }}</code>
              <n-tag size="small" :type="it.failed ? 'error' : 'default'">{{ it.count }} 个</n-tag>
              <n-tag v-if="it.failed" size="small" type="error">未识别</n-tag>
              <span v-if="it.count" class="pv-range">{{ it.first }} ~ {{ it.last }}</span>
            </div>
            <div class="pv-ips">
              <span v-for="ip in it.ips" :key="ip" class="ip ok">{{ ip }}</span>
              <span v-for="ip in it.excludedIps" :key="'x' + ip" class="ip bad">{{ ip }}</span>
              <span v-if="it.count > it.ips.length + it.excludedIps.length" class="ip more">
                …还有 {{ it.count - it.ips.length - it.excludedIps.length }} 个
              </span>
            </div>
          </div>
        </n-scrollbar>
      </template>
    </n-modal>

    <n-modal v-model:show="showEdit" preset="card"
      :title="editType === 'seg' ? '编辑网段' : '编辑免扫描范围'" style="width: 520px">
      <n-form label-placement="left" label-width="80">
        <template v-if="editType === 'seg'">
          <n-form-item label="机房名"><n-input v-model:value="editForm.name" placeholder="可留空，默认「未命名」" /></n-form-item>
          <n-form-item label="网段 / IP"><n-input v-model:value="editForm.cidr" placeholder="10.24.1.0/24 或 10.24.1.1" /></n-form-item>
          <n-form-item label="描述"><n-input v-model:value="editForm.description" /></n-form-item>
        </template>
        <template v-else>
          <n-form-item label="IP 范围"><n-input v-model:value="editForm.ipRange" placeholder="10.24.5.10-20 或 10.24.5.0/24" /></n-form-item>
          <n-form-item label="原因"><n-input v-model:value="editForm.reason" /></n-form-item>
        </template>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showEdit = false">取消</n-button>
          <n-button type="primary" @click="saveEdit">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="showImport" preset="card"
      :title="importType === 'seg' ? '批量导入网段' : '批量导入免扫描范围'" style="width: 680px">
      <p class="tip">{{ importHint }}</p>
      <n-input v-model:value="importText" type="textarea" :rows="12"
        :placeholder="importPlaceholder" style="font-family:monospace" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="showImport = false">取消</n-button>
          <n-button type="primary" :loading="importing" @click="doImport">
            导入（{{ importCount }} 行）
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, computed, onMounted } from 'vue'
import { NButton, NPopconfirm, NTag, useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()
const segments = ref([])
const excludes = ref([])
const segForm = ref({ name: '', cidr: '', desc: '' })
const excForm = ref({ ipRange: '', reason: '' })

/* 解析预览 */
const showPreview = ref(false)
const previewLoading = ref(false)
const preview = ref(null)

async function openPreview() {
  showPreview.value = true
  previewLoading.value = true
  preview.value = null
  try {
    preview.value = await api.post('/targets/preview', {
      targets: segments.value.map(s => s.cidr).filter(Boolean),
      excludes: excludes.value.map(e => e.ipRange).filter(Boolean)
    })
  } catch (e) {
    message.error('解析失败：' + (e.response?.data?.error || e.message))
    showPreview.value = false
  } finally {
    previewLoading.value = false
  }
}

/* 编辑 */
const showEdit = ref(false)
const editType = ref('seg')
const editForm = ref({})

function openEdit(type, row) {
  editType.value = type
  editForm.value = { ...row }
  showEdit.value = true
}

async function saveEdit() {
  try {
    if (editType.value === 'seg') {
      if (!editForm.value.cidr) return message.warning('网段必填')
      await api.put(`/segments/${editForm.value.id}`, {
        name: editForm.value.name || '',
        cidr: editForm.value.cidr,
        description: editForm.value.description || ''
      })
      loadSegments()
    } else {
      if (!editForm.value.ipRange) return message.warning('IP 范围必填')
      await api.put(`/excludes/${editForm.value.id}`, {
        ipRange: editForm.value.ipRange,
        reason: editForm.value.reason || ''
      })
      loadExcludes()
    }
    message.success('已保存')
    showEdit.value = false
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  }
}

/* 批量导入 */
const showImport = ref(false)
const importType = ref('seg')
const importText = ref('')
const importing = ref(false)

const importCount = computed(() =>
  importText.value.split('\n').map(l => l.trim()).filter(Boolean).length)

const importHint = computed(() => importType.value === 'seg'
  ? '每行一条：网段 或 机房名,网段,描述；只有网段必填'
  : '每行一条：IP范围 或 IP范围,原因；原因可省略')

const importPlaceholder = computed(() => importType.value === 'seg'
  ? '10.24.1.0/24\n10.24.2.0/24\n1号机房,10.24.3.0/24,核心区'
  : '10.24.5.10-20\n10.24.6.0/24,生产库\n10.24.7.1,电子设备')

function openImport(type) {
  importType.value = type
  importText.value = ''
  showImport.value = true
}

async function doImport() {
  const lines = importText.value.split('\n').map(l => l.trim()).filter(Boolean)
  if (!lines.length) return message.warning('没有可导入的内容')

  importing.value = true
  let ok = 0, fail = 0
  const errors = []
  const looksLikeIP = x => /\d+\.\d+\.\d+\.\d+/.test(x) || x.includes('/')
  for (const line of lines) {
    const parts = line.split(/[,，\t]/).map(x => x.trim()).filter(Boolean)
    try {
      if (importType.value === 'seg') {
        if (!parts.length) throw new Error('空行')
        let name = '', cidr = '', description = ''
        if (parts.length === 1) {
          cidr = parts[0]
        } else if (looksLikeIP(parts[0])) {
          // 第一列是网段：网段,描述
          cidr = parts[0]; description = parts[1] || ''
        } else {
          // 第一列是机房名：机房名,网段[,描述]
          name = parts[0]; cidr = parts[1] || ''; description = parts[2] || ''
        }
        if (!cidr) throw new Error('缺少网段')
        await api.post('/segments', { name, cidr, description })
      } else {
        if (!parts.length) throw new Error('空行')
        const ipRange = parts[0]
        const reason = parts[1] || '未填写'
        if (!ipRange) throw new Error('缺少 IP 范围')
        await api.post('/excludes', { ipRange, reason })
      }
      ok++
    } catch (e) {
      fail++
      if (errors.length < 3) errors.push(`${line} → ${e.response?.data?.error || e.message}`)
    }
  }
  importing.value = false
  showImport.value = false
  if (ok) message.success(`导入成功 ${ok} 条${fail ? `，失败 ${fail} 条` : ''}`)
  else message.error(`全部失败：${errors[0] || '格式不正确'}`)
  if (fail && ok) console.warn('[批量导入] 失败明细', errors)
  importType.value === 'seg' ? loadSegments() : loadExcludes()
}

const segColumns = [
  { title: '机房名', key: 'name', width: 160 },
  { title: '网段', key: 'cidr', width: 200, render: r => h(NTag, { size: 'small', type: 'info' }, { default: () => r.cidr }) },
  { title: '描述', key: 'description' },
  {
    title: '操作', key: 'actions', width: 130,
    render: row => h('div', { style: 'display:flex;gap:6px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openEdit('seg', row) }, { default: () => '编辑' }),
      h(NPopconfirm, { onPositiveClick: () => delSegment(row.id) },
        { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除该网段？' })
    ])
  }
]

const excColumns = [
  { title: 'IP 范围', key: 'ipRange', width: 240, render: r => h(NTag, { size: 'small', type: 'warning' }, { default: () => r.ipRange }) },
  { title: '原因', key: 'reason', render: r => r.reason || '-' },
  { title: '添加时间', key: 'createdAt', width: 170, render: r => fmt(r.createdAt) },
  {
    title: '操作', key: 'actions', width: 130,
    render: row => h('div', { style: 'display:flex;gap:6px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openEdit('exc', row) }, { default: () => '编辑' }),
      h(NPopconfirm, { onPositiveClick: () => delExclude(row.id) },
        { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除？' })
    ])
  }
]

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function loadSegments() { segments.value = await api.get('/segments') }
async function loadExcludes() { excludes.value = await api.get('/excludes') }

async function addSegment() {
  if (!segForm.value.cidr) { message.warning('网段必填'); return }
  await api.post('/segments', { name: segForm.value.name, cidr: segForm.value.cidr, description: segForm.value.desc })
  message.success('已添加')
  segForm.value = { name: '', cidr: '', desc: '' }
  loadSegments()
}
async function delSegment(id) { await api.delete(`/segments/${id}`); message.success('已删除'); loadSegments() }

async function addExclude() {
  if (!excForm.value.ipRange) { message.warning('IP 范围必填'); return }
  if (!excForm.value.reason) { message.warning('请填写原因'); return }
  await api.post('/excludes', { ipRange: excForm.value.ipRange, reason: excForm.value.reason })
  message.success('已加入免扫描')
  excForm.value = { ipRange: '', reason: '' }
  loadExcludes()
}
async function delExclude(id) { await api.delete(`/excludes/${id}`); message.success('已删除'); loadExcludes() }

onMounted(() => {
  loadSegments()
  loadExcludes()
})
</script>

<style scoped>
.pv-stats { font-size: 13px; margin-bottom: 12px; color: #374151; }
.pv-stats b { font-size: 15px; }
.pv-stats .ok { color: #16a34a; }
.pv-stats .bad { color: #dc2626; }
.pv-stats .sep { margin: 0 8px; color: #d1d5db; }
.pv-seg { margin-bottom: 14px; }
.pv-head { display: flex; align-items: center; gap: 8px; margin-bottom: 5px; }
.pv-head code { font-size: 12.5px; background: #f3f4f6; padding: 1px 6px; border-radius: 3px; }
.pv-range { font-size: 11px; color: #9ca3af; }
.pv-ips { line-height: 2; }
.ip {
  display: inline-block; padding: 0 6px; margin: 2px 3px 2px 0;
  border-radius: 3px; font-size: 11px; font-family: monospace;
}
.ip.ok { background: rgba(34, 197, 94, .12); color: #15803d; }
.ip.bad { background: rgba(239, 68, 68, .12); color: #b91c1c; text-decoration: line-through; }
.ip.more { background: #f3f4f6; color: #6b7280; }

.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: #fbbf24; font-size: 13px; margin: 0 0 12px; }
</style>
