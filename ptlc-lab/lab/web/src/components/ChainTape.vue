<script setup lang="ts">
// The contract's own account chain, newest first: every call anyone made,
// whoever signed it and however it was sent. A block from the CLI, from a Go
// scenario, from Syrius and from this page all land on this one tape, which is
// the point of having it beside every bench.
import { computed } from 'vue'
import HexValue from './HexValue.vue'
import { formatAmount, nameOf, state } from '../store.ts'
import type { ContractEvent } from '../ptlc/scan.ts'
import { pointTypeInfo } from '../ptlc/contract.ts'
import { toHex } from '../znn/primitives.ts'

const events = computed(() => state.events.slice(0, 60))

function entryId(e: ContractEvent): string | null {
  if (!e.call) return null
  return e.call.method === 'Create' ? e.sendHash : e.call.id
}

function summary(e: ContractEvent): string {
  const c = e.call
  if (!c) return 'not one of the four calls'
  const who = nameOf(e.from) ?? `${e.from.slice(0, 9)}…`
  switch (c.method) {
    case 'Create':
      return `${who} locks ${formatAmount(e.amount, e.tokenStandard)} to a ${pointTypeInfo(c.pointType)?.name ?? 'lock of unknown type'}`
    case 'Unlock':
      return `${who} opens it`
    case 'ProxyUnlock':
      return `${who} opens it for ${nameOf(c.destination) ?? `${c.destination.slice(0, 9)}…`}`
    case 'Reclaim':
      return `${who} takes it back`
  }
}

function witness(e: ContractEvent): string | null {
  if (!e.ok || !e.call) return null
  if (e.call.method === 'Unlock' || e.call.method === 'ProxyUnlock') return toHex(e.call.witness)
  return null
}

function ago(timestamp: number): string {
  const now = state.momentum?.timestamp ?? Math.floor(state.now / 1000)
  const s = Math.max(0, now - timestamp)
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  return `${(s / 3600).toFixed(1)} h ago`
}
</script>

<template>
  <section class="chain" aria-label="The ptlc contract's account chain">
    <div class="panel-head">
      <h2 class="section-title">Contract chain</h2>
      <span class="small faint">height {{ state.scanHeight }}</span>
    </div>
    <p v-if="state.spork === false" class="note bad">
      The ptlc spork is not active on this chain, so the contract address accepts nothing. The lab's devnet activates it in genesis; this node is something else.
    </p>
    <p v-else-if="events.length === 0" class="small empty">
      No call has reached the contract on this chain yet.
    </p>
    <TransitionGroup name="land" tag="ol">
      <li v-for="e in events" :key="e.height">
        <div class="row">
          <span class="data h">{{ e.height }}</span>
          <span class="tag" :class="e.ok ? 'ink' : 'refused'">{{ e.call?.method ?? 'unknown' }}</span>
          <span v-if="!e.ok" class="tag refused">refused</span>
          <span class="when faint">{{ ago(e.timestamp) }}</span>
        </div>
        <p class="what">{{ summary(e) }}</p>
        <p v-if="entryId(e)" class="small">
          entry <a :href="`#/entries/${entryId(e)}`" class="data">{{ entryId(e)!.slice(0, 10) }}…{{ entryId(e)!.slice(-6) }}</a>
        </p>
        <p v-if="witness(e)" class="small">
          {{ witness(e)!.length === 64 ? 'scalar shown' : 'signature shown' }} <HexValue :value="witness(e)!" :head="12" :tail="8" />
        </p>
      </li>
    </TransitionGroup>
  </section>
</template>

<style scoped>
.chain {
  margin-top: 22px;
}
.chain ol {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  /* The spine of the tape. */
  border-left: 2px solid var(--line-strong);
}
.chain li {
  position: relative;
  padding: 7px 0 9px 14px;
}
.chain li::before {
  content: '';
  position: absolute;
  left: -6px;
  top: 14px;
  width: 10px;
  height: 2px;
  background: var(--line-strong);
}
.row {
  display: flex;
  align-items: baseline;
  gap: 7px;
}
.h {
  color: var(--dim);
  min-width: 2.2em;
}
.when {
  margin-left: auto;
  font-size: 0.74rem;
}
.what {
  font-size: 0.88rem;
  line-height: 1.35;
  margin-top: 3px;
}
.empty {
  margin-top: 6px;
}
</style>
