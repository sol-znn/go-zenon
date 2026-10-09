<script setup lang="ts">
// The blocks this page sent, and where each one is. A contract call passes
// four points: signed, published, included in a momentum, answered by the
// contract in the next. The gaps between them are the latency a client has to
// plan for, so they are shown rather than hidden behind a spinner.
import { computed } from 'vue'
import HexValue from './HexValue.vue'
import { signerView, state, type Activity } from '../store.ts'

const CONTRACT = ['Create', 'Unlock', 'ProxyUnlock', 'Reclaim']

function steps(a: Activity): { name: string; done: boolean; current: boolean; bad: boolean }[] {
  const contract = CONTRACT.includes(a.kind)
  const order = contract ? ['signing', 'published', 'confirmed', 'answered'] : ['signing', 'published', 'confirmed']
  const names: Record<string, string> = {
    signing: a.signer === 'syrius' ? 'In Syrius' : 'Signing',
    published: 'Published',
    confirmed: 'In a momentum',
    answered: a.stage === 'refused' ? 'Refused' : 'Accepted',
  }
  const reached =
    a.stage === 'accepted' || a.stage === 'refused'
      ? order.length
      : a.stage === 'confirmed'
        ? 3
        : a.stage === 'published'
          ? 2
          : a.stage === 'signing'
            ? 0
            : a.hash
              ? 2
              : 0
  const ended = ['accepted', 'refused', 'failed', 'declined'].includes(a.stage) || (!contract && a.stage === 'confirmed')
  return order.map((key, i) => ({
    name: names[key],
    done: i < reached,
    current: !ended && i === reached,
    bad: key === 'answered' && a.stage === 'refused',
  }))
}

function took(a: Activity): string | null {
  if (!a.sendMomentum) return null
  const included = `included in momentum ${a.sendMomentum.toLocaleString('en')}`
  if (!a.answerMomentum || !a.sendTimestamp || !a.answerTimestamp) return CONTRACT.includes(a.kind) ? null : included
  const momentums = a.answerMomentum - a.sendMomentum
  const seconds = a.answerTimestamp - a.sendTimestamp
  return `${included}; the contract answered ${momentums} momentum${momentums === 1 ? '' : 's'} later, ${seconds} s by the chain's clock`
}

/** A call the contract has not answered for longer than a healthy devnet takes. */
function overdue(a: Activity): number | null {
  if (a.stage !== 'confirmed' || !CONTRACT.includes(a.kind) || !a.sendMomentum || !state.momentum) return null
  const waited = state.momentum.height - a.sendMomentum
  return waited >= 4 ? waited : null
}

const items = computed(() => state.activity.slice(0, 12))
</script>

<template>
  <section class="feed" aria-label="Blocks sent from this page">
    <h2 class="section-title">Sent from this page</h2>
    <p v-if="items.length === 0" class="small empty">
      Nothing yet. Lock funds from the Lock funds bench, or run the swap, and each block is followed here until the contract answers it.
    </p>
    <TransitionGroup name="land" tag="ol">
      <li v-for="a in items" :key="a.n" :class="a.stage">
        <div class="top">
          <strong>{{ a.label }}</strong>
          <span class="by">{{ signerView(a.signer).name }}</span>
        </div>
        <ol class="steps" :aria-label="`Progress: ${a.stage}`">
          <li v-for="s in steps(a)" :key="s.name" :class="{ done: s.done, current: s.current, bad: s.bad }">
            <span class="dot" :class="{ working: s.current }" aria-hidden="true"></span>{{ s.name }}
          </li>
        </ol>
        <p v-if="a.hash" class="small">block <HexValue :value="a.hash" /></p>
        <p v-if="a.error" class="small err">{{ a.error }}</p>
        <p v-if="a.note" class="small">{{ a.note }}</p>
        <p v-if="took(a)" class="small faint">{{ took(a) }}</p>
        <p v-if="overdue(a)" class="small err">
          {{ overdue(a) }} momentums and no answer. The contract answers in the momentum after the call when every pillar has the block; a wait like this means some do not. <a href="#/">Start here</a> shows the node's peers.
        </p>
      </li>
    </TransitionGroup>
  </section>
</template>

<style scoped>
.feed ol {
  list-style: none;
  margin: 0;
  padding: 0;
}
.feed > ol > li {
  border-top: 1px solid var(--line);
  padding: 9px 0 10px;
}
.empty {
  margin-top: 6px;
}
.top {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  align-items: baseline;
}
.top strong {
  font-size: 0.88rem;
  line-height: 1.3;
}
.by {
  font-size: 0.74rem;
  color: var(--dim);
  flex: none;
}
.steps {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 12px;
  margin: 5px 0 3px !important;
  font-size: 0.74rem;
  color: var(--dim);
}
.steps li {
  display: flex;
  align-items: center;
  gap: 5px;
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  border: 1.5px solid var(--line-strong);
  background: transparent;
}
.steps li.done {
  color: var(--fg);
}
.steps li.done .dot {
  background: var(--paid);
  border-color: var(--paid);
}
.steps li.current {
  color: var(--fg);
}
.steps li.current .dot {
  background: var(--locked);
  border-color: var(--locked);
}
.steps li.bad {
  color: var(--refused);
}
.steps li.bad .dot {
  background: var(--refused);
  border-color: var(--refused);
}
.err {
  color: var(--refused);
}
li.failed .top strong,
li.declined .top strong {
  color: var(--soft);
}
</style>
