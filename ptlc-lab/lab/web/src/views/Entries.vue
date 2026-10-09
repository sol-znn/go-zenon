<script setup lang="ts">
// Every entry the contract has held, and the three calls that end one.
//
// The bench says what the contract should answer before a call is sent, and
// then lets it be sent anyway. A lab that only allows calls that succeed
// cannot show a refusal, and the refusals are half of what the contract does.
import { computed, reactive, ref, watch } from 'vue'
import CallPreview from '../components/CallPreview.vue'
import HexValue from '../components/HexValue.vue'
import Who from '../components/Who.vue'
import {
  POINT_SECP256K1,
  REVEAL_MARGIN,
  packProxyUnlock,
  packReclaim,
  packUnlock,
  pointTypeInfo,
} from '../ptlc/contract.ts'
import { makeWitness, witnessOpens } from '../ptlc/curve.ts'
import type { Entry } from '../ptlc/scan.ts'
import {
  addSecret,
  chainId,
  chainNow,
  client,
  contractCall,
  formatAmount,
  inFlight,
  nameOf,
  secretFor,
  signerView,
  state,
  trackedAddresses,
} from '../store.ts'
import { EMPTY_ADDRESS, fromHex, isAddress, isHex } from '../znn/primitives.ts'
import { clock, errorText, short, span } from '../lib.ts'

const props = defineProps<{ param: string }>()

// ---- the list ------------------------------------------------------------------

const filter = ref<'all' | 'locked' | 'paid' | 'returned'>('all')
const listed = computed(() => state.entries.filter((e) => filter.value === 'all' || e.state === filter.value))
const expired = (e: Entry) => e.state === 'locked' && chainNow() >= e.expirationTime
const stateLabel = (e: Entry) => (expired(e) ? 'expired' : e.state)

function expiryText(e: Entry): string {
  const left = e.expirationTime - chainNow()
  if (e.state !== 'locked') return clock(e.expirationTime, state.now)
  return left > 0 ? `in ${span(left)}` : `${span(left)} ago`
}

// ---- one entry -----------------------------------------------------------------

const id = computed(() => props.param.trim().toLowerCase())
const entry = computed(() => state.entries.find((e) => e.id === id.value) ?? null)
const pending = computed(() => state.activity.find((a) => a.hash === id.value && a.kind === 'Create') ?? null)
const signer = computed(() => signerView(state.signer))

const rpc = reactive({ busy: false, answer: '', asked: '' })
async function askNode() {
  rpc.busy = true
  rpc.asked = id.value
  try {
    const info = await client.ptlcById(id.value)
    rpc.answer = info
      ? JSON.stringify(info, null, 2)
      : 'data non existent\n\nThe node says this for an entry that was never made, and equally for one that was paid or returned. Only the contract\'s chain tells them apart.'
  } catch (err) {
    rpc.answer = errorText(err)
  } finally {
    rpc.busy = false
  }
}
watch(id, () => {
  rpc.answer = ''
  rpc.asked = ''
})

// ---- opening it ----------------------------------------------------------------

const open = reactive({
  mode: 'unlock' as 'unlock' | 'proxy',
  proxyTo: '',
  customTo: '',
  source: 'held' as 'held' | 'paste',
  witnessHex: '',
})
const held = computed(() => (entry.value ? secretFor(entry.value.pointType, entry.value.pointLock) : undefined))
watch(
  () => [entry.value?.id, held.value?.id],
  () => {
    open.source = held.value ? 'held' : 'paste'
    open.witnessHex = ''
    if (entry.value) open.proxyTo = entry.value.destination !== EMPTY_ADDRESS ? entry.value.destination : ''
  },
  { immediate: true },
)

/** Where this unlock would pay: the caller for Unlock, the named address for ProxyUnlock. */
const payee = computed(() => {
  if (open.mode === 'unlock') return signer.value.address ?? ''
  return open.proxyTo === 'custom' ? open.customTo.trim() : open.proxyTo
})

const witness = computed<Uint8Array | null>(() => {
  const e = entry.value
  if (!e) return null
  try {
    if (open.source === 'paste') {
      const hex = open.witnessHex.trim()
      return isHex(hex) && hex.length > 0 ? fromHex(hex) : null
    }
    if (!held.value || !isAddress(payee.value)) return null
    return makeWitness(e.pointType, fromHex(held.value.secret), chainId(), e.id, payee.value)
  } catch {
    return null
  }
})

interface Verdict {
  ok: boolean
  text: string
}

const openVerdicts = computed<Verdict[]>(() => {
  const e = entry.value
  if (!e) return []
  const list: Verdict[] = []
  const left = e.expirationTime - chainNow()
  list.push(
    left >= REVEAL_MARGIN
      ? { ok: true, text: `Not expired: ${span(left)} left by the chain's clock.` }
      : left > 0
        ? {
            ok: false,
            text: `Only ${span(left)} left by the chain's clock, under the ${span(REVEAL_MARGIN)} the lab's other clients insist on. The contract answers an unlock two momentums after it is sent; answered after the expiry it is refused, and the witness in it is public all the same.`,
          }
        : { ok: false, text: `Expired ${span(left)} ago. The contract refuses an unlock at or after the expiry.` },
  )
  if (!isAddress(payee.value)) list.push({ ok: false, text: 'There is no address to pay yet.' })
  else if (e.destination !== EMPTY_ADDRESS && e.destination !== payee.value) {
    list.push({ ok: false, text: `This entry pays only ${nameOf(e.destination) ?? short(e.destination)}. Paying ${nameOf(payee.value) ?? short(payee.value)} is refused whatever the witness.` })
  } else if (e.destination !== EMPTY_ADDRESS) list.push({ ok: true, text: 'The destination is the one the entry fixes.' })
  else list.push({ ok: true, text: 'The entry leaves the destination open; the signature names it.' })

  const info = pointTypeInfo(e.pointType)!
  if (!witness.value) list.push({ ok: false, text: open.source === 'held' ? 'This browser does not hold the secret for this lock.' : `Paste the witness: ${info.witness}.` })
  else if (witness.value.length !== info.witnessSize) list.push({ ok: false, text: `A witness for this lock is ${info.witnessSize} bytes; this is ${witness.value.length}.` })
  else if (!isAddress(payee.value)) list.push({ ok: false, text: 'The witness cannot be checked without a destination.' })
  else if (witnessOpens(e.pointType, fromHex(e.pointLock), witness.value, chainId(), e.id, payee.value)) {
    list.push({ ok: true, text: e.pointType === POINT_SECP256K1 ? 'The scalar is the discrete log of the lock.' : 'The signature verifies for this chain, entry and destination.' })
  } else {
    list.push({ ok: false, text: e.pointType === POINT_SECP256K1 ? 'The scalar is not the discrete log of the lock.' : 'The signature does not verify for this chain, entry and destination.' })
  }
  return list
})

const openData = computed(() => {
  const e = entry.value
  if (!e || !witness.value) return null
  try {
    if (open.mode === 'unlock') return packUnlock(e.id, witness.value)
    if (!isAddress(payee.value)) return null
    return packProxyUnlock(e.id, payee.value, witness.value)
  } catch {
    return null
  }
})

const openCli = computed(() => {
  const e = entry.value
  if (!e || !witness.value) return null
  const w = Array.from(witness.value, (b) => b.toString(16).padStart(2, '0')).join('')
  return open.mode === 'unlock' ? `ptlc.unlock ${e.id} ${w}` : `ptlc.proxyUnlock ${e.id} ${payee.value} ${w}`
})

const proxyChoices = computed(() => trackedAddresses().map((a) => ({ address: a, name: nameOf(a) ?? a })))

// ---- taking it back ------------------------------------------------------------

const reclaimVerdicts = computed<Verdict[]>(() => {
  const e = entry.value
  if (!e) return []
  const left = e.expirationTime - chainNow()
  return [
    signer.value.address === e.creator
      ? { ok: true, text: `${signer.value.name} created this entry.` }
      : { ok: false, text: `Only the creator, ${nameOf(e.creator) ?? short(e.creator)}, can reclaim. ${signer.value.name} would be refused.` },
    left <= 0
      ? { ok: true, text: `Expired ${span(left)} ago by the chain's clock.` }
      : { ok: false, text: `Not expired: ${span(left)} to go. A reclaim before then is refused.` },
  ]
})
const reclaimData = computed(() => (entry.value ? packReclaim(entry.value.id) : null))

// ---- sending -------------------------------------------------------------------

const sending = ref('')
const error = ref('')
/** The last block sent from this bench, per entry, so a second press waits for the answer. */
const lastSent = reactive<Record<string, number>>({})
const answering = computed(() => inFlight(lastSent[id.value]))

async function send(kind: 'open' | 'reclaim') {
  const e = entry.value
  if (!e) return
  error.value = ''
  sending.value = kind
  try {
    let activity
    if (kind === 'reclaim') {
      activity = await contractCall(state.signer, 'Reclaim', reclaimData.value!, `Reclaim entry ${short(e.id, 8, 4)}`)
    } else if (open.mode === 'unlock') {
      activity = await contractCall(state.signer, 'Unlock', openData.value!, `Unlock entry ${short(e.id, 8, 4)}`)
    } else {
      activity = await contractCall(
        state.signer,
        'ProxyUnlock',
        openData.value!,
        `Proxy unlock ${short(e.id, 8, 4)} for ${nameOf(payee.value) ?? short(payee.value, 8, 4)}`,
      )
    }
    lastSent[e.id] = activity.n
  } catch (err) {
    error.value = errorText(err)
  } finally {
    sending.value = ''
  }
}

const allOk = (v: Verdict[]) => v.every((x) => x.ok)

/** Under the margin and not yet expired: the answer could go either way. */
const lateReveal = computed(() => {
  const e = entry.value
  if (!e || e.state !== 'locked') return false
  const left = e.expirationTime - chainNow()
  return left > 0 && left < REVEAL_MARGIN
})

function keepWitness(e: Entry) {
  if (e.witness) addSecret(`scalar read from ${short(e.id, 8, 4)}`, POINT_SECP256K1, fromHex(e.witness), 'read from chain')
}
const heldWitness = (e: Entry) => !!e.witness && state.secrets.some((s) => s.secret === e.witness)
</script>

<template>
  <h1 class="bench-title">Entries</h1>
  <p class="bench-lede">
    Read off the contract's chain, so an entry made by the CLI or a scenario is here the same as one made on this page.
  </p>

  <!-- one entry -->
  <section v-if="id" class="panel detail">
    <div class="panel-head">
      <h2 class="section-title">Entry <span class="data">{{ short(id, 12, 8) }}</span></h2>
      <a href="#/entries" class="small">All entries</a>
    </div>

    <template v-if="entry">
      <dl class="kv">
        <dt>State</dt>
        <dd>
          <span class="tag" :class="[entry.state, { expired: expired(entry) }]">{{ stateLabel(entry) }}</span>
          <span v-if="expired(entry)" class="small"> still held by the contract; only its creator can take it back</span>
        </dd>
        <dt>Holds</dt>
        <dd><strong>{{ formatAmount(entry.amount, entry.tokenStandard) }}</strong></dd>
        <dt>Created by</dt>
        <dd><Who :address="entry.creator" /></dd>
        <dt>Pays</dt>
        <dd>
          <Who v-if="entry.destination !== EMPTY_ADDRESS" :address="entry.destination" />
          <span v-else>any address the signature names</span>
        </dd>
        <dt>Lock</dt>
        <dd>
          {{ pointTypeInfo(entry.pointType)?.name ?? `type ${entry.pointType}` }}
          <HexValue :value="entry.pointLock" full />
          <span v-if="held" class="tag paid">secret held here: {{ held.name }}</span>
        </dd>
        <dt>Expiry</dt>
        <dd>{{ clock(entry.expirationTime, state.now) }} <span class="small">{{ entry.state === 'locked' ? expiryText(entry) : '' }}</span></dd>
        <dt>Id</dt>
        <dd><HexValue :value="entry.id" full /></dd>
      </dl>

      <!-- how it ended -->
      <div v-if="entry.closedBy" class="ended">
        <p v-if="entry.state === 'paid'">
          <strong>Paid</strong> to <Who :address="entry.paidTo ?? ''" /> by a
          {{ entry.closedBy.call?.method === 'ProxyUnlock' ? 'proxy unlock' : 'unlock' }} from
          <Who :address="entry.closedBy.from" />, {{ clock(entry.closedBy.timestamp, state.now) }}.
        </p>
        <p v-else><strong>Returned</strong> to its creator, {{ clock(entry.closedBy.timestamp, state.now) }}.</p>
        <dl v-if="entry.witness" class="kv" style="margin-top: 8px">
          <dt>{{ entry.pointType === POINT_SECP256K1 ? 'Scalar shown' : 'Signature shown' }}</dt>
          <dd>
            <HexValue :value="entry.witness" full />
            <template v-if="entry.pointType === POINT_SECP256K1">
              <span v-if="heldWitness(entry)" class="tag paid">held here</span>
              <button v-else type="button" class="btn btn-quiet" @click="keepWitness(entry)">Keep it as a secret</button>
            </template>
          </dd>
        </dl>
        <p v-if="entry.pointType === POINT_SECP256K1 && entry.witness" class="note" style="margin-top: 8px">
          This is what a swap counterparty reads: the scalar is now public, and with the tweak they agreed it opens the other leg.
        </p>
      </div>

      <div v-if="entry.refused.length" class="ended">
        <p class="small">
          <strong>{{ entry.refused.length }} call{{ entry.refused.length === 1 ? '' : 's' }} on this entry refused:</strong>
          <span v-for="r in entry.refused" :key="r.height"> {{ r.call?.method }} from {{ nameOf(r.from) ?? short(r.from, 8, 4) }} at height {{ r.height }};</span>
        </p>
      </div>
    </template>

    <template v-else>
      <p v-if="pending && ['signing', 'published', 'confirmed'].includes(pending.stage)" class="note warn">
        The Create is on its way: the contract makes the entry when it answers the block, a momentum or two after it was published. This page updates when it does.
      </p>
      <p v-else-if="pending && pending.stage === 'refused'" class="note bad">
        The contract refused this Create, so no entry was made. {{ pending.error }}
      </p>
      <p v-else-if="!isHex(id, 32)" class="note bad">An entry id is the 32-byte hash of its Create block, in hex.</p>
      <p v-else class="note">
        No accepted Create with this hash is on the contract's chain. It may be on another chain, or still in the pool.
      </p>
    </template>

    <details class="ask" @toggle="($event.target as HTMLDetailsElement).open && rpc.asked !== id && askNode()">
      <summary>What <code>embedded.ptlc.getById</code> says</summary>
      <pre class="well">{{ rpc.busy ? 'asking' : rpc.answer }}</pre>
      <button type="button" class="btn btn-quiet" style="margin-top: 6px" :disabled="rpc.busy" @click="askNode">Ask again</button>
    </details>
  </section>

  <!-- the calls -->
  <template v-if="entry && entry.state === 'locked'">
    <section class="panel">
      <div class="panel-head">
        <h2 class="section-title">Open it</h2>
        <span class="small">signing as <strong>{{ signer.name }}</strong>, {{ signer.kind }}</span>
      </div>
      <div class="choice" role="radiogroup" aria-label="Which call">
        <label>
          <input type="radio" name="mode" value="unlock" v-model="open.mode" />
          <strong>Unlock</strong>
          <small>Pays whoever sends it: {{ signer.name }}.</small>
        </label>
        <label>
          <input type="radio" name="mode" value="proxy" v-model="open.mode" />
          <strong>ProxyUnlock</strong>
          <small>Pays an address you name. Anyone may send it, with no funds of their own.</small>
        </label>
      </div>

      <label v-if="open.mode === 'proxy'" class="field">
        <span>Pay to</span>
        <select v-model="open.proxyTo">
          <option value="" disabled>choose an address</option>
          <option v-for="c in proxyChoices" :key="c.address" :value="c.address">{{ c.name }}, {{ c.address }}</option>
          <option value="custom">another address</option>
        </select>
      </label>
      <label v-if="open.mode === 'proxy' && open.proxyTo === 'custom'" class="field">
        <span>Address</span>
        <input type="text" v-model="open.customTo" spellcheck="false" placeholder="z1…" />
      </label>

      <label class="field">
        <span>Witness</span>
        <select v-model="open.source">
          <option value="held" :disabled="!held">
            {{ held ? (entry.pointType === POINT_SECP256K1 ? `the scalar of ${held.name}` : `a signature by ${held.name}`) : 'no secret for this lock is held here' }}
          </option>
          <option value="paste">a witness I paste</option>
        </select>
      </label>
      <label v-if="open.source === 'paste'" class="field">
        <span>The witness, in hex: {{ pointTypeInfo(entry.pointType)?.witness }}</span>
        <textarea v-model="open.witnessHex" spellcheck="false" rows="2"></textarea>
        <span class="hint">Paste a wrong one to see the contract refuse it.</span>
      </label>

      <ul class="verdicts">
        <li v-for="v in openVerdicts" :key="v.text" :class="{ no: !v.ok }">{{ v.text }}</li>
      </ul>
      <p v-if="lateReveal" class="note bad">
        Too close to the expiry to know the answer. If the contract runs it after the expiry it refuses, and whoever reads the chain has the witness anyway: for a point lock, the secret that may open the other side of a swap. It can be sent all the same; nothing else here will.
      </p>
      <p v-else class="note" :class="allOk(openVerdicts) ? 'good' : 'warn'">
        {{ allOk(openVerdicts) ? 'The contract should accept this and pay the entry out.' : 'The contract should refuse this. It can be sent all the same: nothing moves, and the refusal lands on the tape.' }}
      </p>

      <CallPreview
        :method="open.mode === 'unlock' ? 'Unlock' : 'ProxyUnlock'"
        :data="openData"
        :cli="openCli"
        :cli-note="open.mode === 'unlock' ? 'Run it as the address being paid: the CLI\'s Unlock pays the keystore index it is run with.' : 'Any keystore index can send it.'"
      />

      <div class="btn-row" style="margin-top: 14px">
        <button type="button" class="btn btn-primary" :disabled="!openData || !!sending || !!signer.blocked || answering" @click="send('open')">
          {{ sending === 'open' ? (state.signer === 'syrius' ? 'Waiting for Syrius' : 'Signing') : `${open.mode === 'unlock' ? 'Unlock' : 'Proxy unlock'} as ${signer.name}` }}
        </button>
        <span v-if="signer.blocked" class="small">{{ signer.blocked }}</span>
        <span v-else-if="answering" class="small"><span class="working">Published.</span> The buttons come back when the contract has answered.</span>
      </div>
    </section>

    <section class="panel">
      <div class="panel-head">
        <h2 class="section-title">Take it back</h2>
        <span class="small">signing as <strong>{{ signer.name }}</strong></span>
      </div>
      <ul class="verdicts">
        <li v-for="v in reclaimVerdicts" :key="v.text" :class="{ no: !v.ok }">{{ v.text }}</li>
      </ul>
      <CallPreview method="Reclaim" :data="reclaimData" :cli="`ptlc.reclaim ${entry.id}`" cli-note="Run it as the creator's keystore index." />
      <div class="btn-row" style="margin-top: 14px">
        <button type="button" class="btn" :class="{ 'btn-primary': allOk(reclaimVerdicts) }" :disabled="!!sending || !!signer.blocked || answering" @click="send('reclaim')">
          {{ sending === 'reclaim' ? (state.signer === 'syrius' ? 'Waiting for Syrius' : 'Signing') : allOk(reclaimVerdicts) ? `Reclaim as ${signer.name}` : `Send a reclaim that should be refused` }}
        </button>
      </div>
    </section>
    <p v-if="error" class="note bad" style="margin-top: 12px" role="alert">{{ error }}</p>
  </template>

  <!-- the list -->
  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">{{ id ? 'All entries' : 'On this chain' }}</h2>
      <div class="filter" role="radiogroup" aria-label="Show">
        <button v-for="f in ['all', 'locked', 'paid', 'returned'] as const" :key="f" type="button" role="radio" :aria-checked="filter === f" :class="{ on: filter === f }" @click="filter = f">
          {{ f }}
        </button>
      </div>
    </div>
    <p v-if="state.entries.length === 0" class="small">
      No entry has been made on this chain yet. <a href="#/lock">Lock some funds</a>, or run a scenario from <a href="#/programs">CLI and scenarios</a>.
    </p>
    <p v-else-if="listed.length === 0" class="small">No entry is {{ filter }} right now.</p>
    <div v-else class="table-scroll">
      <table class="grid rows">
        <thead>
          <tr>
            <th>State</th>
            <th>Holds</th>
            <th>Lock</th>
            <th>From, to</th>
            <th>Expiry</th>
            <th>Id</th>
          </tr>
        </thead>
        <TransitionGroup name="land" tag="tbody">
          <tr v-for="e in listed" :key="e.id" :class="{ current: e.id === id }">
            <td><span class="tag" :class="[e.state, { expired: expired(e) }]">{{ stateLabel(e) }}</span></td>
            <td>{{ formatAmount(e.amount, e.tokenStandard) }}</td>
            <td>
              {{ pointTypeInfo(e.pointType)?.name }}
              <span v-if="secretFor(e.pointType, e.pointLock)" class="tag paid" title="This browser holds the secret">held</span>
            </td>
            <td>
              {{ nameOf(e.creator) ?? short(e.creator, 7, 4) }} to
              {{ e.destination === EMPTY_ADDRESS ? 'anyone' : (nameOf(e.destination) ?? short(e.destination, 7, 4)) }}
            </td>
            <td class="small">{{ expiryText(e) }}</td>
            <td><a :href="`#/entries/${e.id}`" class="data">{{ short(e.id, 8, 6) }}</a></td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </section>
</template>

<style scoped>
.ended {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px dashed var(--line-strong);
}
.ask {
  margin-top: 12px;
}
.verdicts {
  list-style: none;
  margin: 12px 0 10px;
  padding: 0;
  font-size: 0.9rem;
}
.verdicts li {
  padding-left: 20px;
  position: relative;
  margin-top: 3px;
}
.verdicts li::before {
  content: '';
  position: absolute;
  left: 2px;
  top: 0.52em;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--paid);
}
.verdicts li.no::before {
  background: var(--refused);
  border-radius: 1px;
}
.filter {
  display: flex;
}
.filter button {
  font: inherit;
  font-size: 0.8rem;
  font-weight: 500;
  background: var(--sunk);
  border: 1px solid var(--line-strong);
  transition:
    color 150ms var(--ease),
    background 150ms var(--ease);
  border-left-width: 0;
  padding: 2px 10px;
  cursor: pointer;
  color: var(--soft);
}
.filter button:first-child {
  border-left-width: 1px;
  border-radius: var(--radius) 0 0 var(--radius);
}
.filter button:last-child {
  border-radius: 0 var(--radius) var(--radius) 0;
}
.filter button:hover {
  color: var(--fg);
}
.filter button.on {
  background: var(--raised);
  color: var(--fg);
  box-shadow:
    var(--edge),
    inset 0 -2px 0 var(--znn);
}
tr.current td {
  background: var(--sunk);
}
</style>
