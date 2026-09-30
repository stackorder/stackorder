<script setup lang="ts">
import { withBase } from 'vitepress'

const props = defineProps<{
  name: string
  alt: string
  width: number
  height: number
  caption?: string
}>()

const schemes = ['light', 'dark'] as const

const src = (scheme: (typeof schemes)[number]) => withBase(`/screenshots/${props.name}-${scheme}.png`)
</script>

<template>
  <figure class="screenshot">
    <a
      v-for="scheme in schemes"
      :key="scheme"
      :class="`screenshot__${scheme}`"
      :href="src(scheme)"
      title="Open the full-size screenshot"
    >
      <img :src="src(scheme)" :alt="alt" :width="width" :height="height" loading="lazy" decoding="async" />
    </a>
    <figcaption v-if="caption">{{ caption }}</figcaption>
  </figure>
</template>

<style scoped>
.screenshot {
  margin: 20px 0;
}

.screenshot a {
  display: block;
  line-height: 0;
}

.screenshot img {
  width: auto;
  max-width: 100%;
  height: auto;
  border: 1px solid var(--vp-c-divider);
  border-radius: 8px;
}

.screenshot figcaption {
  margin-top: 8px;
  font-size: 14px;
  line-height: 1.5;
  color: var(--vp-c-text-2);
}

.dark .screenshot__light,
html:not(.dark) .screenshot__dark {
  display: none;
}
</style>
