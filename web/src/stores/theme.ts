import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { darkTheme, type GlobalThemeOverrides } from 'naive-ui'

const THEME_KEY = 'emby_lite_theme'
const SIDEBAR_KEY = 'emby_lite_sidebar_collapsed'

export const useThemeStore = defineStore('theme', () => {
  const saved = localStorage.getItem(THEME_KEY)
  // Default to light glacial glass theme!
  const mode = ref<'light' | 'dark'>(saved === 'dark' ? 'dark' : 'light')
  const sidebarCollapsed = ref<boolean>(localStorage.getItem(SIDEBAR_KEY) === 'true')

  const isDark = computed(() => mode.value === 'dark')

  function applyDomClass() {
    const root = document.documentElement
    if (mode.value === 'dark') {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
  }

  function setMode(next: 'light' | 'dark') {
    mode.value = next
    localStorage.setItem(THEME_KEY, next)
    applyDomClass()
  }

  function toggleMode() {
    setMode(mode.value === 'dark' ? 'light' : 'dark')
  }

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
    localStorage.setItem(SIDEBAR_KEY, String(sidebarCollapsed.value))
  }

  const naiveTheme = computed(() => (isDark.value ? darkTheme : null))

  const naiveThemeOverrides = computed<GlobalThemeOverrides>(() => {
    if (isDark.value) {
      return {
        common: {
          primaryColor: '#38bdf8',
          primaryColorHover: '#7dd3fc',
          primaryColorPressed: '#0284c7',
          primaryColorSuppl: '#38bdf8',
          infoColor: '#818cf8',
          successColor: '#34d399',
          warningColor: '#fbbf24',
          errorColor: '#fb7185',
          borderRadius: '12px',
          bodyColor: '#0b1120',
          cardColor: 'rgba(20, 29, 48, 0.78)',
          modalColor: 'rgba(18, 26, 44, 0.9)',
          popoverColor: 'rgba(22, 33, 54, 0.94)',
          inputColor: 'rgba(15, 23, 42, 0.72)',
          borderColor: 'rgba(255, 255, 255, 0.12)',
        },
        Card: {
          borderColor: 'rgba(255, 255, 255, 0.1)',
          borderRadius: '16px',
        },
        Button: {
          borderRadiusMedium: '11px',
          borderRadiusSmall: '9px',
          borderRadiusTiny: '8px',
        },
      }
    }
    return {
      common: {
        primaryColor: '#0284c7',
        primaryColorHover: '#0ea5e9',
        primaryColorPressed: '#0369a1',
        primaryColorSuppl: '#0284c7',
        infoColor: '#4f46e5',
        successColor: '#059669',
        warningColor: '#d97706',
        errorColor: '#e11d48',
        borderRadius: '12px',
        bodyColor: '#eef4fb',
        cardColor: 'rgba(255, 255, 255, 0.74)',
        modalColor: 'rgba(255, 255, 255, 0.88)',
        popoverColor: 'rgba(255, 255, 255, 0.94)',
        inputColor: 'rgba(255, 255, 255, 0.76)',
        inputColorDisabled: 'rgba(226, 232, 240, 0.55)',
        borderColor: 'rgba(148, 163, 184, 0.28)',
        textColorBase: '#0f172a',
        textColor1: '#0f172a',
        textColor2: '#334155',
        textColor3: '#64748b',
      },
      Card: {
        borderColor: 'rgba(255, 255, 255, 0.88)',
        borderRadius: '16px',
        titleTextColor: '#0f172a',
      },
      Button: {
        borderRadiusMedium: '11px',
        borderRadiusSmall: '9px',
        borderRadiusTiny: '8px',
      },
      Tag: {
        borderRadius: '8px',
      },
    }
  })

  applyDomClass()

  return {
    mode,
    isDark,
    sidebarCollapsed,
    naiveTheme,
    naiveThemeOverrides,
    setMode,
    toggleMode,
    toggleSidebar,
    applyDomClass,
  }
})
