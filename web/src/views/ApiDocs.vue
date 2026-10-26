<template>
  <div class="dash">
    <n-card title="API 文档" size="small">
      <p class="tip">
        所有接口前缀 <code>/api</code>，除「公开接口」外均需在请求头带
        <code>Authorization: Bearer &lt;token&gt;</code>。
        Token 由 <code>POST /api/auth/login</code> 获取，有效期 24 小时。
      </p>

      <n-tabs type="line" animated>
        <n-tab-pane v-for="group in groups" :key="group.name" :name="group.name" :tab="group.name">
          <n-data-table :columns="cols" :data="group.items" size="small" :bordered="false" />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </div>
</template>

<script setup>
import { h } from 'vue'
import { NTag } from 'naive-ui'

const methodType = { GET: 'info', POST: 'success', PUT: 'warning', DELETE: 'error' }

const cols = [
  { title: '方法', key: 'method', width: 80, render: r => h(NTag, { size: 'small', type: methodType[r.method] || 'default' }, { default: () => r.method }) },
  { title: '路径', key: 'path', width: 320, render: r => h('code', r.path) },
  { title: '说明', key: 'desc' },
  { title: '鉴权', key: 'auth', width: 90, render: r => h(NTag, { size: 'small', type: r.auth === '公开' ? 'default' : 'warning' }, { default: () => r.auth }) }
]

const groups = [
  {
    name: '分片',
    items: [
      { method: 'POST', path: '/agent-tasks/:id/cancel', desc: '创建/执行:id/cancel', auth: '需登录' },
      { method: 'POST', path: '/agent-tasks/:id/stop', desc: '创建/执行:id/stop', auth: '需登录' }
    ]
  },
  {
    name: 'api',
    items: [
      { method: 'GET', path: '/api/agent/nodes/:id/tasks', desc: '查询agent/nodes/:id/tasks', auth: '需登录' },
      { method: 'GET', path: '/api/agent/stream/:id', desc: '查询agent/stream/:id', auth: '需登录' },
      { method: 'GET', path: '/api/agent/tasks/:id', desc: '查询agent/tasks/:id', auth: '需登录' },
      { method: 'POST', path: '/api/agent/tasks/:id/finish', desc: '创建/执行agent/tasks/:id/finish', auth: '需登录' },
      { method: 'POST', path: '/api/agent/tasks/:id/progress', desc: '创建/执行agent/tasks/:id/progress', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/:id/heartbeat', desc: '创建/执行nodes/:id/heartbeat', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/:id/results', desc: '创建/执行nodes/:id/results', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/:id/tasks', desc: '查询nodes/:id/tasks', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/agent-binary', desc: '查询nodes/agent-binary', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/install.ps1', desc: '查询nodes/install.ps1', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/install.sh', desc: '查询nodes/install.sh', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/register', desc: '创建/执行nodes/register', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/tscan-binary', desc: '查询nodes/tscan-binary', auth: '需登录' }
    ]
  },
  {
    name: '资产',
    items: [
      { method: 'GET', path: '/assets', desc: '资产列表，支持 keyword/status/page/size', auth: '需登录' },
      { method: 'PUT', path: '/assets/:id/confirm', desc: '更新:id/confirm', auth: '需登录' },
      { method: 'PUT', path: '/assets/:id/owner', desc: '更新:id/owner', auth: '需登录' },
      { method: 'GET', path: '/assets/:ip/ports', desc: '查询:ip/ports', auth: '需登录' },
      { method: 'GET', path: '/assets/:ip/snapshots', desc: '查询:ip/snapshots', auth: '需登录' },
      { method: 'POST', path: '/assets/batch-delete', desc: '创建/执行batch-delete', auth: '需登录' },
      { method: 'POST', path: '/assets/import', desc: '创建/执行import', auth: '需登录' },
      { method: 'GET', path: '/assets/template', desc: '查询template', auth: '需登录' }
    ]
  },
  {
    name: '认证',
    items: [
      { method: 'GET', path: '/auth/me', desc: '当前用户信息', auth: '需登录' },
      { method: 'POST', path: '/auth/password', desc: '修改密码', auth: '需登录' }
    ]
  },
  {
    name: '合规检测',
    items: [
      { method: 'POST', path: '/compliance/check', desc: '创建/执行check', auth: '需登录' },
      { method: 'GET', path: '/compliance/results', desc: '查询results', auth: '需登录' },
      { method: 'DELETE', path: '/compliance/results/:id', desc: '删除results/:id', auth: '需登录' },
      { method: 'POST', path: '/compliance/results/batch-delete', desc: '创建/执行results/batch-delete', auth: '需登录' },
      { method: 'GET', path: '/compliance/rules', desc: '查询rules', auth: '需登录' },
      { method: 'POST', path: '/compliance/rules', desc: '创建/执行rules', auth: '需登录' },
      { method: 'DELETE', path: '/compliance/rules/:id', desc: '删除rules/:id', auth: '需登录' }
    ]
  },
  {
    name: '首页概览',
    items: [
      { method: 'GET', path: '/dashboard/summary', desc: '首页概览统计', auth: '需登录' },
      { method: 'GET', path: '/dashboard/trend', desc: '查询trend', auth: '需登录' }
    ]
  },
  {
    name: '部门',
    items: [
      { method: 'GET', path: '/departments', desc: '查询departments', auth: '需登录' },
      { method: 'POST', path: '/departments', desc: '创建/执行departments', auth: '需登录' },
      { method: 'DELETE', path: '/departments/:id', desc: '删除:id', auth: '需登录' }
    ]
  },
  {
    name: '资产处置',
    items: [
      { method: 'GET', path: '/disposal', desc: '查询disposal', auth: '需登录' },
      { method: 'DELETE', path: '/disposal/:ip', desc: '删除:ip', auth: '需登录' },
      { method: 'POST', path: '/disposal/batch-delete', desc: '创建/执行batch-delete', auth: '需登录' },
      { method: 'POST', path: '/disposal/confirm-ledger', desc: '创建/执行confirm-ledger', auth: '需登录' },
      { method: 'GET', path: '/disposal/export', desc: '查询export', auth: '需登录' }
    ]
  },
  {
    name: '排除目标',
    items: [
      { method: 'POST', path: '/excludes', desc: '创建/执行excludes', auth: '需登录' },
      { method: 'DELETE', path: '/excludes/:id', desc: '删除:id', auth: '需登录' },
      { method: 'PUT', path: '/excludes/:id', desc: '更新:id', auth: '需登录' }
    ]
  },
  {
    name: '导出',
    items: [
      { method: 'POST', path: '/export/excel', desc: '创建/执行excel', auth: '需登录' }
    ]
  },
  {
    name: '故障',
    items: [
      { method: 'GET', path: '/faults', desc: '故障列表，支持 kind 筛选', auth: '需登录' }
    ]
  },
  {
    name: '节点',
    items: [
      { method: 'GET', path: '/nodes', desc: '节点列表', auth: '需登录' },
      { method: 'DELETE', path: '/nodes/:id', desc: '删除:id', auth: '需登录' },
      { method: 'PUT', path: '/nodes/:id/config', desc: '更新:id/config', auth: '需登录' },
      { method: 'POST', path: '/nodes/:id/health', desc: '创建/执行:id/health', auth: '需登录' },
      { method: 'GET', path: '/nodes/:id/projects', desc: '查询:id/projects', auth: '需登录' },
      { method: 'POST', path: '/nodes/:id/projects/delete', desc: '创建/执行:id/projects/delete', auth: '需登录' },
      { method: 'GET', path: '/nodes/:id/terminal', desc: '查询:id/terminal', auth: '需登录' },
      { method: 'POST', path: '/nodes/:id/upgrade', desc: '创建/执行:id/upgrade', auth: '需登录' },
      { method: 'GET', path: '/nodes/agent-version', desc: '查询agent-version', auth: '需登录' },
      { method: 'POST', path: '/nodes/join-token', desc: '生成一次性加入令牌', auth: '需登录' }
    ]
  },
  {
    name: '通知',
    items: [
      { method: 'GET', path: '/notify/settings', desc: '通知配置', auth: '需登录' },
      { method: 'POST', path: '/notify/settings', desc: '创建/执行settings', auth: '需登录' },
      { method: 'POST', path: '/notify/test', desc: '发送测试通知，?channel=xxx', auth: '需登录' }
    ]
  },
  {
    name: '网段',
    items: [
      { method: 'GET', path: '/segments', desc: '查询segments', auth: '需登录' },
      { method: 'POST', path: '/segments', desc: '创建/执行segments', auth: '需登录' },
      { method: 'DELETE', path: '/segments/:id', desc: '删除:id', auth: '需登录' },
      { method: 'PUT', path: '/segments/:id', desc: '更新:id', auth: '需登录' }
    ]
  },
  {
    name: '应用服务',
    items: [
      { method: 'GET', path: '/services', desc: '查询services', auth: '需登录' },
      { method: 'DELETE', path: '/services/:id', desc: '删除:id', auth: '需登录' },
      { method: 'POST', path: '/services/batch-delete', desc: '创建/执行batch-delete', auth: '需登录' }
    ]
  },
  {
    name: '页面快照',
    items: [
      { method: 'GET', path: '/snapshots', desc: '查询snapshots', auth: '需登录' },
      { method: 'GET', path: '/snapshots/:id/image', desc: '查询:id/image', auth: '需登录' },
      { method: 'POST', path: '/snapshots/batch-delete', desc: '创建/执行batch-delete', auth: '需登录' },
      { method: 'POST', path: '/snapshots/capture', desc: '创建/执行capture', auth: '需登录' },
      { method: 'GET', path: '/snapshots/settings', desc: '查询settings', auth: '需登录' },
      { method: 'POST', path: '/snapshots/settings', desc: '创建/执行settings', auth: '需登录' }
    ]
  },
  {
    name: '系统',
    items: [
      { method: 'GET', path: '/system/fuse', desc: '读取熔断规则', auth: '需登录' },
      { method: 'POST', path: '/system/fuse', desc: '保存熔断规则', auth: '需登录' },
      { method: 'GET', path: '/system/logs', desc: '系统日志（故障+执行记录）', auth: '需登录' },
      { method: 'GET', path: '/system/settings', desc: '查询settings', auth: '需登录' },
      { method: 'POST', path: '/system/settings', desc: '创建/执行settings', auth: '需登录' },
      { method: 'GET', path: '/system/stats', desc: '查询stats', auth: '需登录' },
      { method: 'POST', path: '/system/update/apply', desc: '创建/执行update/apply', auth: '需登录' },
      { method: 'GET', path: '/system/update/check', desc: '查询update/check', auth: '需登录' },
      { method: 'POST', path: '/system/update/rollback', desc: '创建/执行update/rollback', auth: '需登录' },
      { method: 'GET', path: '/system/version', desc: '查询version', auth: '需登录' }
    ]
  },
  {
    name: '目标',
    items: [
      { method: 'POST', path: '/targets/preview', desc: '创建/执行preview', auth: '需登录' }
    ]
  },
  {
    name: '扫描任务',
    items: [
      { method: 'GET', path: '/tasks', desc: '扫描任务列表（含进度、概览）', auth: '需登录' },
      { method: 'POST', path: '/tasks', desc: '创建扫描任务', auth: '需登录' },
      { method: 'DELETE', path: '/tasks/:id', desc: '删除:id', auth: '需登录' },
      { method: 'PUT', path: '/tasks/:id', desc: '更新:id', auth: '需登录' },
      { method: 'GET', path: '/tasks/:id/agent-tasks', desc: '查询:id/agent-tasks', auth: '需登录' },
      { method: 'POST', path: '/tasks/:id/dispatch', desc: '创建/执行:id/dispatch', auth: '需登录' },
      { method: 'GET', path: '/tasks/:id/log', desc: '查询:id/log', auth: '需登录' },
      { method: 'GET', path: '/tasks/:id/overview', desc: '任务概览：IP/存活/应用/违规', auth: '需登录' },
      { method: 'POST', path: '/tasks/:id/run', desc: '立即执行任务', auth: '需登录' },
      { method: 'GET', path: '/tasks/:id/runs', desc: '任务执行历史', auth: '需登录' },
      { method: 'POST', path: '/tasks/:id/stop', desc: '停止任务', auth: '需登录' },
      { method: 'POST', path: '/tasks/batch-delete', desc: '创建/执行batch-delete', auth: '需登录' },
      { method: 'GET', path: '/tasks/scheduled', desc: '定时任务列表', auth: '需登录' }
    ]
  },
  {
    name: '扫描模板',
    items: [
      { method: 'GET', path: '/templates', desc: '查询templates', auth: '需登录' },
      { method: 'POST', path: '/templates', desc: '创建/执行templates', auth: '需登录' },
      { method: 'DELETE', path: '/templates/:id', desc: '删除:id', auth: '需登录' }
    ]
  }
]
</script>

<style scoped>
.dash { animation: rise 0.4s cubic-bezier(0.4, 0, 0.2, 1); }
@keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }
.tip { font-size: 13px; color: var(--text-muted); margin: 0 0 16px; line-height: 1.8; }
.tip code { background: var(--bg-hover); padding: 1px 6px; border-radius: 4px; font-size: 12px; }
</style>
