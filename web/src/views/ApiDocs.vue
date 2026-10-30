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
      { method: 'GET', path: '/api/agent/nodes/:id/tasks', desc: '查询节点任务列表（agent 专用）', auth: '需登录' },
      { method: 'GET', path: '/api/agent/stream/:id', desc: '任务执行流式输出（agent 专用）', auth: '需登录' },
      { method: 'GET', path: '/api/agent/tasks/:id', desc: '查询任务详情（agent 专用）', auth: '需登录' },
      { method: 'POST', path: '/api/agent/tasks/:id/finish', desc: '上报任务完成（agent 专用）', auth: '需登录' },
      { method: 'POST', path: '/api/agent/tasks/:id/progress', desc: '上报任务进度（agent 专用）', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/:id/heartbeat', desc: '节点心跳（版本不一致时自动触发升级）', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/:id/results', desc: '上报扫描结果（agent 专用）', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/:id/tasks', desc: '查询节点任务（agent 专用）', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/agent-binary', desc: '下载控制文件二进制（按架构）', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/install.ps1', desc: 'Windows 安装脚本', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/install.sh', desc: 'Linux 安装脚本', auth: '需登录' },
      { method: 'POST', path: '/api/nodes/register', desc: '节点注册', auth: '需登录' },
      { method: 'GET', path: '/api/nodes/tscan-binary', desc: '下载 TscanClient 扫描引擎', auth: '需登录' }
    ]
  },
  {
    name: '资产',
    items: [
      { method: 'GET', path: '/assets', desc: '资产列表，支持 keyword/status/page/size', auth: '需登录' },
      { method: 'PUT', path: '/assets/:id/confirm', desc: '标注归属（系统名/负责人/部门）', auth: '需登录' },
      { method: 'PUT', path: '/assets/:id/owner', desc: '修改资产负责人/部门', auth: '需登录' },
      { method: 'GET', path: '/assets/:ip/ports', desc: '查询资产端口列表', auth: '需登录' },
      { method: 'GET', path: '/assets/:ip/snapshots', desc: '查询资产快照', auth: '需登录' },
      { method: 'POST', path: '/assets/batch-delete', desc: '批量删除资产', auth: '需登录' },
      { method: 'POST', path: '/assets/import', desc: '导入台账 Excel（xlsx，已确认资产不覆盖）', auth: '需登录' },
      { method: 'GET', path: '/assets/template', desc: '下载台账导入模板', auth: '需登录' },
      { method: 'GET', path: '/assets/import-conflicts', desc: '列出重复导入冲突（已确认资产被改）', auth: '需登录' },
      { method: 'POST', path: '/assets/import-conflicts/:id/resolve', desc: '处理单条冲突（按旧/按新+编辑）', auth: '需登录' },
      { method: 'POST', path: '/assets/import-conflicts/resolve-all', desc: '批量处理冲突（全部按旧/按新）', auth: '需登录' }
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
      { method: 'GET', path: '/dashboard/summary', desc: '首页概览统计（Web资产/已登记/待确认/导入/新存活等）', auth: '需登录' },
      { method: 'GET', path: '/dashboard/trend', desc: '首页趋势图数据（7d/30d）', auth: '需登录' }
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
      { method: 'POST', path: '/disposal/batch-delete', desc: '批量删除资产', auth: '需登录' },
      { method: 'POST', path: '/disposal/confirm-ledger', desc: '创建/执行confirm-ledger', auth: '需登录' },
      { method: 'GET', path: '/disposal/export', desc: '导出资产处置 Excel', auth: '需登录' }
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
      { method: 'POST', path: '/export/excel', desc: '导出 Excel（按类型+筛选条件+列）', auth: '需登录' }
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
      { method: 'POST', path: '/notify/settings', desc: '保存系统设置', auth: '需登录' },
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
      { method: 'POST', path: '/services/batch-delete', desc: '批量删除资产', auth: '需登录' }
    ]
  },
  {
    name: '页面快照',
    items: [
      { method: 'GET', path: '/snapshots', desc: '查询snapshots', auth: '需登录' },
      { method: 'GET', path: '/snapshots/:id/image', desc: '查询:id/image', auth: '需登录' },
      { method: 'POST', path: '/snapshots/batch-delete', desc: '批量删除资产', auth: '需登录' },
      { method: 'POST', path: '/snapshots/capture', desc: '创建/执行capture', auth: '需登录' },
      { method: 'GET', path: '/snapshots/settings', desc: '读取系统设置（GitHub仓库/代理/端口）', auth: '需登录' },
      { method: 'POST', path: '/snapshots/settings', desc: '保存系统设置', auth: '需登录' }
    ]
  },
  {
    name: '系统',
    items: [
      { method: 'GET', path: '/system/fuse', desc: '读取扫描熔断规则', auth: '需登录' },
      { method: 'POST', path: '/system/fuse', desc: '保存扫描熔断规则', auth: '需登录' },
      { method: 'GET', path: '/system/logs', desc: '系统日志（故障+执行记录）', auth: '需登录' },
      { method: 'GET', path: '/system/settings', desc: '读取系统设置（GitHub仓库/代理/端口）', auth: '需登录' },
      { method: 'POST', path: '/system/settings', desc: '保存系统设置', auth: '需登录' },
      { method: 'GET', path: '/system/stats', desc: '系统运行统计', auth: '需登录' },
      { method: 'POST', path: '/system/update/apply', desc: '应用更新（交给 updater 边车拉镜像重启）', auth: '需登录' },
      { method: 'GET', path: '/system/update/check', desc: '检查 GitHub Release 新版本（返回 hasUpdate/version/current）', auth: '需登录' },
      { method: 'POST', path: '/system/update/rollback', desc: '回滚到最近一次备份的二进制', auth: '需登录' },
      { method: 'GET', path: '/system/version', desc: '获取服务端版本号', auth: '需登录' }
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
      { method: 'POST', path: '/tasks/batch-delete', desc: '批量删除资产', auth: '需登录' },
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
