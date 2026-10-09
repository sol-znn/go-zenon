<script setup lang="ts">
// The chain's pulse, as a strip chart. Each bar is one momentum and its height
// is how long it took; a slot is 10 s, so a bar twice as tall is a slot nobody
// produced in. The last bar is the momentum being waited for, growing.
//
// It is here because every latency in the lab is counted in momentums: a
// contract call is answered in the momentum after the one that includes it.
import { computed } from 'vue'
import { state } from '../store.ts'

const SLOT = 10
const waiting = computed(() => (state.momentumSeenAt ? Math.max(0, (state.now - state.momentumSeenAt) / 1000) : 0))
const bars = computed(() => state.slots.slice(-22))
const height = (seconds: number) => `${Math.min(100, (seconds / (SLOT * 3)) * 100)}%`
const average = computed(() => {
  if (state.slots.length < 3) return null
  return state.slots.reduce((a, b) => a + b, 0) / state.slots.length
})
</script>

<template>
  <div class="clock" :class="{ down: !!state.nodeError }">
    <div class="readout">
      <template v-if="state.momentum">
        <span class="lamp" :key="state.momentum.height" aria-hidden="true"></span>
        <span class="data height">{{ state.momentum.height.toLocaleString('en') }}</span>
        <span class="label">momentum, chain {{ state.momentum.chainIdentifier }}</span>
      </template>
      <span v-else-if="state.nodeError" class="label">node not answering</span>
      <span v-else class="label">reading the chain</span>
    </div>
    <div class="strip" role="img" :aria-label="`The last ${bars.length} momentums, by how long each took`">
      <span class="slot-line" aria-hidden="true"></span>
      <span
        v-for="(seconds, i) in bars"
        :key="state.momentum!.height - bars.length + i"
        class="bar"
        :class="{ missed: seconds > SLOT * 1.5 }"
        :style="{ height: height(seconds) }"
        :title="`${seconds.toFixed(0)} s`"
      ></span>
      <span v-if="state.momentum" class="bar next" :class="{ missed: waiting > SLOT * 1.5 }" :style="{ height: height(waiting) }"></span>
    </div>
    <div class="label side">
      <template v-if="state.nodeError">{{ state.nodeError }}</template>
      <template v-else-if="state.momentum">
        {{ waiting.toFixed(0) }} s since the last<template v-if="average">, {{ average.toFixed(1) }} s on average</template>
      </template>
    </div>
  </div>
</template>

<style scoped>
.clock {
  display: flex;
  align-items: center;
  gap: 16px;
  flex: 1;
  min-width: 0;
}
.readout {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex: none;
}
.height {
  font-size: 1.1rem;
  font-weight: 600;
  color: var(--fg);
}
.label {
  font-size: 0.76rem;
  color: var(--dim);
}
.side {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.down .side {
  color: var(--danger);
}
/* Re-keyed on every momentum, so it beats once each time one lands. */
.lamp {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--live);
  align-self: center;
  animation: beat 1.4s ease-out;
}
@keyframes beat {
  0% {
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--znn) 85%, transparent);
    background: #b6ffcf;
    transform: scale(1.35);
  }
  100% {
    box-shadow: 0 0 0 14px transparent;
    background: var(--live);
    transform: none;
  }
}
.strip {
  position: relative;
  display: flex;
  align-items: flex-end;
  gap: 3px;
  height: 38px;
  width: 196px;
  flex: none;
  border-bottom: 1px solid var(--line-strong);
}
.slot-line {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 33.33%;
  border-top: 1px dashed var(--line-strong);
}
.bar {
  width: 5px;
  background: var(--dim);
  border-radius: 1px 1px 0 0;
  transition: height 900ms linear;
  position: relative;
}
/* A slot nobody produced in. Amber is the colour of time running over. */
.bar.missed {
  background: var(--warn);
}
/* The momentum being waited for: the one thing here that is live, so the one
   bar in plasma. */
.bar.next {
  background: var(--plasma);
}
.bar.next.missed {
  background: var(--warn);
}
/* The bar holds the wordmark, this and the signer picker on one line down to
   760px, so what is said twice goes first: the sentence beside the strip,
   then the caption beside the height. Without these the strip ran on under
   the picker at about 840px. */
@media (max-width: 1180px) {
  .side {
    display: none;
  }
}
@media (max-width: 1000px) {
  .strip {
    width: 130px;
  }
  .readout .label {
    display: none;
  }
}
@media (max-width: 760px) {
  .clock {
    flex-basis: 100%;
    order: 3;
  }
  .strip {
    width: 130px;
  }
  .side {
    display: none;
  }
}
</style>
