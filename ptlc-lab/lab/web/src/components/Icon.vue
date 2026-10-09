<script setup lang="ts">
// Lucide, inlined. The design system calls for Lucide everywhere and no
// emoji, and the lab ships in a container with no route to a CDN, so it
// carries the handful of paths it uses rather than the package.
import { computed } from 'vue'

const PATHS = {
  sun: ['M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10Z', 'M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4'],
  moon: ['M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z'],
  arrow: ['M5 12h14', 'm12 5 7 7-7 7'],
  down: ['m6 9 6 6 6-6'],
  copy: ['M20 8H10a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V10a2 2 0 0 0-2-2Z', 'M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2'],
  check: ['M20 6 9 17l-5-5'],
  play: ['M6 3.5v17l14-8.5-14-8.5Z'],
  square: ['M6 6h12v12H6Z'],
  lock: ['M5 11h14a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4'],
  alert: ['M12 9v4', 'M10.3 3.9 2.2 18a2 2 0 0 0 1.7 3h16.2a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z', 'M12 17h.01'],
} as const

const props = withDefaults(defineProps<{ name: keyof typeof PATHS; size?: number; stroke?: number }>(), {
  size: 16,
  stroke: 2,
})
const paths = computed(() => PATHS[props.name])
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" :stroke-width="stroke" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path v-for="(d, i) in paths" :key="i" :d="d" />
  </svg>
</template>
