<script setup lang="ts">
// A hash, point, scalar or signature. Shown shortened in the middle unless
// `full`, always copyable whole: the value is the thing people carry from one
// bench to another, and from the lab to a terminal.
import { computed, ref } from 'vue'

const props = withDefaults(defineProps<{ value: string; head?: number; tail?: number; full?: boolean }>(), {
  head: 10,
  tail: 8,
  full: false,
})

const copied = ref(false)
const shown = computed(() => {
  const v = props.value
  if (props.full || v.length <= props.head + props.tail + 1) return v
  return `${v.slice(0, props.head)}…${v.slice(-props.tail)}`
})

async function copy() {
  try {
    await navigator.clipboard.writeText(props.value)
  } catch {
    // An http page that is not localhost has no clipboard; select instead.
    const selection = window.getSelection()
    selection?.removeAllRanges()
    return
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1100)
}
</script>

<template>
  <button type="button" class="hex" :class="{ full, copied }" :title="copied ? 'Copied' : `Copy ${value}`" @click.stop="copy">
    <span class="data">{{ shown }}</span>
    <span class="hex-mark" aria-hidden="true">{{ copied ? 'copied' : 'copy' }}</span>
  </button>
</template>

<style scoped>
.hex {
  font: inherit;
  color: inherit;
  background: none;
  border: 0;
  padding: 0;
  margin: 0;
  cursor: copy;
  text-align: left;
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  max-width: 100%;
  border-radius: 3px;
}
/* Blue on the way to it: a value like this is something to go and look up. */
.hex .data {
  transition: color 150ms var(--ease);
}
.hex:hover .data {
  color: var(--nom-ink);
}
.hex.full .data {
  word-break: break-all;
}
.hex-mark {
  font-family: var(--font-mono);
  font-size: 10px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--dim);
  opacity: 0;
  transition: opacity 120ms;
  flex: none;
}
.hex:hover .hex-mark,
.hex:focus-visible .hex-mark,
.hex.copied .hex-mark {
  opacity: 1;
}
.hex.copied .data {
  animation: flash 1.1s ease-out;
}
.hex.copied .hex-mark {
  color: var(--paid);
}
</style>
