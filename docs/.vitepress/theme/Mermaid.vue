<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { mermaidConfig, mermaidThemeVariables } from '../mermaid'

const props = defineProps<{ id: string; graph: string }>()
const svg = ref('')
let observer: MutationObserver | undefined
let renders = 0
let renderedDark: boolean | undefined

async function render() {
  const isDark = document.documentElement.classList.contains('dark')
  if (isDark === renderedDark) return
  renderedDark = isDark
  const current = ++renders
  const [{ default: mermaid }] = await Promise.all([import('mermaid'), document.fonts.ready])
  mermaid.initialize({ ...mermaidConfig, themeVariables: mermaidThemeVariables(isDark) })
  const result = await mermaid.render(`${props.id}-${current}`, decodeURIComponent(props.graph))
  if (current === renders) svg.value = result.svg
}

onMounted(() => {
  observer = new MutationObserver(render)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  render()
})

onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div v-html="svg" />
</template>
