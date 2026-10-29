<template>
  <div class="spl-wrap">
    <!-- 简单模式：条件胶囊输入框，点胶囊弹出编辑面板 -->
    <div v-if="!advanced" class="spl-simple">
      <div class="tagbox">
        <n-popover v-for="(c, i) in simpleConds" :key="i+'-'+c.field" trigger="click" placement="bottom-start"
          style="max-width: 340px">
          <template #trigger>
            <div class="tag">
              <span class="f">{{ c.field ? labelOf(c.field) : '选择字段' }}</span>
              <span v-if="c.field" class="op">{{ opLabel(c.op) }}</span>
              <span class="v" :class="{ all: !c.value }">{{ displayValue(c) || '全部' }}</span>
              <span class="x" @click.stop="removeSimpleCond(i)">×</span>
            </div>
          </template>
          <div class="edit-panel">
            <div class="row">
              <span class="lbl">字段</span>
              <n-select v-model:value="c.field" :options="fieldOpts" size="small" style="width: 160px"
                @update:value="v => onFieldChange(c, v)" />
            </div>
            <div class="row">
              <span class="lbl">条件</span>
              <n-select v-model:value="c.op" :options="optsFor(c)" size="small" style="width: 160px"
                @update:value="syncSimpleText" />
            </div>
            <div class="row">
              <span class="lbl">值</span>
              <n-select v-if="optionsOf(c.field)" v-model:value="c.value" :options="optionsOf(c.field)"
                :multiple="isMultiple(c.field)" :filterable="isFilterable(c.field)" :tag="isFilterable(c.field)"
                size="small" style="width: 200px" placeholder="请选择" clearable
                @update:value="syncSimpleText" />
              <n-input v-else v-model:value="c.value" size="small" placeholder="输入值"
                style="width: 180px" @update:value="syncSimpleText" @keyup.enter="editIdx = -1" />
            </div>
            <div class="panel-foot">
              <n-button size="tiny" text @click="clearCond(i)">清空</n-button>
              <n-button size="tiny" type="primary" @click="editIdx = -1">完成</n-button>
            </div>
          </div>
        </n-popover>

      </div>
      <div class="acts">
        <n-button type="primary" @click="doSearch">查询</n-button>
        <n-button @click="toggleAdv">高级查询</n-button>
        <n-button quaternary @click="doReset">重置</n-button>
        <span class="hint">{{ simpleText || '点任意条件方块填写筛选值，留「全部」表示不限制' }}</span>
      </div>
    </div>

    <!-- 高级模式（SPL + 积木） -->
    <div v-else class="spl-adv">
      <div class="spl-input">
        <n-input v-model:value="splText" placeholder="请输入 SPL 语句进行检索，或点击下方条件面板拼装"
          @keyup.enter="doSearch" style="flex:1" />
        <n-button @click="parseSpl">解析</n-button>
        <n-button type="primary" @click="doSearch">查询</n-button>
        <n-button @click="toggleAdv">简单查询</n-button>
        <n-button quaternary @click="doReset">重置</n-button>
      </div>
      <div class="panel">
        <div v-for="(g, gi) in groups" :key="gi" class="group">
          <div v-if="gi > 0" class="group-logic">
            <n-select v-model:value="g.logic" :options="logicOpts" size="tiny" style="width: 70px" />
          </div>
          <div class="group-body">
            <div v-for="(c, ci) in g.conds" :key="ci" class="cond-row">
              <n-select v-if="ci > 0" v-model:value="c.logic" :options="logicOpts" size="small" style="width: 70px" />
              <n-select v-model:value="c.field" :options="fieldOpts" size="small" placeholder="字段" style="width: 130px" />
              <n-select v-model:value="c.op" :options="opOpts" size="small" placeholder="操作符" style="width: 110px" />
              <n-input v-model:value="c.value" size="small" placeholder="值" style="flex: 1" />
              <n-button size="small" text type="error" @click="g.conds.splice(ci, 1)">×</n-button>
            </div>
            <div class="group-acts">
              <n-button size="tiny" dashed @click="addCond(gi)">+ 新增条件</n-button>
              <n-button size="tiny" dashed @click="addGroup">+ 新增条件组</n-button>
            </div>
          </div>
        </div>
        <div v-if="groups.length === 0" class="empty">
          <n-button size="small" dashed @click="addFirstCond">+ 新增条件</n-button>
          <n-button size="small" dashed @click="addGroup">+ 新增条件组</n-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'

const props = defineProps({
  fields: { type: Array, required: true },      // [{ key, label, type?, options? }]
  statusOptions: { type: Array, default: () => [] }
})
const emit = defineEmits(['search', 'reset'])

const advanced = ref(false)
const simpleText = ref('')
const editIdx = ref(-1)

// 操作符：固定选项字段只允许 等于/不等于
// 切换字段：固定选项字段自动把操作符纠正为「等于」，并清空不匹配的值
function onFieldChange(c, v) {
  c.field = v
  if (optionsOf(v)) {
    if (c.op !== 'eq' && c.op !== 'neq') c.op = 'eq'
    const opts = optionsOf(v)
    if (c.value && !opts.some(o => String(o.value) === String(c.value))) c.value = ''
  }
  syncSimpleText()
}

function optsFor(c) {
  if (optionsOf(c.field)) {
    return opOpts.filter(o => o.value === 'eq' || o.value === 'neq')
  }
  return opOpts
}

// 取该字段的固定选项（没有则返回 null → 用输入框）
function optionsOf(field) {
  const f = props.fields.find(x => x.key === field)
  return f && f.options && f.options.length ? f.options : null
}

// chip 上显示的文本：有选项时显示 label
function displayValue(c) {
  const v = c.value
  if (v === '' || v === null || v === undefined) return ''
  const opts = optionsOf(c.field)
  const arr = Array.isArray(v) ? v : [v]
  const out = arr.map(x => {
    if (opts) {
      const hit = opts.find(o => String(o.value) === String(x))
      if (hit) return hit.label
    }
    return String(x)
  })
  return out.join(', ')
}

function addBlankCond() {
  const f0 = props.fields[0]?.key
  simpleConds.value.push({ field: f0, op: defaultOp(f0), value: '' })
  editIdx.value = simpleConds.value.length - 1
  syncSimpleText()
}
// 初始就覆盖所有字段：每个字段一个条件方块，默认「全部」，不填值不参与查询
// 有固定选项的字段默认用「等于」，避免出现无意义的「包含」
function defaultOp(field) {
  const f = props.fields.find(x => x.key === field)
  return f && f.options && f.options.length ? 'eq' : 'like'
}
const simpleConds = ref(props.fields.map(f => ({ field: f.key, op: defaultOp(f.key), value: '' })))
const groups = ref([])
const splText = ref('')

const fieldOpts = computed(() => props.fields.map(f => ({ label: f.label, value: f.key })))
const statusOpts = computed(() => props.statusOptions.length
  ? props.statusOptions
  : [{ label: '全部', value: '' }])
const opOpts = [
  { label: '等于', value: 'eq' }, { label: '包含', value: 'like' },
  { label: '不等于', value: 'neq' }, { label: '开头是', value: 'prefix' },
  { label: '大于', value: 'gt' }, { label: '小于', value: 'lt' }
]
const logicOpts = [{ label: '并且', value: 'AND' }, { label: '或者', value: 'OR' }]
const addFieldOpts = computed(() => props.fields
  .filter(f => !simpleConds.value.some(c => c.field === f.key))
  .map(f => ({ label: f.label, value: f.key })))

function labelOf(key) {
  const f = props.fields.find(x => x.key === key)
  return f ? f.label : key
}
function opLabel(op) {
  return (opOpts.find(o => o.value === op) || {}).label || op
}
// 简单条件 → 文本框可读描述
function syncSimpleText() {
  simpleText.value = simpleConds.value
    .filter(c => hasValue(c))
    .map(c => `${labelOf(c.field)} ${opLabel(c.op)} ${displayValue(c)}`)
    .join(' 并且 ')
}

function hasValue(c) {
  const v = c.value
  if (Array.isArray(v)) return v.length > 0
  return v !== '' && v !== null && v !== undefined
}

// 清空条件值（保留条件行，仅把值置空）
function clearCond(i) {
  const c = simpleConds.value[i]
  if (!c) return
  c.value = isMultiple(c.field) ? [] : ''
  editIdx.value = -1
  syncSimpleText()
}

function isMultiple(field) {
  const f = props.fields.find(x => x.key === field)
  return !!(f && f.multiple)
}

function isFilterable(field) {
  const f = props.fields.find(x => x.key === field)
  return !!(f && f.filterable)
}

function removeSimpleCond(i) {
  simpleConds.value.splice(i, 1)
  editIdx.value = -1
  syncSimpleText()
}

// 高级模式：没有条件组时，先建一个含单条件的组
function addFirstCond() {
  const f0 = props.fields[0]?.key
      groups.value = [{ logic: 'AND', conds: [{ field: f0, op: defaultOp(f0), value: '', logic: 'AND' }] }]
}

function addSimpleCond(field) {
  if (simpleConds.value.some(c => c.field === field)) return
  simpleConds.value.push({ field, op: 'like', value: '' })
  syncSimpleText()
}
function addCond(gi) {
  const f0 = props.fields[0]?.key
  groups.value[gi].conds.push({ field: f0, op: defaultOp(f0), value: '', logic: 'AND' })
}
function addGroup() {
  const f0 = props.fields[0]?.key
  groups.value.push({ logic: 'AND', conds: [{ field: f0, op: defaultOp(f0), value: '', logic: 'AND' }] })
}
function toggleAdv() {
  advanced.value = !advanced.value
  if (advanced.value) {
    // 高级模式默认只给 1 行空条件，不继承简单模式的方块
    if (groups.value.length === 0) {
      const f0 = props.fields[0]?.key
      groups.value = [{ logic: 'AND', conds: [{ field: f0, op: defaultOp(f0), value: '', logic: 'AND' }] }]
    }
    syncSpl()
  } else {
    syncSimpleText()
  }
}

// 积木 → SPL 文本
function syncSpl() {
  const parts = []
  groups.value.forEach((g, gi) => {
    const inner = g.conds
      .filter(c => c.value !== '' && c.value !== null && c.value !== undefined)
      .map((c, ci) => `${c.field} ${c.op} "${c.value}"`)
      .join(' ' + (g.logic || 'AND') + ' ')
    if (inner) parts.push(gi > 0 ? `(${inner})` : inner)
  })
  splText.value = parts.join(' AND ')
}

watch(groups, syncSpl, { deep: true })

// SPL 文本 → 积木（简单解析：field op "value" AND/OR ...）
function parseSpl() {
  const txt = (splText.value || '').trim()
  if (!txt) { groups.value = []; return }
  const parts = txt.split(/\s+(AND|OR)\s+/i)
  const conds = []
  let logic = 'AND'
  for (const p of parts) {
    const t = p.trim()
    if (/^(AND|OR)$/i.test(t)) { logic = t.toUpperCase(); continue }
    const m = t.replace(/[()]/g, '').match(/^(\w+)\s+(eq|neq|like|gt|lt|prefix)\s+"?([^"]*)"?$/i)
    if (m) conds.push({ field: m[1], op: m[2].toLowerCase(), value: m[3], logic })
  }
  groups.value = conds.length ? [{ logic: 'AND', conds }] : []
}

function buildConds() {
  const out = []
  if (!advanced.value) {
    simpleConds.value.forEach((c, i) => {
      if (hasValue(c)) {
        const v = Array.isArray(c.value) ? c.value.slice() : String(c.value)
        out.push({ field: c.field, op: c.op, value: v, logic: i === 0 ? 'AND' : 'AND' })
      }
    })
  } else {
    groups.value.forEach((g, gi) => {
      g.conds.forEach((c, ci) => {
        if (c.value !== '' && c.value !== null && c.value !== undefined) {
          out.push({
            field: c.field, op: c.op, value: String(c.value),
            logic: ci === 0 ? (gi === 0 ? 'AND' : g.logic) : c.logic
          })
        }
      })
    })
  }
  return out
}

function doSearch() {
  if (advanced.value) syncSpl()
  emit('search', { conditions: buildConds(), spl: splText.value })
}
function doReset() {
  simpleConds.value = []
  groups.value = []
  splText.value = ''
  emit('reset')
}

// 暴露给父组件读取条件
defineExpose({ buildConds, splText, advanced })
</script>

<style scoped>
.spl-wrap { margin-bottom: 12px; }
.spl-simple { display: flex; gap: 10px; align-items: flex-start; flex-wrap: wrap; }
.spl-simple .tagbox { flex: 1; min-width: 420px; }

.tagbox {
  display: flex; flex-wrap: wrap; gap: 8px; align-items: center;
  min-height: 38px; padding: 6px 8px;
  border: 1px solid var(--n-border-color, rgba(128,128,128,.3));
  border-radius: 6px;
  background: var(--n-color, transparent);
}
.tag {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 4px 9px; border-radius: 5px; font-size: 12px; cursor: pointer;
  background: var(--n-action-color, rgba(128,128,128,.14));
  border: 1px solid var(--n-border-color, rgba(128,128,128,.3));
  user-select: none;
}
.tag:hover { border-color: var(--n-color-target, #18a058); }
.tag .v.all { opacity: .45; font-weight: 400; }
.tag .f { font-weight: 500; }
.tag .op { opacity: .6; }
.tag .v { color: var(--n-color-target, #18a058); font-weight: 500; }
.tag .x { opacity: .45; margin-left: 2px; }
.tag .x:hover { color: #e88080; opacity: 1; }
.addtag {
  padding: 4px 10px; border-radius: 5px; font-size: 12px; cursor: pointer;
  border: 1px dashed var(--n-border-color, rgba(128,128,128,.45)); opacity: .8;
}
.addtag:hover { border-color: #18a058; color: #18a058; opacity: 1; }

.edit-panel { width: 260px; }
.edit-panel .row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.edit-panel .lbl { width: 34px; font-size: 12px; opacity: .7; }
.panel-foot { display: flex; justify-content: space-between; margin-top: 4px; }

.acts { display: flex; gap: 8px; align-items: center; flex-wrap: nowrap; white-space: nowrap; }
.hint { display: none; }
.spl-adv .spl-input { display: flex; gap: 8px; }
.spl-adv .panel { margin-top: 10px; padding: 12px; border: 1px dashed var(--n-border-color, rgba(128,128,128,.3)); border-radius: 6px; background: transparent; }
.spl-adv .group { display: flex; gap: 10px; margin-bottom: 10px; }
.spl-adv .group-logic { padding-top: 4px; }
.spl-adv .group-body { flex: 1; border-left: 2px solid var(--n-border-color, rgba(128,128,128,.3)); padding-left: 10px; }
.spl-adv .cond-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.spl-adv .group-acts { display: flex; gap: 8px; }
.spl-adv .empty { padding: 8px; display: flex; gap: 8px; }
.spl-adv .spl-preview { opacity: .55; font-size: 12px; font-family: monospace; }
</style>
