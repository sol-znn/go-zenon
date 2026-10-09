<script setup lang="ts">
// What a program run by labd printed, as it prints it.
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { followRun, stopRun, type RunLine } from '../api.ts'

const props = defineProps<{ runId: string | null; tall?: boolean }>()
const emit = defineEmits<{ done: [exit: number]; line: [line: RunLine] }>()

const lines = ref<RunLine[]>([])
const exit = ref<number | null>(null)
const running = ref(false)
const box = ref<HTMLElement | null>(null)
let stop: (() => void) | null = null

watch(
  () => props.runId,
  (id) => {
    stop?.()
    stop = null
    lines.value = []
    exit.value = null
    running.value = false
    if (!id) return
    running.value = true
    stop = followRun(
      id,
      (line) => {
        lines.value.push(line)
        emit('line', line)
        // Follow the output only while the reader is at the bottom of it.
        const el = box.value
        const pinned = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 40
        if (pinned) void nextTick(() => el && (el.scrollTop = el.scrollHeight))
      },
      (code) => {
        exit.value = code
        running.value = false
        emit('done', code)
      },
    )
  },
  { immediate: true },
)

onBeforeUnmount(() => stop?.())

const time = (at: number) => new Date(at).toISOString().slice(11, 19)

async function halt() {
  if (props.runId) await stopRun(props.runId).catch(() => undefined)
}
</script>

<template>
  <div v-if="runId" class="run">
    <div class="bar">
      <span v-if="running" class="tag locked"><span class="working">running</span></span>
      <span v-else-if="exit === 0" class="tag paid">finished</span>
      <span v-else class="tag refused">exit {{ exit }}</span>
      <span class="small faint">{{ lines.length }} lines</span>
      <button v-if="running" type="button" class="btn btn-quiet" @click="halt">Stop</button>
    </div>
    <div ref="box" class="out" :class="{ tall }" tabindex="0" aria-label="Program output">
      <div v-for="l in lines" :key="l.n" :class="l.stream"><span class="t">{{ time(l.at) }}</span>{{ l.text || ' ' }}</div>
      <div v-if="running" class="cursor working">▍</div>
    </div>
  </div>
</template>

<style scoped>
.run {
  margin-top: 12px;
}
.bar {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 6px;
}
.bar .btn {
  margin-left: auto;
}
/* A terminal: cut into the panel, and the one place a line is not wrapped
   into prose. */
.out {
  background: var(--sunk);
  color: var(--soft);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
  font-family: var(--font-mono);
  font-size: 0.74rem;
  line-height: 1.65;
  max-height: 280px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.out.tall {
  max-height: 460px;
}
.t {
  color: var(--dim);
  opacity: 0.7;
  margin-right: 10px;
  user-select: none;
}
.err {
  color: var(--danger);
}
.lab {
  color: var(--warn);
}
.cursor {
  color: var(--znn-ink);
}
</style>
