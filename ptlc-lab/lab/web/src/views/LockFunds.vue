<script setup lang="ts">
// Create: lock funds to a point until a time.
import { computed, reactive, ref, watch } from 'vue'
import CallPreview from '../components/CallPreview.vue'
import HexValue from '../components/HexValue.vue'
import {
  POINT_TYPES,
  destinationProblem,
  lockProblem,
  packCreate,
  pointTypeInfo,
} from '../ptlc/contract.ts'
import {
  chainNow,
  contractCall,
  formatAmount,
  formatUnits,
  loadToken,
  nameOf,
  parseUnits,
  signerView,
  state,
  tokenOf,
  trackedAddresses,
  type Activity,
} from '../store.ts'
import { EMPTY_ADDRESS, QSR_ZTS, ZNN_ZTS, fromHex, isAddress, isHex, isZts } from '../znn/primitives.ts'
import { cliToken, clock, errorText, span } from '../lib.ts'

defineProps<{ param: string }>()

const form = reactive({
  token: ZNN_ZTS,
  customToken: '',
  amount: '1',
  expiryValue: 10,
  expiryUnit: 60,
  pointType: 2,
  lockSource: '' as string, // a secret id, or 'paste'
  lockHex: '',
  destination: '' as string, // an address, 'zero' or 'custom'
  customDestination: '',
})

const signer = computed(() => signerView(state.signer))
const zts = computed(() => (form.token === 'custom' ? form.customToken.trim() : form.token))
watch(zts, (value) => {
  if (isZts(value)) void loadToken(value)
})
const token = computed(() => (isZts(zts.value) ? tokenOf(zts.value) : null))
const amount = computed(() => (token.value ? parseUnits(form.amount, token.value.decimals) : null))
const balance = computed(() => {
  const address = signer.value.address
  if (!address || !isZts(zts.value)) return null
  return BigInt(state.accounts[address]?.balances[zts.value] ?? '0')
})

const secrets = computed(() => state.secrets.filter((s) => s.pointType === form.pointType))
watch(
  () => [form.pointType, secrets.value.length],
  () => {
    // Keep a valid choice selected as the type changes or a secret is made.
    if (form.lockSource !== 'paste' && !secrets.value.some((s) => s.id === form.lockSource)) {
      form.lockSource = secrets.value[0]?.id ?? 'paste'
    }
    if (form.pointType === 2 && form.destination === 'zero') form.destination = ''
  },
  { immediate: true },
)

const lockHex = computed(() => {
  if (form.lockSource === 'paste') return form.lockHex.trim().toLowerCase()
  return secrets.value.find((s) => s.id === form.lockSource)?.lock ?? ''
})
const destination = computed(() => {
  if (form.destination === 'zero') return EMPTY_ADDRESS
  if (form.destination === 'custom') return form.customDestination.trim()
  return form.destination
})
const expirationTime = computed(() => chainNow() + Math.round(form.expiryValue * form.expiryUnit))

const known = computed(() =>
  trackedAddresses()
    .filter((a) => a !== signer.value.address)
    .map((a) => ({ address: a, name: nameOf(a) ?? a })),
)

const problems = computed(() => {
  const list: string[] = []
  if (!isZts(zts.value)) list.push('Name the token: ZNN, QSR, or a zts1… token standard.')
  else if (!token.value) list.push('The node does not know this token.')
  if (token.value && (amount.value === null || amount.value <= 0n)) {
    list.push(`The amount is a positive number with at most ${token.value.decimals} decimal places.`)
  }
  if (!(form.expiryValue > 0)) list.push('The expiry is some time after now.')
  if (!lockHex.value) list.push(form.lockSource === 'paste' ? 'Paste the lock, in hex.' : 'Make a secret of this kind on the Secrets bench, or paste a lock.')
  else if (!isHex(lockHex.value)) list.push('The lock is hex.')
  else {
    const p = lockProblem(form.pointType, fromHex(lockHex.value))
    if (p) list.push(`The contract would refuse this lock: ${p}.`)
  }
  if (!destination.value) list.push('Choose who the entry pays.')
  else if (!isAddress(destination.value)) list.push('The destination is not a Zenon address.')
  else {
    const p = destinationProblem(form.pointType, destination.value)
    if (p) list.push(`The contract would refuse this destination: ${p}.`)
  }
  return list
})

const warnings = computed(() => {
  const list: string[] = []
  if (amount.value !== null && balance.value !== null && amount.value > balance.value && token.value) {
    list.push(`${signer.value.name} holds ${formatUnits(balance.value, token.value.decimals)} ${token.value.symbol}; the node will refuse a block that sends more.`)
  }
  if (form.expiryValue * form.expiryUnit < 60) {
    list.push('A contract call is answered a momentum or two after it is sent, 15 to 30 s here. An expiry this close may have passed by then, and the Create would be refused.')
  }
  if (form.lockSource === 'paste' && lockHex.value && !state.secrets.some((s) => s.lock === lockHex.value)) {
    list.push('This browser does not hold the secret for that lock. Whoever does can open the entry; you will only be able to reclaim it.')
  }
  if (isAddress(destination.value) && destination.value === signer.value.address) {
    list.push('The entry pays its own creator. That works, and is only useful for trying the calls.')
  }
  return list
})

const data = computed(() => {
  if (problems.value.length) return null
  try {
    return packCreate(expirationTime.value, form.pointType, fromHex(lockHex.value), destination.value)
  } catch {
    return null
  }
})

const cli = computed(() => {
  const seconds = form.expiryValue * form.expiryUnit
  if (!data.value || !token.value || seconds % 3600 !== 0 || seconds < 3600 || seconds > 168 * 3600) return null
  const dest = destination.value === EMPTY_ADDRESS ? '' : ` ${destination.value}`
  return `ptlc.create ${cliToken(zts.value)} ${form.amount.trim()} ${seconds / 3600} ${form.pointType} ${lockHex.value}${dest}`
})
const cliNote = computed(() =>
  cli.value
    ? `Signed by whichever keystore index the CLI is run with. The CLI adds the hours to the frontier momentum's timestamp, as this page does.`
    : 'znn-cli takes the expiry in whole hours, from 1 to 168. This entry expires sooner than that, which only the page and the Go client can ask for.',
)

const sending = ref(false)
const error = ref('')
const sent = ref<Activity | null>(null)

async function send() {
  if (!data.value || amount.value === null) return
  sending.value = true
  error.value = ''
  sent.value = null
  try {
    sent.value = await contractCall(
      state.signer,
      'Create',
      data.value,
      `Lock ${formatAmount(amount.value, zts.value)} to a ${pointTypeInfo(form.pointType)!.name}`,
      { amount: amount.value, tokenStandard: zts.value },
    )
  } catch (err) {
    error.value = errorText(err)
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <h1 class="bench-title">Lock funds</h1>
  <p class="bench-lede">
    Create sends funds to the contract with a lock, an expiry and, for a point lock, the one address it pays. The hash of the block that carries it is the entry's id.
  </p>

  <section class="panel">
    <h2 class="section-title">What to lock</h2>
    <div class="fields-2">
      <label class="field">
        <span>Token</span>
        <select v-model="form.token">
          <option :value="ZNN_ZTS">ZNN</option>
          <option :value="QSR_ZTS">QSR</option>
          <option value="custom">another token</option>
        </select>
      </label>
      <label v-if="form.token === 'custom'" class="field">
        <span>Token standard</span>
        <input type="text" v-model="form.customToken" spellcheck="false" placeholder="zts1…" />
      </label>
      <label class="field">
        <span>Amount</span>
        <input type="text" v-model="form.amount" inputmode="decimal" />
        <span v-if="balance !== null && token" class="hint">{{ signer.name }} holds {{ formatUnits(balance, token.decimals) }} {{ token.symbol }}</span>
      </label>
    </div>
    <div class="fields-2">
      <label class="field">
        <span>Expires after</span>
        <input type="number" v-model.number="form.expiryValue" min="1" step="1" />
      </label>
      <label class="field">
        <span>Unit</span>
        <select v-model.number="form.expiryUnit">
          <option :value="1">seconds</option>
          <option :value="60">minutes</option>
          <option :value="3600">hours</option>
        </select>
      </label>
    </div>
    <p class="small" style="margin-top: 6px">
      Unlockable until, and reclaimable from, <strong>{{ clock(expirationTime, state.now) }}</strong> by the chain's clock, {{ span(form.expiryValue * form.expiryUnit) }} after the frontier momentum. The page reads that timestamp again when you press the button.
    </p>
  </section>

  <section class="panel">
    <h2 class="section-title">The lock</h2>
    <div class="choice" role="radiogroup" aria-label="Point type">
      <label v-for="p in POINT_TYPES" :key="p.type">
        <input type="radio" name="pointType" :value="p.type" v-model="form.pointType" />
        <strong><span class="data">{{ p.type }}</span> {{ p.name }}</strong>
        <small>Opens with {{ p.witness }}.</small>
      </label>
    </div>
    <label class="field">
      <span>Lock to</span>
      <select v-model="form.lockSource">
        <option v-for="s in secrets" :key="s.id" :value="s.id">{{ s.name }}, held in this browser</option>
        <option value="paste">a lock someone gave me</option>
      </select>
      <span v-if="secrets.length === 0" class="hint">
        This browser holds no secret of this kind. <a href="#/secrets">Make one</a>, or paste a counterparty's lock.
      </span>
    </label>
    <label v-if="form.lockSource === 'paste'" class="field">
      <span>The lock, {{ pointTypeInfo(form.pointType)!.lockSize }} bytes of hex</span>
      <input type="text" v-model="form.lockHex" spellcheck="false" :placeholder="pointTypeInfo(form.pointType)!.lock" />
    </label>
    <p v-else-if="lockHex" class="small" style="margin-top: 8px">lock <HexValue :value="lockHex" full /></p>
  </section>

  <section class="panel">
    <h2 class="section-title">Who it pays</h2>
    <label class="field">
      <span>Destination</span>
      <select v-model="form.destination">
        <option value="" disabled>choose who can be paid</option>
        <option v-for="k in known" :key="k.address" :value="k.address">{{ k.name }}, {{ k.address }}</option>
        <option v-if="signer.address" :value="signer.address">{{ signer.name }}, the creator</option>
        <option value="custom">another address</option>
        <option value="zero" :disabled="form.pointType === 2">any address the signature names</option>
      </select>
      <span class="hint" v-if="form.pointType === 2">
        A point lock must name its destination. Its witness is a bare scalar, public one momentum before the contract acts on it; with no fixed destination anyone who saw it could claim first.
      </span>
      <span class="hint" v-else>
        A key lock may leave the destination open: the signature names it, and can only pay the address it names.
      </span>
    </label>
    <label v-if="form.destination === 'custom'" class="field">
      <span>Address</span>
      <input type="text" v-model="form.customDestination" spellcheck="false" placeholder="z1…" />
    </label>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">Send it</h2>
      <span class="small">signing as <strong>{{ signer.name }}</strong>, {{ signer.kind }}</span>
    </div>
    <ul v-if="problems.length" class="issues">
      <li v-for="p in problems" :key="p">{{ p }}</li>
    </ul>
    <p v-for="w in warnings" :key="w" class="note warn" style="margin-top: 8px">{{ w }}</p>
    <p v-if="signer.blocked" class="note bad" style="margin-top: 8px">
      {{ signer.name }} cannot sign right now: {{ signer.blocked }}. <a href="#/wallet">Sort it out</a>, or pick another signer in the header.
    </p>

    <CallPreview method="Create" :data="data" :amount="amount ?? 0n" :token-standard="zts" :cli="cli" :cli-note="cliNote" />

    <div class="btn-row" style="margin-top: 14px">
      <button type="button" class="btn btn-primary" :disabled="!data || sending || !!signer.blocked" @click="send">
        {{ sending ? (state.signer === 'syrius' ? 'Waiting for Syrius' : 'Signing') : `Lock the funds as ${signer.name}` }}
      </button>
      <span v-if="sending && state.signer === 'syrius'" class="small">
        Approve the block in the Syrius window. It will name the contract as unrecognized; that is expected.
      </span>
    </div>
    <p v-if="error" class="note bad" style="margin-top: 10px" role="alert">{{ error }}</p>
    <p v-if="sent?.hash" class="note good" style="margin-top: 10px">
      Published. The entry's id is the block's hash, <a :href="`#/entries/${sent.hash}`" class="data">{{ sent.hash.slice(0, 16) }}…</a>, and it exists once the contract has answered: follow it on the right.
    </p>
  </section>
</template>

<style scoped>
.issues {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--soft);
  font-size: 0.9rem;
}
</style>
