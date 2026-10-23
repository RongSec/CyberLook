<template>
  <n-config-provider :theme="currentTheme" :theme-overrides="themeOverrides" :locale="zhCN" :date-locale="dateZhCN">
    <n-message-provider>
      <n-dialog-provider>
        <router-view v-slot="{ Component }">
          <transition name="page" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup>
import { computed, watch } from 'vue'
import { NConfigProvider, NMessageProvider, NDialogProvider, darkTheme, lightTheme, zhCN, dateZhCN } from 'naive-ui'
import { useThemeStore } from './store/theme'

const themeStore = useThemeStore()
const currentTheme = computed(() => themeStore.theme === 'dark' ? darkTheme : lightTheme)

// 同步主题类名到 <html>，供全局 CSS 变量切换
watch(() => themeStore.theme, (t) => {
  document.documentElement.className = t === 'light' ? 'theme-light' : 'theme-dark'
}, { immediate: true })

const themeOverrides = computed(() => ({
  common: {
    primaryColor: themeStore.theme === 'dark' ? '#f59e0b' : '#d97706',
    primaryColorHover: themeStore.theme === 'dark' ? '#fbbf24' : '#f59e0b',
    primaryColorPressed: themeStore.theme === 'dark' ? '#d97706' : '#b45309',
    primaryColorSuppl: themeStore.theme === 'dark' ? '#f59e0b' : '#d97706',
    borderRadius: '10px',
    fontSize: '14px'
  },
  Card: {
    borderRadius: '16px'
  },
  Button: {
    borderRadiusMedium: '10px',
    borderRadiusLarge: '12px'
  },
  DataTable: {
    borderRadius: '12px'
  }
}))
</script>

<style>
:root,
.theme-dark {
  --bg-deep: #050507;
  --bg-panel: rgba(20, 17, 12, 0.72);
  --bg-side: rgba(10, 9, 7, 0.84);
  --bg-header: rgba(10, 9, 7, 0.62);
  --border-subtle: rgba(245, 158, 11, 0.14);
  --border-glow: rgba(245, 158, 11, 0.38);
  --accent: #f59e0b;
  --accent-2: #fbbf24;
  --text-main: #f2ebdd;
  --text-muted: #a3977f;
  --aurora-opacity: 1;
  --grid-opacity: 0.05;
}

.theme-light {
  --bg-deep: #faf7f2;
  --bg-panel: rgba(255, 255, 255, 0.88);
  --bg-side: rgba(255, 255, 255, 0.93);
  --bg-header: rgba(255, 255, 255, 0.86);
  --border-subtle: rgba(180, 130, 40, 0.16);
  --border-glow: rgba(217, 119, 6, 0.35);
  --accent: #d97706;
  --accent-2: #f59e0b;
  --text-main: #1c1917;
  --text-muted: #78716c;
  --aurora-opacity: 0.5;
  --grid-opacity: 0.035;
}

html, body, #app {
  height: 100%;
  margin: 0;
  padding: 0;
  background: #000;            /* 兜底：页面过渡半透明时也不会露白 */
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif;
}
html.theme-light,
html.theme-light body,
html.theme-light #app {
  background: #faf7f2;
}

/* 页面切换过渡 */
.page-enter-active,
.page-leave-active {
  transition: opacity 0.28s cubic-bezier(0.4, 0, 0.2, 1), transform 0.28s cubic-bezier(0.4, 0, 0.2, 1);
}
.page-enter-from {
  opacity: 0;
  transform: translateY(14px);
}
.page-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}

/* 卡片通用微交互 */
.n-card {
  transition: transform 0.25s cubic-bezier(0.4, 0, 0.2, 1), box-shadow 0.25s cubic-bezier(0.4, 0, 0.2, 1), border-color 0.25s;
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
}
.n-card:hover {
  border-color: var(--border-glow);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.28);
}

/* 数据表格行微交互 */
.n-data-table .n-data-table-tr {
  transition: background 0.2s;
}

/* 滚动条 */
::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
::-webkit-scrollbar-track {
  background: transparent;
}
::-webkit-scrollbar-thumb {
  background: rgba(148, 163, 184, 0.25);
  border-radius: 4px;
}
::-webkit-scrollbar-thumb:hover {
  background: rgba(148, 163, 184, 0.45);
}
</style>
