import { ref, watchEffect } from 'vue'

const KEY = 'verdict.theme'

function initial() {
  try {
    return localStorage.getItem(KEY) || 'dark'
  } catch {
    return 'dark'
  }
}

export const theme = ref(initial())

watchEffect(() => {
  document.documentElement.dataset.theme = theme.value
  try {
    localStorage.setItem(KEY, theme.value)
  } catch {
    /* 存不了就只在本次会话生效 */
  }
})

export function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
}
