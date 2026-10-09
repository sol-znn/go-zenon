<script setup lang="ts">
// One scenario as lanes and steps: who acts, towards whom, and what came of
// it. Before a run every step is drawn dim, so the flow can be read first;
// during one, each step lights as the runner reports it.
//
// Two things are shown apart on purpose. The mark on the left is the test:
// did this step go as the catalogue says it must. The line on the right is the
// chain: what the contract did. "Bob tries a wrong secret" ends with a red
// line that stops short of the contract and a green mark, because a refusal
// was the right answer.
import { computed, onBeforeUnmount, ref } from 'vue'
import { PARTY, took, type RunView, type Scenario, type StepDef, type StepView } from './flow.ts'

const props = defineProps<{ scenario: Scenario; run: RunView | null }>()

const lanes = computed(() => props.scenario.parties)
const lane = (party: string | undefined): number => Math.max(0, lanes.value.indexOf(party ?? ''))
const centre = (index: number): number => ((index + 0.5) / lanes.value.length) * 100

const view = (step: StepDef): StepView => props.run?.steps[step.id] ?? { state: 'pending', detail: '' }
const flying = (v: StepView): boolean => v.state === 'active' || v.state === 'sent' || v.state === 'confirmed'

function arrow(step: StepDef): Record<string, string> {
  const a = lane(step.from)
  const b = lane(step.to)
  return { left: `${centre(Math.min(a, b))}%`, width: `${Math.abs(centre(b) - centre(a))}%` }
}
const backwards = (step: StepDef): boolean => lane(step.to) < lane(step.from)

/** Who is in the middle of something, for the glow on the lane's token. */
const acting = computed(() => {
  const set = new Set<string>()
  for (const step of props.scenario.steps) {
    if (!flying(view(step))) continue
    set.add(step.from)
    if (step.to) set.add(step.to)
  }
  return set
})

/** What each party has been paid so far in this run, read off the finished pay steps. */
const received = computed(() => {
  const out: Record<string, string[]> = {}
  for (const step of props.scenario.steps) {
    const v = view(step)
    if (step.kind !== 'pay' || v.state !== 'done' || !step.to) continue
    const amount = v.detail.split(' paid')[0]
    if (amount) (out[step.to] ??= []).push(amount)
  }
  return out
})

const pips = (v: StepView): number => ({ pending: 0, active: 0, sent: 1, confirmed: 2, done: 3, failed: 0 })[v.state]
const outcome = (v: StepView): string => ({ accepted: 'Accepted', refused: 'Refused', rejected: 'Not taken' })[v.answer ?? 'accepted']

// A wait is on the chain's clock. The runner says how long it had to go when
// it began; the bar runs that down on this machine's clock from then.
const tick = ref(Date.now())
const timer = window.setInterval(() => (tick.value = Date.now()), 250)
onBeforeUnmount(() => window.clearInterval(timer))
function left(v: StepView): number {
  if (v.state === 'done') return 0
  if (v.waitSeconds === undefined || v.waitFrom === undefined) return 0
  return Math.max(0, v.waitSeconds - (tick.value - v.waitFrom) / 1000)
}
function waited(v: StepView): number {
  if (v.state === 'done') return 100
  if (!v.waitSeconds) return 0
  return Math.min(100, 100 - (left(v) / v.waitSeconds) * 100)
}

const short = (hash: string): string => `${hash.slice(0, 10)}…${hash.slice(-6)}`
const clock = (at: number): string => new Date(at).toISOString().slice(11, 19)
</script>

<template>
  <div class="stage" :style="{ '--lanes': lanes.length }">
    <div class="lanes" aria-hidden="true">
      <span></span>
      <span></span>
      <div class="lane-heads">
        <div v-for="party in lanes" :key="party" class="lane-head" :class="[party, { acting: acting.has(party) }]">
          <span class="token">
            <svg v-if="party === 'contract'" viewBox="0 0 24 24" width="16" height="16"><path d="M7 10V7a5 5 0 0 1 10 0v3M5 10h14v10H5z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" /></svg>
            <template v-else>{{ PARTY[party]?.mark }}</template>
          </span>
          <span class="who">{{ PARTY[party]?.name ?? party }}</span>
          <TransitionGroup name="coin" tag="span" class="purse">
            <span v-for="(amount, i) in received[party] ?? []" :key="i" class="coin">+{{ amount }}</span>
          </TransitionGroup>
        </div>
      </div>
    </div>

    <ol class="steps">
      <li v-for="(step, index) in scenario.steps" :key="step.id" class="step" :class="[step.kind, view(step).state, view(step).answer, { flying: flying(view(step)) }]" :style="{ '--i': index }">
        <span class="mark" :title="view(step).state">
          <svg viewBox="0 0 24 24" width="22" height="22">
            <circle class="ring" cx="12" cy="12" r="9" />
            <circle class="spin" cx="12" cy="12" r="9" />
            <path class="tick" d="M7.5 12.5l3 3 6-6.5" />
            <path class="cross" d="M8.5 8.5l7 7M15.5 8.5l-7 7" />
          </svg>
        </span>

        <div class="what">
          <p class="label">
            {{ step.label }}
            <Transition name="pop">
              <span v-if="step.kind === 'call' && view(step).state === 'done'" class="tag" :class="view(step).answer">{{ outcome(view(step)) }}</span>
            </Transition>
          </p>
          <div v-if="step.kind === 'call' && view(step).answer !== 'rejected'" class="pips" :data-lit="pips(view(step))">
            <span>Sent</span><span>On the chain</span><span>Answered</span>
          </div>
          <Transition name="swap" mode="out-in">
            <p v-if="view(step).detail" :key="view(step).detail" class="detail">{{ view(step).detail }}</p>
          </Transition>
        </div>

        <div class="track">
          <span v-for="(party, i) in lanes" :key="party" class="guide" :style="{ left: `${centre(i)}%` }"></span>

          <div v-if="step.kind === 'wait'" class="timer">
            <span class="fill" :style="{ width: `${waited(view(step))}%` }"></span>
            <span class="count">
              <template v-if="view(step).state === 'done'">deadline passed</template>
              <template v-else-if="flying(view(step))">{{ Math.ceil(left(view(step))) }} s to the deadline</template>
              <template v-else>waits for the deadline</template>
            </span>
          </div>

          <span v-else-if="!step.to" class="node" :style="{ left: `${centre(lane(step.from))}%` }"></span>

          <div v-else class="arrow" :class="{ backwards: backwards(step) }" :style="arrow(step)">
            <span class="shaft"></span>
            <span class="tip"></span>
            <span class="stop"></span>
            <span class="packet"></span>
            <span class="arrive"></span>
          </div>
        </div>
      </li>
    </ol>

    <Transition name="rise">
      <div v-if="run && run.status !== 'running'" class="verdict" :class="run.status" role="status">
        <strong>{{ run.status === 'pass' ? 'Passed' : 'Failed' }}</strong>
        <span>{{ run.summary }}</span>
        <small>{{ took(run) }}</small>
      </div>
    </Transition>

    <details v-if="run" class="log">
      <summary>Technical details</summary>
      <ul class="hashes">
        <template v-for="step in scenario.steps" :key="step.id">
          <li v-if="view(step).hash">
            <span>{{ step.label }}</span>
            <code :title="view(step).hash">{{ short(view(step).hash!) }}</code>
          </li>
        </template>
      </ul>
      <pre v-if="run.log.length">{{ run.log.map((l) => `${clock(l.at)}  ${l.text}`).join('\n') }}</pre>
    </details>
  </div>
</template>
