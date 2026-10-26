import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/login', component: () => import('../views/Login.vue') },
  {
    path: '/',
    component: () => import('../layouts/MainLayout.vue'),
    redirect: '/dashboard',
    children: [
      { path: 'dashboard', component: () => import('../views/Dashboard.vue'), meta: { title: '首页' } },
      { path: 'assets', component: () => import('../views/Assets.vue'), meta: { title: '资产总览' } },
      { path: 'disposal', component: () => import('../views/Disposal.vue'), meta: { title: '资产处置' } },
      { path: 'services', component: () => import('../views/Services.vue'), meta: { title: '应用服务' } },
      { path: 'compliance', component: () => import('../views/Compliance.vue'), meta: { title: '合规检测' } },
      { path: 'snapshots', component: () => import('../views/Snapshots.vue'), meta: { title: '页面快照' } },
      { path: 'plans', component: () => import('../views/Plans.vue'), meta: { title: '扫描' } },
      { path: 'nodes', component: () => import('../views/Nodes.vue'), meta: { title: '节点' } },
      { path: 'network', component: () => import('../views/Network.vue'), meta: { title: 'IP 规划' } },
      { path: 'departments', component: () => import('../views/Departments.vue'), meta: { title: '组织结构' } },
      { path: 'faults', component: () => import('../views/Faults.vue'), meta: { title: '故障和日志' } },
      { path: 'notify', component: () => import('../views/Notify.vue'), meta: { title: '通知设置' } },
      { path: 'api-docs', component: () => import('../views/ApiDocs.vue'), meta: { title: 'API 文档' } },
      { path: 'settings', component: () => import('../views/Settings.vue'), meta: { title: '系统配置' } }
    ]
  }
]

const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach((to, from, next) => {
  const token = localStorage.getItem('token')
  if (to.path !== '/login' && !token) next('/login')
  else next()
})

export default router
