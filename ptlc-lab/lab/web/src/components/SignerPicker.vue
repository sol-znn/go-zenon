<script setup lang="ts">
// Who signs the next block. The same call can leave the page three ways: with
// a devnet key the page holds, through the Syrius extension, or (from the
// Programs bench) through the CLI. This picks between the first two.
import { computed } from 'vue'
import { setSigner, signerIds, signerView, state } from '../store.ts'

const views = computed(() => signerIds.map(signerView))
const current = computed(() => signerView(state.signer))
</script>

<template>
  <div class="signer">
    <div class="pick" role="radiogroup" aria-label="Who signs the next block">
      <span class="caption">Signing as</span>
      <button
        v-for="v in views"
        :key="v.id"
        type="button"
        role="radio"
        :aria-checked="v.id === state.signer"
        :class="{ on: v.id === state.signer, blocked: !!v.blocked }"
        :title="v.blocked ?? v.address ?? ''"
        @click="setSigner(v.id)"
      >
        {{ v.name }}
      </button>
    </div>
    <div class="line">
      <span v-if="current.address" class="data">{{ current.address.slice(0, 12) }}…{{ current.address.slice(-6) }}</span>
      <span class="kind">{{ current.kind }}</span>
      <a v-if="current.blocked" class="why" href="#/wallet">{{ current.blocked }}</a>
    </div>
  </div>
</template>

<style scoped>
.signer {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  justify-content: center;
  gap: 4px;
  flex: none;
  max-width: 100%;
}
.pick {
  display: flex;
  align-items: center;
  gap: 0;
}
.caption {
  font-family: var(--font-mono);
  font-size: 11px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--dim);
  margin-right: 10px;
}
/* A segmented control: one of these is on, and it is the raised one. */
button {
  font: inherit;
  font-size: 0.86rem;
  font-weight: 500;
  color: var(--dim);
  background: var(--sunk);
  border: 1px solid var(--line-strong);
  border-left-width: 0;
  padding: 4px 13px;
  cursor: pointer;
  transition:
    color 150ms var(--ease),
    background 150ms var(--ease);
}
button:first-of-type {
  border-left-width: 1px;
  border-radius: var(--radius) 0 0 var(--radius);
}
button:last-of-type {
  border-radius: 0 var(--radius) var(--radius) 0;
}
button:hover {
  color: var(--fg);
}
button.on {
  background: var(--raised);
  color: var(--fg);
  box-shadow:
    var(--edge),
    inset 0 -2px 0 var(--znn);
}
button.blocked:not(.on) {
  color: color-mix(in srgb, var(--dim) 65%, transparent);
}
.line {
  display: flex;
  gap: 10px;
  align-items: baseline;
  font-size: 0.74rem;
  color: var(--dim);
  flex-wrap: wrap;
  justify-content: flex-end;
}
.line .data {
  color: var(--soft);
  font-size: 0.72rem;
}
.why {
  color: var(--warn);
}
/* Between the phone layout and about 900px the bar has no room for the
   caption; the group still carries its name for a screen reader. */
@media (min-width: 761px) and (max-width: 900px) {
  .caption {
    display: none;
  }
}
@media (max-width: 760px) {
  .signer {
    align-items: flex-start;
    width: 100%;
    order: 4;
  }
  .line {
    justify-content: flex-start;
  }
}
</style>
