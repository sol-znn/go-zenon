<script setup lang="ts">
// SPEC.md §3 as a walk: a NoM-to-NoM swap on two point locks with a tweak,
// one call at a time, each party played by whichever signer you give it.
//
// Nothing here is scripted past the next call. Each step reads the chain to
// decide whether it is done, so a step can equally be made from the Entries
// bench, the CLI or a second browser, and the walk picks it up.
//
// The order is the protocol: Alice holds t, so she locks first and her entry
// outlasts Bob's. Until 2026-10-09 this bench had Bob lock first and expire
// last, which lets Alice claim his entry without funding hers, or take hers
// back at its expiry and claim his afterwards (DECISIONS D-30).
import { computed, reactive, ref, watch } from 'vue'
import HexValue from '../components/HexValue.vue'
import MermaidDiagram from '../components/MermaidDiagram.vue'
import Who from '../components/Who.vue'
import { POINT_SECP256K1, REVEAL_MARGIN, packCreate, packReclaim, packUnlock } from '../ptlc/contract.ts'
import { pointOf, randomScalar, tweakPoint, tweakScalar } from '../ptlc/curve.ts'
import type { Entry } from '../ptlc/scan.ts'
import {
  addSecret,
  chainNow,
  contractCall,
  inFlight,
  formatAmount,
  parseUnits,
  receiveAll,
  signerIds,
  signerView,
  state,
  type SignerId,
} from '../store.ts'
import { QSR_ZTS, ZNN_ZTS, equalBytes, fromHex, toHex } from '../znn/primitives.ts'
import { clock, errorText, short, span } from '../lib.ts'

defineProps<{ param: string }>()

const diagram = `sequenceDiagram
  autonumber
  participant A as Alice
  participant B as Bob
  participant N as NoM ptlc

  A->>B: T, d, amounts, expiries E_A > E_B
  A->>N: Create(E_A, point, T + d·G, destination B), QSR
  Note over B: waits until Alice's entry is confirmed, checks every field
  B->>N: Create(E_B, point, T, destination A), ZNN
  Note over A: checks every field, and that E_B is more than a minute away
  A->>N: Unlock(Bob's id, t)
  N-->>A: ZNN
  B->>N: reads t from Alice's send block
  B->>N: Unlock(Alice's id, t + d)
  N-->>B: QSR
  alt Alice never unlocks
    B->>N: Reclaim(Bob's id) at E_B
    A->>N: Reclaim(Alice's id) at E_A
  end`

/** Bob's time to claim hers once t is out: her entry must outlast his by this. */
const MARGIN = 120

interface SwapRecord {
  alice: SignerId
  bob: SignerId
  secretId: string
  d: string
  znn: string
  qsr: string
  aliceMinutes: number
  bobMinutes: number
  bobEntry: string
  aliceEntry: string
  abandoned: boolean
}

// v2: the order changed, and a walk saved under v1 is half a swap in the old
// order. It is not resumed.
const KEY = 'ptlc-lab.swap.v2'
const blank = (): SwapRecord => ({
  alice: 'lab:alice',
  bob: 'lab:bob',
  secretId: '',
  d: '',
  znn: '10',
  qsr: '100',
  aliceMinutes: 20,
  bobMinutes: 10,
  bobEntry: '',
  aliceEntry: '',
  abandoned: false,
})

function load(): SwapRecord {
  try {
    const stored = JSON.parse(localStorage.getItem(KEY) ?? 'null')
    if (stored && typeof stored === 'object') return { ...blank(), ...stored }
  } catch {
    /* start blank */
  }
  return blank()
}

const swap = reactive(load())
watch(swap, () => {
  try {
    localStorage.setItem(KEY, JSON.stringify(swap))
  } catch {
    /* the walk just will not survive a reload */
  }
})

const alice = computed(() => signerView(swap.alice))
const bob = computed(() => signerView(swap.bob))
const secret = computed(() => state.secrets.find((s) => s.id === swap.secretId) ?? null)
const T = computed(() => secret.value?.lock ?? '')
const Tprime = computed(() => {
  if (!secret.value || !swap.d) return ''
  try {
    return toHex(tweakPoint(fromHex(secret.value.lock), fromHex(swap.d)))
  } catch {
    return ''
  }
})
const znn = computed(() => parseUnits(swap.znn, 8))
const qsr = computed(() => parseUnits(swap.qsr, 8))

const started = computed(() => !!secret.value && !!swap.d)
const bobEntry = computed<Entry | null>(() => state.entries.find((e) => e.id === swap.bobEntry) ?? null)
const aliceEntry = computed<Entry | null>(() => state.entries.find((e) => e.id === swap.aliceEntry) ?? null)

const termsProblem = computed(() => {
  if (swap.alice === swap.bob) return 'The two parties need two different signers: one account cannot be both sides.'
  if (!alice.value.address) return `${alice.value.name} has no address yet. Connect Syrius, or give Alice's part to a lab key.`
  if (!bob.value.address) return `${bob.value.name} has no address yet. Connect Syrius, or give Bob's part to a lab key.`
  if (alice.value.address === bob.value.address) return 'Both parts resolve to the same address. Syrius is on a lab key\'s account; pick another signer for one part.'
  if (znn.value === null || znn.value <= 0n || qsr.value === null || qsr.value <= 0n) return 'Both amounts are positive numbers.'
  if (!(swap.aliceMinutes > swap.bobMinutes + MARGIN / 60)) {
    return `Alice holds t, so she locks first and her entry has to outlast Bob's: hers must expire more than ${MARGIN / 60} minutes after his.`
  }
  if (swap.bobMinutes * 60 < 2 * REVEAL_MARGIN) return `Bob's entry needs a few minutes at least: Alice will not unlock it with under ${REVEAL_MARGIN} seconds left.`
  return null
})

function begin() {
  const t = addSecret(`swap t, ${new Date().toISOString().slice(11, 19)}`, POINT_SECP256K1, randomScalar(), 'generated')
  swap.secretId = t.id
  swap.d = toHex(randomScalar())
  swap.bobEntry = ''
  swap.aliceEntry = ''
  swap.abandoned = false
}

function startOver() {
  Object.assign(swap, { ...blank(), alice: swap.alice, bob: swap.bob, znn: swap.znn, qsr: swap.qsr })
}

// ---- what each party checks on the other's entry ----------------------------------

interface Check {
  ok: boolean
  text: string
}

/** When Bob's entry will expire if he locks now. */
const bobExpiryIfNow = computed(() => chainNow() + Math.round(swap.bobMinutes * 60))

/** Bob reads Alice's entry before he locks anything. */
const bobChecks = computed<Check[]>(() => {
  const e = aliceEntry.value
  if (!e) return []
  const outlasts = e.expirationTime - (bobEntry.value?.expirationTime ?? bobExpiryIfNow.value)
  return [
    { ok: e.pointType === POINT_SECP256K1 && e.pointLock === Tprime.value, text: 'It is locked to T + d·G, which he can work out from T and d.' },
    { ok: e.destination === bob.value.address, text: 'It pays his address and no other.' },
    { ok: e.tokenStandard === QSR_ZTS && BigInt(e.amount) === qsr.value, text: `It holds ${formatAmount(e.amount, e.tokenStandard)}, the amount agreed.` },
    { ok: e.creator === alice.value.address, text: 'Alice made it, so only Alice can take it back, and only after it expires.' },
    { ok: e.state === 'locked', text: e.state === 'locked' ? 'It is still locked.' : `It is ${e.state}.` },
    {
      ok: outlasts >= MARGIN,
      text:
        outlasts >= MARGIN
          ? `It outlasts his own by ${span(outlasts)}: whenever t comes out, he has at least that long to claim.`
          : `It would outlast his own by ${outlasts > 0 ? span(outlasts) : 'nothing'}, under the ${span(MARGIN)} he needs to claim once t is out. He does not lock.`,
    },
  ]
})

/** Alice reads Bob's entry before her secret leaves her hands. */
const aliceChecks = computed<Check[]>(() => {
  const e = bobEntry.value
  if (!e) return []
  const left = e.expirationTime - chainNow()
  return [
    { ok: e.pointType === POINT_SECP256K1 && e.pointLock === T.value, text: 'It is locked to T, the point of her secret.' },
    { ok: e.destination === alice.value.address, text: 'It pays her address and no other.' },
    { ok: e.tokenStandard === ZNN_ZTS && BigInt(e.amount) === znn.value, text: `It holds ${formatAmount(e.amount, e.tokenStandard)}, the amount agreed.` },
    { ok: e.state === 'locked', text: e.state === 'locked' ? 'It is still locked.' : `It is ${e.state}.` },
    {
      ok: left >= REVEAL_MARGIN,
      text:
        left >= REVEAL_MARGIN
          ? `It has ${span(left)} left by the chain's clock.`
          : `It has ${left > 0 ? span(left) : 'no time'} left. A call answered after the expiry is refused with t in it all the same, and Bob could take his ZNN back and her QSR too. She does not send it.`,
    },
  ]
})

const revealed = computed(() => {
  const w = bobEntry.value?.witness
  if (!w || !T.value) return null
  let matches = false
  try {
    matches = equalBytes(pointOf(fromHex(w)), fromHex(T.value))
  } catch {
    matches = false
  }
  let tweaked = ''
  try {
    tweaked = toHex(tweakScalar(fromHex(w), fromHex(swap.d)))
  } catch {
    tweaked = ''
  }
  return { t: w, matches, tweaked }
})

// ---- the calls -------------------------------------------------------------------

const busy = ref('')
const error = ref('')
/** The block each step last sent, so its button waits for the contract's answer. */
const sent = reactive<Record<string, number>>({})
const waiting = (name: string) => inFlight(sent[name])

async function act(name: string, job: () => Promise<{ n: number } | void>) {
  busy.value = name
  error.value = ''
  try {
    const activity = await job()
    if (activity) sent[name] = activity.n
  } catch (err) {
    error.value = errorText(err)
  } finally {
    busy.value = ''
  }
}

const aliceLocks = () =>
  act('aliceLocks', async () => {
    const expiry = chainNow() + Math.round(swap.aliceMinutes * 60)
    const data = packCreate(expiry, POINT_SECP256K1, fromHex(Tprime.value), bob.value.address!)
    const a = await contractCall(swap.alice, 'Create', data, `Swap: Alice locks ${swap.qsr} QSR to T + d·G for Bob`, {
      amount: qsr.value!,
      tokenStandard: QSR_ZTS,
    })
    swap.aliceEntry = a.hash!
    return a
  })

const bobLocks = () =>
  act('bobLocks', async () => {
    const expiry = chainNow() + Math.round(swap.bobMinutes * 60)
    if (aliceEntry.value!.expirationTime - expiry < MARGIN) {
      throw new Error(
        `Alice's entry would outlast this one by ${span(aliceEntry.value!.expirationTime - expiry)}. Bob needs at least ${span(MARGIN)} after t comes out, or Alice could reclaim hers before he claims it. Start over with a longer expiry for Alice.`,
      )
    }
    const data = packCreate(expiry, POINT_SECP256K1, fromHex(T.value), alice.value.address!)
    const a = await contractCall(swap.bob, 'Create', data, `Swap: Bob locks ${swap.znn} ZNN to T for Alice`, {
      amount: znn.value!,
      tokenStandard: ZNN_ZTS,
    })
    swap.bobEntry = a.hash!
    return a
  })

const aliceUnlocks = () =>
  act('aliceUnlocks', async () => {
    const left = bobEntry.value!.expirationTime - chainNow()
    if (left < REVEAL_MARGIN) {
      throw new Error(`Bob's entry has ${left > 0 ? span(left) : 'no time'} left, under the ${span(REVEAL_MARGIN)} an unlock needs. Alice keeps t; both take their own back.`)
    }
    return contractCall(swap.alice, 'Unlock', packUnlock(swap.bobEntry, fromHex(secret.value!.secret)), 'Swap: Alice unlocks Bob\'s entry, showing t')
  })

const bobUnlocks = () =>
  act('bobUnlocks', async () => {
    return contractCall(swap.bob, 'Unlock', packUnlock(swap.aliceEntry, fromHex(revealed.value!.tweaked)), 'Swap: Bob unlocks Alice\'s entry with t + d')
  })

const reclaim = (who: 'alice' | 'bob') =>
  act(`reclaim-${who}`, async () => {
    const id = who === 'alice' ? swap.aliceEntry : swap.bobEntry
    return contractCall(who === 'alice' ? swap.alice : swap.bob, 'Reclaim', packReclaim(id), `Swap: ${who === 'alice' ? 'Alice' : 'Bob'} reclaims`)
  })

const collect = (who: SignerId) => act(`collect-${who}`, async () => void (await receiveAll(who)))

// ---- where the walk is -------------------------------------------------------------

const pendingCreate = (hash: string) => !!hash && !state.entries.some((e) => e.id === hash)
const refusedCreate = (hash: string) => state.activity.find((a) => a.hash === hash && a.stage === 'refused')
const left = (e: Entry | null) => (e ? e.expirationTime - chainNow() : 0)
const done = computed(() => bobEntry.value?.state === 'paid' && aliceEntry.value?.state === 'paid')
const unwound = computed(
  () => !!swap.aliceEntry && !!aliceEntry.value && aliceEntry.value.state === 'returned' && (!swap.bobEntry || bobEntry.value?.state === 'returned'),
)

type StepState = 'done' | 'now' | 'later'
const stepState = computed<Record<string, StepState>>(() => {
  const s1: StepState = started.value ? 'done' : 'now'
  const s2: StepState = aliceEntry.value ? 'done' : started.value ? 'now' : 'later'
  const s3: StepState = bobEntry.value ? 'done' : aliceEntry.value ? 'now' : 'later'
  const s4: StepState = bobEntry.value?.state === 'paid' ? 'done' : aliceEntry.value && bobEntry.value?.state === 'locked' ? 'now' : 'later'
  const s5: StepState = aliceEntry.value?.state === 'paid' ? 'done' : s4 === 'done' ? 'now' : 'later'
  return { s1, s2, s3, s4, s5 }
})
const tagFor = (s: StepState) => (s === 'done' ? 'paid' : s === 'now' ? 'locked' : '')
const labelFor = (s: StepState) => (s === 'done' ? 'done' : s === 'now' ? 'next' : 'later')
const others = (id: SignerId) => signerIds.filter((s) => s !== id)
</script>

<template>
  <h1 class="bench-title">Swap, step by step</h1>
  <p class="bench-lede">
    Alice sells QSR for Bob's ZNN. She holds a secret <code>t</code>; they agree a tweak <code>d</code>. She locks to <code>T + d·G</code> first, Bob locks to <code>T</code> for less time, and her claim publishes the scalar that lets Bob make his. On chain the two entries share nothing.
  </p>

  <details class="panel">
    <summary>The protocol, as SPEC.md §3 draws it</summary>
    <MermaidDiagram :code="diagram" label="Sequence of the NoM-to-NoM swap: Alice locks QSR to T plus d times G for Bob, Bob checks it and locks ZNN to T for Alice with an earlier expiry, Alice unlocks Bob's entry with t, Bob reads t from the chain and unlocks Alice's entry with t plus d. If Alice never unlocks, Bob reclaims at his expiry and Alice at hers, which is later." />
  </details>

  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">Who plays whom</h2>
      <button v-if="started" type="button" class="btn btn-quiet" @click="startOver">Start over</button>
    </div>
    <div class="fields-2">
      <label class="field">
        <span>Alice's part: holds t, has QSR, locks first</span>
        <select v-model="swap.alice" :disabled="started">
          <option v-for="s in others(swap.bob)" :key="s" :value="s">{{ signerView(s).name }}, {{ signerView(s).kind }}</option>
        </select>
      </label>
      <label class="field">
        <span>Bob's part: has ZNN</span>
        <select v-model="swap.bob" :disabled="started">
          <option v-for="s in others(swap.alice)" :key="s" :value="s">{{ signerView(s).name }}, {{ signerView(s).kind }}</option>
        </select>
      </label>
    </div>
    <div class="fields-2">
      <label class="field">
        <span>Bob's ZNN</span>
        <input type="text" v-model="swap.znn" :disabled="started" inputmode="decimal" />
      </label>
      <label class="field">
        <span>Alice's QSR</span>
        <input type="text" v-model="swap.qsr" :disabled="started" inputmode="decimal" />
      </label>
      <label class="field">
        <span>Alice's entry expires after, minutes</span>
        <input type="number" v-model.number="swap.aliceMinutes" :disabled="!!swap.aliceEntry" min="5" />
      </label>
      <label class="field">
        <span>Bob's entry expires after, minutes</span>
        <input type="number" v-model.number="swap.bobMinutes" :disabled="!!swap.bobEntry" min="2" />
      </label>
    </div>
    <p class="small" style="margin-top: 8px; max-width: 68ch">
      Short expiries are what make the refund watchable: set Alice's to 6 and Bob's to 2, stop after step 3, and both entries can be reclaimed within minutes, Bob's first. The CLI cannot go below an hour.
    </p>
    <p v-if="termsProblem" class="note bad" style="margin-top: 10px">{{ termsProblem }}</p>
  </section>

  <ol class="steps">
    <li class="panel" :class="stepState.s1">
      <div class="panel-head">
        <h2 class="section-title">Alice makes t, and they agree d</h2>
        <span class="tag" :class="tagFor(stepState.s1)">{{ labelFor(stepState.s1) }}</span>
      </div>
      <p class="small prose">Off chain. In a real swap this is the first message between two wallets; here both halves are in one browser.</p>
      <dl v-if="started" class="kv" style="margin-top: 10px">
        <dt>T = t·G</dt>
        <dd><HexValue :value="T" full /></dd>
        <dt>d</dt>
        <dd><HexValue :value="swap.d" full /></dd>
        <dt>T + d·G</dt>
        <dd><HexValue :value="Tprime" full /></dd>
        <dt>t</dt>
        <dd class="small">held by Alice only, in <a href="#/secrets">Secrets</a> as “{{ secret?.name }}”</dd>
      </dl>
      <div v-else class="btn-row" style="margin-top: 12px">
        <button type="button" class="btn btn-primary" :disabled="!!termsProblem" @click="begin">Make t and d</button>
      </div>
    </li>

    <li class="panel" :class="stepState.s2">
      <div class="panel-head">
        <h2 class="section-title">Alice locks QSR to T + d·G, payable to Bob</h2>
        <span class="tag" :class="tagFor(stepState.s2)">{{ labelFor(stepState.s2) }}</span>
      </div>
      <p class="small prose">
        Alice goes first because she holds <code>t</code>. If Bob locked first she could take his ZNN with <code>t</code> and never lock anything, or wait out her own entry, take it back, and take his after. Her entry is opened by <code>t + d</code>, which Bob can work out only once she has shown <code>t</code>.
      </p>
      <template v-if="aliceEntry">
        <p class="small" style="margin-top: 8px">
          Entry <a :href="`#/entries/${aliceEntry.id}`" class="data">{{ short(aliceEntry.id) }}</a>,
          <span class="tag" :class="aliceEntry.state">{{ aliceEntry.state }}</span>, expiry {{ clock(aliceEntry.expirationTime, state.now) }}
        </p>
      </template>
      <p v-else-if="refusedCreate(swap.aliceEntry)" class="note bad" style="margin-top: 8px">{{ refusedCreate(swap.aliceEntry)!.error }} Start over.</p>
      <p v-else-if="pendingCreate(swap.aliceEntry)" class="note warn" style="margin-top: 8px">
        <span class="working">Published.</span> The entry exists once the contract answers, a momentum or two from now.
      </p>
      <div v-else-if="stepState.s2 === 'now'" class="btn-row" style="margin-top: 12px">
        <button type="button" class="btn btn-primary" :disabled="!!busy || !!termsProblem || !!alice.blocked" @click="aliceLocks">
          {{ busy === 'aliceLocks' ? 'Signing' : `Lock ${swap.qsr} QSR as ${alice.name}` }}
        </button>
        <span v-if="alice.blocked" class="small">{{ alice.blocked }}</span>
      </div>
    </li>

    <li class="panel" :class="stepState.s3">
      <div class="panel-head">
        <h2 class="section-title">Bob checks it, then locks ZNN to T, payable to Alice</h2>
        <span class="tag" :class="tagFor(stepState.s3)">{{ labelFor(stepState.s3) }}</span>
      </div>
      <p class="small prose">
        Bob's entry ends first. Whenever Alice claims it, and so shows <code>t</code>, hers is still open to him for at least {{ span(MARGIN) }}.
      </p>
      <ul v-if="bobChecks.length && stepState.s3 !== 'done'" class="checks">
        <li v-for="c in bobChecks" :key="c.text" :class="{ no: !c.ok }">{{ c.text }}</li>
      </ul>
      <template v-if="bobEntry">
        <p class="small" style="margin-top: 8px">
          Entry <a :href="`#/entries/${bobEntry.id}`" class="data">{{ short(bobEntry.id) }}</a>,
          <span class="tag" :class="bobEntry.state">{{ bobEntry.state }}</span>, expiry {{ clock(bobEntry.expirationTime, state.now) }}
        </p>
      </template>
      <p v-else-if="refusedCreate(swap.bobEntry)" class="note bad" style="margin-top: 8px">{{ refusedCreate(swap.bobEntry)!.error }}</p>
      <p v-else-if="pendingCreate(swap.bobEntry)" class="note warn" style="margin-top: 8px">
        <span class="working">Published.</span> Waiting for the contract to answer.
      </p>
      <div v-else-if="stepState.s3 === 'now'" class="btn-row" style="margin-top: 12px">
        <button type="button" class="btn btn-primary" :disabled="!!busy || !bobChecks.every((c) => c.ok) || !!bob.blocked" @click="bobLocks">
          {{ busy === 'bobLocks' ? 'Signing' : `Lock ${swap.znn} ZNN as ${bob.name}` }}
        </button>
        <span v-if="bob.blocked" class="small">{{ bob.blocked }}</span>
      </div>
    </li>

    <li class="panel" :class="stepState.s4">
      <div class="panel-head">
        <h2 class="section-title">Alice checks it, then unlocks Bob's entry with t</h2>
        <span class="tag" :class="tagFor(stepState.s4)">{{ labelFor(stepState.s4) }}</span>
      </div>
      <p class="small prose">
        This is the moment the swap commits. Her call carries <code>t</code> in the clear, so from here Bob can take her QSR whether or not she wants him to. She must send it at least {{ span(REVEAL_MARGIN) }} before Bob's entry expires<template v-if="bobEntry && bobEntry.state === 'locked'">, {{ left(bobEntry) > 0 ? `${span(left(bobEntry))} from now` : 'which has already happened' }}</template>: a call the contract answers after that is refused, and gives <code>t</code> away all the same.
      </p>
      <ul v-if="aliceChecks.length && stepState.s4 !== 'done'" class="checks">
        <li v-for="c in aliceChecks" :key="c.text" :class="{ no: !c.ok }">{{ c.text }}</li>
      </ul>
      <p v-if="stepState.s4 === 'now' && waiting('aliceUnlocks')" class="note warn" style="margin-top: 10px">
        <span class="working">Published.</span> From this moment t is public. Waiting for the contract to pay her.
      </p>
      <div v-else-if="stepState.s4 === 'now' && !swap.abandoned" class="btn-row" style="margin-top: 12px">
        <button type="button" class="btn btn-primary" :disabled="!!busy || !!alice.blocked || !aliceChecks.every((c) => c.ok)" @click="aliceUnlocks">
          {{ busy === 'aliceUnlocks' ? 'Signing' : `Unlock as ${alice.name}, showing t` }}
        </button>
        <button type="button" class="btn" :disabled="!!busy" @click="swap.abandoned = true">Walk away instead</button>
      </div>
      <p v-if="stepState.s4 === 'done'" class="small" style="margin-top: 8px">
        Paid to <Who :address="bobEntry!.paidTo ?? ''" />. The ZNN is an unreceived block until she receives it.
      </p>
    </li>

    <li class="panel" :class="stepState.s5">
      <div class="panel-head">
        <h2 class="section-title">Bob reads t off the chain, adds d, and unlocks Alice's entry</h2>
        <span class="tag" :class="tagFor(stepState.s5)">{{ labelFor(stepState.s5) }}</span>
      </div>
      <p class="small prose">
        He reads the contract's chain, not Alice's: the unlock could have been sent by anyone. He takes only the witness of the unlock the contract accepted, and checks it against <code>T</code> before using it; a refused call with a junk witness is anyone's to send. Nothing passes between the two of them at this step.
      </p>
      <dl v-if="revealed" class="kv" style="margin-top: 10px">
        <dt>t, from the chain</dt>
        <dd>
          <HexValue :value="revealed.t" full />
          <span class="tag" :class="revealed.matches ? 'paid' : 'refused'">{{ revealed.matches ? 't·G is T' : 'not the scalar of T' }}</span>
        </dd>
        <dt>t + d</dt>
        <dd><HexValue :value="revealed.tweaked" full /></dd>
      </dl>
      <p v-if="stepState.s5 === 'now' && waiting('bobUnlocks')" class="note warn" style="margin-top: 10px">
        <span class="working">Published.</span> Waiting for the contract to pay him.
      </p>
      <div v-else-if="stepState.s5 === 'now' && revealed" class="btn-row" style="margin-top: 12px">
        <button type="button" class="btn btn-primary" :disabled="!!busy || !!bob.blocked || !revealed.matches || left(aliceEntry) <= 0" @click="bobUnlocks">
          {{ busy === 'bobUnlocks' ? 'Signing' : `Unlock as ${bob.name} with t + d` }}
        </button>
        <span v-if="left(aliceEntry) > 0" class="small">Alice's entry is open to him for {{ span(left(aliceEntry)) }} more.</span>
        <span v-else class="small">Alice's entry has expired. With the expiries in this order that takes Bob more than {{ span(MARGIN) }} of not claiming.</span>
      </div>
      <p v-if="stepState.s5 === 'done'" class="small" style="margin-top: 8px">
        Paid to <Who :address="aliceEntry!.paidTo ?? ''" />.
      </p>
    </li>
  </ol>

  <section v-if="done" class="panel">
    <h2 class="section-title">Swapped</h2>
    <p class="prose small">
      Alice has Bob's ZNN and Bob has Alice's QSR. The chain shows two entries, locked to
      <span class="data">{{ short(T, 8, 6) }}</span> and <span class="data">{{ short(Tprime, 8, 6) }}</span>, opened with two different scalars. Without <code>d</code>, nothing ties them together.
    </p>
    <p class="small" style="margin-top: 8px">A payout is an unreceived block until its destination publishes a receive.</p>
    <div class="btn-row" style="margin-top: 10px">
      <button v-if="swap.alice !== 'syrius'" type="button" class="btn" :disabled="!!busy" @click="collect(swap.alice)">Receive for {{ alice.name }}</button>
      <button v-if="swap.bob !== 'syrius'" type="button" class="btn" :disabled="!!busy" @click="collect(swap.bob)">Receive for {{ bob.name }}</button>
      <span v-if="swap.alice === 'syrius' || swap.bob === 'syrius'" class="small">Syrius receives the next time its dashboard is opened.</span>
    </div>
  </section>

  <section v-if="swap.abandoned && !done" class="panel">
    <h2 class="section-title">{{ unwound ? 'Unwound' : 'Walking away' }}</h2>
    <p class="prose small">
      Alice never showed <code>t</code>, so nobody can open either entry. Each creator takes their own back once it expires, with no help from the other. Alice waits longer, which is the price of holding the secret: her entry had to outlast Bob's.
    </p>
    <div class="table-scroll" style="margin-top: 10px">
      <table class="grid">
        <tbody>
          <tr v-if="bobEntry">
            <td>Bob's ZNN</td>
            <td><span class="tag" :class="bobEntry.state">{{ bobEntry.state }}</span></td>
            <td class="small">{{ bobEntry.state === 'locked' ? (left(bobEntry) > 0 ? `reclaimable in ${span(left(bobEntry))}` : 'reclaimable now') : '' }}</td>
            <td>
              <button v-if="bobEntry.state === 'locked'" type="button" class="btn btn-quiet" :disabled="!!busy || !!bob.blocked || waiting('reclaim-bob')" @click="reclaim('bob')">
                {{ left(bobEntry) > 0 ? 'Try to reclaim early' : `Reclaim as ${bob.name}` }}
              </button>
            </td>
          </tr>
          <tr v-if="aliceEntry">
            <td>Alice's QSR</td>
            <td><span class="tag" :class="aliceEntry.state">{{ aliceEntry.state }}</span></td>
            <td class="small">{{ aliceEntry.state === 'locked' ? (left(aliceEntry) > 0 ? `reclaimable in ${span(left(aliceEntry))}` : 'reclaimable now') : '' }}</td>
            <td>
              <button v-if="aliceEntry.state === 'locked'" type="button" class="btn btn-quiet" :disabled="!!busy || !!alice.blocked || waiting('reclaim-alice')" @click="reclaim('alice')">
                {{ left(aliceEntry) > 0 ? 'Try to reclaim early' : `Reclaim as ${alice.name}` }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="btn-row" style="margin-top: 10px">
      <button v-if="!unwound && bobEntry?.state === 'locked' && aliceEntry?.state === 'locked'" type="button" class="btn btn-quiet" @click="swap.abandoned = false">
        Go on with the swap after all
      </button>
    </div>
  </section>

  <p v-if="error" class="note bad" style="margin-top: 12px" role="alert">{{ error }}</p>
</template>

<style scoped>
.steps {
  list-style: none;
  margin: 0;
  padding: 0;
  counter-reset: step;
}
.steps > li {
  counter-increment: step;
  position: relative;
  padding-left: 58px;
}
.steps > li::before {
  content: counter(step);
  position: absolute;
  left: 16px;
  top: 14px;
  font-family: var(--font-mono);
  font-size: 1.2rem;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--line-strong);
  transition: color 300ms var(--ease);
}
/* The step to take now: lit, where the others are only ruled. */
.steps > li.now {
  border-color: color-mix(in srgb, var(--znn) 55%, transparent);
  box-shadow:
    var(--edge),
    0 0 0 3px color-mix(in srgb, var(--znn) 12%, transparent),
    var(--shadow-panel);
}
.steps > li.now::before {
  color: var(--znn-ink);
}
.steps > li.done::before {
  color: var(--paid);
}
.steps > li.later {
  opacity: 0.72;
}
.checks {
  list-style: none;
  margin: 10px 0 0;
  padding: 0;
  font-size: 0.88rem;
}
.checks li {
  padding-left: 20px;
  position: relative;
  margin-top: 3px;
}
.checks li::before {
  content: '';
  position: absolute;
  left: 2px;
  top: 0.52em;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--paid);
}
.checks li.no::before {
  background: var(--refused);
  border-radius: 1px;
}
details.panel > summary {
  font-size: 1rem;
}
@media (max-width: 760px) {
  .steps > li {
    padding-left: 18px;
    padding-top: 40px;
  }
  .steps > li::before {
    top: 8px;
  }
}
</style>
