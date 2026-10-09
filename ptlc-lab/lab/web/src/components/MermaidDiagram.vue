<script setup lang="ts">
// A Mermaid diagram. The library is most of a megabyte, and only the
// benches that draw a protocol need it, so it is loaded when one of them opens.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps<{ code: string; label: string }>()
const host = ref<HTMLElement | null>(null)
const failed = ref('')
let counter = 0

// Mermaid takes colours as values, not as custom properties, so the page's
// tokens are read off the document when a diagram is drawn, and the diagram
// is drawn again when the theme is switched.
function tokens(): Record<string, string> {
  const style = getComputedStyle(document.documentElement)
  const t = (name: string) => style.getPropertyValue(name).trim()
  return {
    fontSize: '13px',
    background: t('--surface'),
    primaryColor: t('--raised'),
    primaryTextColor: t('--fg'),
    primaryBorderColor: t('--line-strong'),
    lineColor: t('--dim'),
    secondaryColor: t('--sunk'),
    tertiaryColor: t('--surface'),
    edgeLabelBackground: t('--surface'),
    // A note is a wait or a precondition: time, so amber.
    noteBkgColor: t('--raised'),
    noteBorderColor: t('--warn'),
    noteTextColor: t('--fg'),
    actorBkg: t('--raised'),
    actorTextColor: t('--fg'),
    actorBorder: t('--znn-ink'),
    actorLineColor: t('--line-strong'),
    signalColor: t('--soft'),
    signalTextColor: t('--fg'),
    labelBoxBkgColor: t('--sunk'),
    labelBoxBorderColor: t('--line-strong'),
    labelTextColor: t('--soft'),
    loopTextColor: t('--soft'),
    sequenceNumberColor: t('--bg'),
    activationBkgColor: t('--raised'),
    activationBorderColor: t('--line-strong'),
  }
}

async function draw() {
  if (!host.value) return
  failed.value = ''
  try {
    const { default: mermaid } = await import('mermaid')
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      theme: 'base',
      fontFamily: "'Space Grotesk Variable', system-ui, sans-serif",
      themeVariables: tokens(),
      sequence: { useMaxWidth: true, mirrorActors: false, messageMargin: 30, boxMargin: 8 },
    })
    const { svg } = await mermaid.render(`seq-${Date.now()}-${++counter}`, props.code)
    host.value.innerHTML = svg
  } catch (err) {
    failed.value = err instanceof Error ? err.message : String(err)
  }
}

const themeWatch = new MutationObserver(() => void draw())
onMounted(() => {
  void draw()
  themeWatch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onBeforeUnmount(() => themeWatch.disconnect())
watch(() => props.code, draw)
</script>

<template>
  <figure class="diagram">
    <div ref="host" role="img" :aria-label="label"></div>
    <p v-if="failed" class="note bad">The diagram could not be drawn: {{ failed }}</p>
  </figure>
</template>

<style scoped>
.diagram {
  margin: 12px 0 0;
  overflow-x: auto;
}
.diagram :deep(svg) {
  max-width: 100%;
  height: auto;
}
</style>
