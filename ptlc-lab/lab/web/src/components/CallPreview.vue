<script setup lang="ts">
// One contract call, written out the ways it can be made: the arguments the
// contract will read, the bytes that carry them, the block a wallet is handed,
// and the CLI command that sends the same thing.
//
// The arguments shown are decoded back out of the bytes, not echoed from the
// form, so what is on screen is what the contract receives.
import { computed } from 'vue'
import { METHODS, PTLC_CONTRACT, decodeCall, methodIdHex, type MethodName } from '../ptlc/contract.ts'
import { blockTemplate } from '../znn/block.ts'
import { ZNN_ZTS, toHex } from '../znn/primitives.ts'
import { chainId, formatAmount, nameOf } from '../store.ts'

const props = defineProps<{
  method: MethodName
  data: Uint8Array | null
  amount?: bigint
  tokenStandard?: string
  /** The equivalent znn-cli command, when there is one. */
  cli?: string | null
  /** Why the CLI cannot make exactly this call, when it cannot. */
  cliNote?: string | null
}>()

const def = computed(() => METHODS.find((m) => m.name === props.method)!)
const decoded = computed(() => (props.data ? decodeCall(props.data) : null))

const rows = computed(() => {
  const d = decoded.value as Record<string, unknown> | null
  if (!d) return []
  return def.value.inputs.map((input) => {
    // The ABI calls the witness `signature`; the decoder names it for what it is.
    const raw = d[input.name === 'signature' ? 'witness' : input.name]
    let value: string
    let gloss = ''
    if (raw instanceof Uint8Array) value = toHex(raw)
    else value = String(raw)
    if (input.type === 'address') gloss = nameOf(value) ?? ''
    if (input.name === 'expirationTime') gloss = new Date(Number(raw) * 1000).toISOString().replace('.000Z', ' UTC').replace('T', ' ')
    return { name: input.name, type: input.type, value, gloss }
  })
})

const hex = computed(() => (props.data ? toHex(props.data) : ''))
const template = computed(() =>
  props.data
    ? blockTemplate(
        { toAddress: PTLC_CONTRACT, data: props.data, amount: props.amount ?? 0n, tokenStandard: props.tokenStandard ?? ZNN_ZTS },
        chainId(),
      )
    : null,
)
</script>

<template>
  <div class="preview" v-if="data">
    <dl class="kv">
      <dt>Send to</dt>
      <dd><span class="data">{{ PTLC_CONTRACT }}</span></dd>
      <dt>Carrying</dt>
      <dd>{{ amount && amount > 0n ? formatAmount(amount, tokenStandard ?? ZNN_ZTS) : 'nothing' }}</dd>
      <dt>Method</dt>
      <dd>
        <strong>{{ method }}</strong> <span class="data faint">{{ methodIdHex(method) }}</span>
      </dd>
      <template v-for="r in rows" :key="r.name">
        <dt>
          {{ r.name }} <span class="type">{{ r.type }}</span>
        </dt>
        <dd>
          <span class="data value">{{ r.value }}</span>
          <span v-if="r.gloss" class="small"> {{ r.gloss }}</span>
        </dd>
      </template>
    </dl>

    <details>
      <summary>Call data, {{ data.length }} bytes</summary>
      <pre class="well"><b>{{ hex.slice(0, 8) }}</b>{{ hex.slice(8) }}</pre>
    </details>
    <details>
      <summary>The block, as Syrius is handed it</summary>
      <p class="small lead">
        The page fills in where it goes, what it carries and the data. The zeroed fields are the wallet's: account, height, previous block, momentum, plasma, signature. Syrius shows the contract as unrecognized, because ptlc is not one it knows.
      </p>
      <pre class="well">{{ JSON.stringify(template, null, 2) }}</pre>
    </details>
    <details v-if="cli || cliNote">
      <summary>The same call with znn-cli</summary>
      <pre v-if="cli" class="well">znn-cli {{ cli }}</pre>
      <p v-if="cliNote" class="small lead">{{ cliNote }}</p>
    </details>
  </div>
</template>

<style scoped>
.preview {
  margin-top: 12px;
  border-top: 1px dashed var(--line-strong);
  padding-top: 12px;
}
.type {
  font-weight: 400;
  color: var(--dim);
  font-size: 0.72rem;
}
.value {
  word-break: break-all;
}
details {
  margin-top: 8px;
}
.lead {
  margin: 4px 0 8px;
  max-width: 68ch;
}
pre b {
  background: var(--locked-bg);
  font-weight: 600;
}
</style>
