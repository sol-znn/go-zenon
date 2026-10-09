<script setup lang="ts">
// Everything a swap needs around the contract calls, done in the page: making
// a secret and its lock, tweaking a point so two legs look unrelated, working
// out the message a key-type witness signs, and the adaptor signature that
// carries a scalar to Bitcoin.
import { computed, reactive, ref } from 'vue'
import HexValue from '../components/HexValue.vue'
import { POINT_TYPES, pointTypeInfo, unlockMessage, unlockPreimage } from '../ptlc/contract.ts'
import {
  adaptorComplete,
  adaptorExtract,
  adaptorSign,
  adaptorVerify,
  bip340PublicKey,
  bip340Verify,
  makeWitness,
  pointOf,
  randomBytes,
  randomScalar,
  randomSecret,
  scalarProblem,
  tweakPoint,
  tweakScalar,
} from '../ptlc/curve.ts'
import { addSecret, chainId, removeSecret, signerView, state, trackedAddresses, nameOf } from '../store.ts'
import { equalBytes, fromHex, isAddress, isHex, toHex, EMPTY_ADDRESS } from '../znn/primitives.ts'
import { errorText } from '../lib.ts'

defineProps<{ param: string }>()

// ---- the list ------------------------------------------------------------------

const newType = ref(2)
const newName = ref('')
const importHex = ref('')
const importError = ref('')
const revealed = reactive(new Set<string>())

function nextName(pointType: number): string {
  const base = pointType === 2 ? 't' : pointType === 1 ? 'schnorr key' : 'ed25519 key'
  const n = state.secrets.filter((s) => s.pointType === pointType).length + 1
  return `${base} ${n}`
}

function generate() {
  addSecret(newName.value.trim() || nextName(newType.value), newType.value, randomSecret(newType.value), 'generated')
  newName.value = ''
}

function importSecret() {
  importError.value = ''
  const hex = importHex.value.trim()
  if (!isHex(hex, 32)) {
    importError.value = 'A secret is 32 bytes: 64 hex characters.'
    return
  }
  const bytes = fromHex(hex)
  if (newType.value !== 0) {
    const problem = scalarProblem(bytes)
    if (problem) {
      importError.value = `Not a usable secp256k1 secret: ${problem}.`
      return
    }
  }
  addSecret(newName.value.trim() || nextName(newType.value), newType.value, bytes, 'imported')
  importHex.value = ''
  newName.value = ''
}

const toggle = (id: string) => (revealed.has(id) ? revealed.delete(id) : revealed.add(id))

// ---- tweak ---------------------------------------------------------------------

const pointSecrets = computed(() => state.secrets.filter((s) => s.pointType === 2))
const tweak = reactive({ base: '', d: '' })
const tweakResult = computed(() => {
  const base = pointSecrets.value.find((s) => s.id === tweak.base)
  if (!base || !isHex(tweak.d.trim(), 32)) return { value: null, error: '' }
  try {
    const d = fromHex(tweak.d.trim())
    const point = tweakPoint(fromHex(base.lock), d)
    const scalar = tweakScalar(fromHex(base.secret), d)
    return { value: { base, point: toHex(point), scalar: toHex(scalar), agrees: equalBytes(pointOf(scalar), point) }, error: '' }
  } catch (err) {
    return { value: null, error: errorText(err) }
  }
})
const tweaked = computed(() => tweakResult.value.value)
function saveTweaked() {
  if (!tweaked.value) return
  addSecret(`${tweaked.value.base.name} + d`, 2, fromHex(tweaked.value.scalar), 'derived')
}

// ---- unlock message --------------------------------------------------------------

const msg = reactive({ pointType: 1, id: '', destination: '', secret: '' })
const destinations = computed(() => [
  ...trackedAddresses().map((a) => ({ address: a, name: nameOf(a) ?? a })),
  { address: EMPTY_ADDRESS, name: 'the zero address' },
])
const message = computed(() => {
  const id = msg.id.trim()
  if (!isHex(id, 32) || !isAddress(msg.destination)) return null
  return {
    preimage: toHex(unlockPreimage(chainId(), msg.pointType, id, msg.destination)),
    digest: toHex(unlockMessage(chainId(), msg.pointType, id, msg.destination)),
  }
})
const keySecrets = computed(() => state.secrets.filter((s) => s.pointType === msg.pointType))
const signature = computed(() => {
  const secret = keySecrets.value.find((s) => s.id === msg.secret)
  if (!message.value || !secret) return null
  return toHex(makeWitness(msg.pointType, fromHex(secret.secret), chainId(), msg.id.trim(), msg.destination))
})

// ---- adaptor ---------------------------------------------------------------------

const ad = reactive({ signer: '', message: '', adaptor: '', presig: '', completeWith: '', signature: '', error: '' })
const schnorrSecrets = computed(() => state.secrets.filter((s) => s.pointType !== 0))
function adaptorMake() {
  ad.error = ''
  try {
    const sk = schnorrSecrets.value.find((s) => s.id === ad.signer)
    const target = pointSecrets.value.find((s) => s.id === ad.adaptor)
    if (!sk || !target) throw new Error('Pick the key that signs and the point it is locked to.')
    if (!isHex(ad.message.trim(), 32)) throw new Error('The message is 32 bytes of hex: a Bitcoin sighash, in a swap.')
    ad.presig = toHex(adaptorSign(fromHex(sk.secret), fromHex(ad.message.trim()), fromHex(target.lock), randomBytes(32)))
    ad.signature = ''
  } catch (err) {
    ad.error = errorText(err)
  }
}
const adaptorState = computed(() => {
  const sk = schnorrSecrets.value.find((s) => s.id === ad.signer)
  const target = pointSecrets.value.find((s) => s.id === ad.adaptor)
  if (!sk || !target || !isHex(ad.presig, 65) || !isHex(ad.message.trim(), 32)) return null
  const pk = bip340PublicKey(fromHex(sk.secret))
  const m = fromHex(ad.message.trim())
  const pre = fromHex(ad.presig)
  return {
    publicKey: toHex(pk),
    preVerifies: adaptorVerify(pk, m, fromHex(target.lock), pre),
    preIsSignature: bip340Verify(pk, m, pre.subarray(1)),
    sigVerifies: isHex(ad.signature, 64) ? bip340Verify(pk, m, fromHex(ad.signature)) : null,
    extracted: isHex(ad.signature, 64) ? toHex(adaptorExtract(pre, fromHex(ad.signature))) : null,
    target,
  }
})
function adaptorFinish() {
  ad.error = ''
  try {
    const target = pointSecrets.value.find((s) => s.id === ad.adaptor)
    if (!target) throw new Error('Pick the point first.')
    ad.signature = toHex(adaptorComplete(fromHex(ad.presig), fromHex(target.secret)))
  } catch (err) {
    ad.error = errorText(err)
  }
}
const randomMessage = () => (ad.message = toHex(randomBytes(32)))
const randomTweak = () => (tweak.d = toHex(randomScalar()))
const useSigner = () => (msg.destination = signerView(state.signer).address ?? '')
</script>

<template>
  <h1 class="bench-title">Secrets and points</h1>
  <p class="bench-lede">
    A lock is made from a secret, and whoever holds the secret can open it. Make them here; the other benches pick them up by the lock they open.
  </p>
  <p class="note warn" style="margin-top: 12px">
    These are kept in this browser's storage in the clear, so that a reload does not lose an entry's only key. That is right for a devnet and wrong for anything else.
  </p>

  <section class="panel">
    <h2 class="section-title">Make or bring a secret</h2>
    <div class="choice" role="radiogroup" aria-label="Kind of secret">
      <label v-for="p in POINT_TYPES" :key="p.type">
        <input type="radio" name="kind" :value="p.type" v-model="newType" />
        <strong>{{ p.name }}</strong>
        <small>opens a lock of pointType {{ p.type }}: {{ p.lock }}</small>
      </label>
    </div>
    <div class="fields-2">
      <label class="field">
        <span>Name, for your own reading</span>
        <input type="text" v-model="newName" :placeholder="nextName(newType)" />
      </label>
      <label class="field">
        <span>Bring one: 32 bytes of hex</span>
        <input type="text" v-model="importHex" placeholder="leave empty to make a new one" spellcheck="false" />
        <span v-if="importError" class="hint bad">{{ importError }}</span>
      </label>
    </div>
    <div class="btn-row" style="margin-top: 14px">
      <button type="button" class="btn btn-primary" @click="importHex.trim() ? importSecret() : generate()">
        {{ importHex.trim() ? 'Add this secret' : 'Make a secret' }}
      </button>
    </div>
  </section>

  <section class="panel">
    <h2 class="section-title">Held in this browser</h2>
    <p v-if="state.secrets.length === 0" class="small">
      None yet. Make a secp256k1 point secret above to lock funds the way the swaps do.
    </p>
    <div v-else class="table-scroll">
      <table class="grid">
        <thead>
          <tr>
            <th>Name</th>
            <th>Kind</th>
            <th>Lock, public</th>
            <th>Secret</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in state.secrets" :key="s.id">
            <td>
              <strong>{{ s.name }}</strong><br /><span class="small faint">{{ s.origin }}</span>
            </td>
            <td>{{ pointTypeInfo(s.pointType)?.name }}</td>
            <td><HexValue :value="s.lock" :head="12" :tail="8" /></td>
            <td>
              <HexValue v-if="revealed.has(s.id)" :value="s.secret" :head="12" :tail="8" />
              <span v-else class="data faint">hidden</span>
            </td>
            <td class="actions">
              <button type="button" class="btn btn-quiet" @click="toggle(s.id)">{{ revealed.has(s.id) ? 'Hide' : 'Show' }}</button>
              <button type="button" class="btn btn-quiet" @click="removeSecret(s.id)">Forget</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <section class="panel">
    <h2 class="section-title">Tweak a point</h2>
    <p class="small prose">
      Two entries locked to <code>T</code> and <code>T + d·G</code> show unrelated points and, once opened, unrelated scalars. Whoever knows <code>d</code> turns one scalar into the other by adding it. This is how the two legs of a swap are kept from being linked on chain.
    </p>
    <div class="fields-2">
      <label class="field">
        <span>The point secret <code>t</code></span>
        <select v-model="tweak.base">
          <option value="" disabled>{{ pointSecrets.length ? 'choose one' : 'make a secp256k1 point secret first' }}</option>
          <option v-for="s in pointSecrets" :key="s.id" :value="s.id">{{ s.name }}</option>
        </select>
      </label>
      <label class="field">
        <span>The tweak <code>d</code>, agreed off chain</span>
        <input type="text" v-model="tweak.d" spellcheck="false" placeholder="32 bytes of hex" />
        <span class="hint"><button type="button" class="btn btn-quiet" @click="randomTweak">Make a random one</button></span>
      </label>
    </div>
    <p v-if="tweakResult.error" class="note bad" style="margin-top: 10px">{{ tweakResult.error }}</p>
    <dl v-if="tweaked" class="kv" style="margin-top: 12px">
      <dt>T + d·G</dt>
      <dd><HexValue :value="tweaked.point" full /></dd>
      <dt>t + d</dt>
      <dd><HexValue :value="tweaked.scalar" full /></dd>
      <dt>Check</dt>
      <dd>
        <span class="tag" :class="tweaked.agrees ? 'paid' : 'refused'">{{ tweaked.agrees ? '(t + d)·G equals T + d·G' : 'the two do not agree' }}</span>
      </dd>
    </dl>
    <div v-if="tweaked" class="btn-row" style="margin-top: 12px">
      <button type="button" class="btn" @click="saveTweaked">Keep t + d as a secret</button>
    </div>
  </section>

  <section class="panel">
    <h2 class="section-title">The unlock message</h2>
    <p class="small prose">
      A key lock opens with a signature over this message. It binds the chain, the contract, the point type, the entry and the destination, so a signature made for one of them opens nothing else. The Entries bench builds it for you; this is the same thing with the parts on show.
    </p>
    <div class="fields-2">
      <label class="field">
        <span>Point type</span>
        <select v-model.number="msg.pointType">
          <option :value="0">0, Ed25519 key</option>
          <option :value="1">1, BIP-340 key</option>
        </select>
      </label>
      <label class="field">
        <span>Entry id</span>
        <input type="text" v-model="msg.id" spellcheck="false" placeholder="the hash of the Create block" />
      </label>
    </div>
    <label class="field">
      <span>Destination the signature will pay</span>
      <select v-model="msg.destination">
        <option value="" disabled>choose an address</option>
        <option v-for="d in destinations" :key="d.address" :value="d.address">{{ d.name }}, {{ d.address }}</option>
      </select>
      <span class="hint"><button type="button" class="btn btn-quiet" @click="useSigner">Use the current signer</button></span>
    </label>
    <dl v-if="message" class="kv" style="margin-top: 12px">
      <dt>101 bytes</dt>
      <dd><HexValue :value="message.preimage" :head="40" :tail="40" /></dd>
      <dt>SHA3-256</dt>
      <dd><HexValue :value="message.digest" full /></dd>
    </dl>
    <label v-if="message" class="field">
      <span>Sign it with</span>
      <select v-model="msg.secret">
        <option value="">no key</option>
        <option v-for="s in keySecrets" :key="s.id" :value="s.id">{{ s.name }}</option>
      </select>
    </label>
    <dl v-if="signature" class="kv" style="margin-top: 12px">
      <dt>Witness</dt>
      <dd><HexValue :value="signature" full /></dd>
    </dl>
  </section>

  <section class="panel">
    <h2 class="section-title">An adaptor signature</h2>
    <p class="small prose">
      The Bitcoin leg of a swap is a signature that is not finished. Whoever signs it locks it to a point <code>T</code>; it becomes a valid BIP-340 signature only when the scalar behind <code>T</code> is added, and anyone holding the unfinished one can read that scalar off the finished one. The same bytes come out of the Go scenario and the Dart SDK.
    </p>
    <div class="fields-2">
      <label class="field">
        <span>Signing key</span>
        <select v-model="ad.signer">
          <option value="" disabled>{{ schnorrSecrets.length ? 'choose one' : 'make a BIP-340 key first' }}</option>
          <option v-for="s in schnorrSecrets" :key="s.id" :value="s.id">{{ s.name }}</option>
        </select>
      </label>
      <label class="field">
        <span>Locked to the point of</span>
        <select v-model="ad.adaptor">
          <option value="" disabled>{{ pointSecrets.length ? 'choose one' : 'make a point secret first' }}</option>
          <option v-for="s in pointSecrets" :key="s.id" :value="s.id">{{ s.name }}</option>
        </select>
      </label>
    </div>
    <label class="field">
      <span>Message, 32 bytes</span>
      <input type="text" v-model="ad.message" spellcheck="false" placeholder="in a swap, the sighash of the claim transaction" />
      <span class="hint"><button type="button" class="btn btn-quiet" @click="randomMessage">Use random bytes</button></span>
    </label>
    <div class="btn-row" style="margin-top: 14px">
      <button type="button" class="btn btn-primary" @click="adaptorMake">Pre-sign</button>
      <button type="button" class="btn" :disabled="!ad.presig" @click="adaptorFinish">Complete with the scalar</button>
    </div>
    <p v-if="ad.error" class="note bad" style="margin-top: 10px">{{ ad.error }}</p>
    <dl v-if="ad.presig && adaptorState" class="kv" style="margin-top: 12px">
      <dt>Pre-signature</dt>
      <dd><HexValue :value="ad.presig" full /></dd>
      <dt>Checks</dt>
      <dd>
        <span class="tag" :class="adaptorState.preVerifies ? 'paid' : 'refused'">{{ adaptorState.preVerifies ? 'verifies as a pre-signature on T' : 'does not verify' }}</span>
        {{ ' ' }}
        <span class="tag" :class="adaptorState.preIsSignature ? 'refused' : 'paid'">{{ adaptorState.preIsSignature ? 'is already a signature' : 'is not a signature yet' }}</span>
      </dd>
      <template v-if="ad.signature">
        <dt>Signature</dt>
        <dd><HexValue :value="ad.signature" full /></dd>
        <dt>Checks</dt>
        <dd>
          <span class="tag" :class="adaptorState.sigVerifies ? 'paid' : 'refused'">{{ adaptorState.sigVerifies ? 'valid BIP-340 signature' : 'not valid' }}</span>
        </dd>
        <dt>Scalar read back</dt>
        <dd>
          <HexValue :value="adaptorState.extracted!" full />
          <span class="tag" :class="adaptorState.extracted === adaptorState.target.secret ? 'paid' : 'refused'">
            {{ adaptorState.extracted === adaptorState.target.secret ? 'the scalar behind T' : 'not the scalar' }}
          </span>
        </dd>
      </template>
    </dl>
  </section>
</template>

<style scoped>
.actions {
  white-space: nowrap;
  text-align: right;
}
.actions .btn + .btn {
  margin-left: 6px;
}
</style>
