<script setup lang="ts">
// The node, one JSON-RPC call at a time, and the log of the calls the benches
// made on your behalf. The contract has one read method; everything else the
// lab knows it worked out from the ledger calls listed here.
import { computed, reactive } from 'vue'
import { PTLC_CONTRACT } from '../ptlc/contract.ts'
import { client, labKeys, state } from '../store.ts'
import { errorText } from '../lib.ts'

defineProps<{ param: string }>()

const form = reactive({ method: 'embedded.ptlc.getById', params: '[]', answer: '', ok: true, busy: false, ms: 0 })

const anEntry = computed(() => state.entries.find((e) => e.state === 'locked')?.id ?? state.entries[0]?.id ?? '<entry id>')
const presets = computed(() => [
  { label: 'An entry, while it is locked', method: 'embedded.ptlc.getById', params: [anEntry.value] },
  { label: "The contract's newest block", method: 'ledger.getFrontierAccountBlock', params: [PTLC_CONTRACT] },
  { label: "The contract's chain, from the start", method: 'ledger.getAccountBlocksByHeight', params: [PTLC_CONTRACT, 1, 10] },
  { label: 'A call block, by its hash', method: 'ledger.getAccountBlockByHash', params: [state.events[0]?.sendHash ?? '<block hash>'] },
  { label: 'Frontier momentum', method: 'ledger.getFrontierMomentum', params: [] },
  { label: 'Sporks, ptlc among them', method: 'embedded.spork.getAll', params: [0, 10] },
  { label: "Alice's balances", method: 'ledger.getAccountInfoByAddress', params: [labKeys[0].account.address] },
  { label: "What is waiting for Alice", method: 'ledger.getUnreceivedBlocksByAddress', params: [labKeys[0].account.address, 0, 10] },
  { label: "Alice's plasma", method: 'embedded.plasma.get', params: [labKeys[0].account.address] },
  {
    label: 'Plasma an unlock would need',
    method: 'embedded.plasma.getRequiredPoWForAccountBlock',
    params: [{ address: labKeys[0].account.address, blockType: 2, toAddress: PTLC_CONTRACT, data: '' }],
  },
])

function use(index: number) {
  const p = presets.value[index]
  if (!p) return
  form.method = p.method
  form.params = JSON.stringify(p.params)
}

async function call() {
  let params: unknown
  try {
    params = JSON.parse(form.params || '[]')
    if (!Array.isArray(params)) throw new Error('params is a JSON array')
  } catch (err) {
    form.ok = false
    form.answer = `The params are not a JSON array: ${errorText(err)}`
    return
  }
  form.busy = true
  const started = performance.now()
  try {
    const result = await client.call<unknown>(form.method.trim(), params as unknown[])
    form.ok = true
    form.answer = JSON.stringify(result, null, 2)
  } catch (err) {
    form.ok = false
    form.answer = errorText(err)
  } finally {
    form.ms = Math.round(performance.now() - started)
    form.busy = false
  }
}

const brief = (value: unknown) => {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return text === undefined ? '' : text.length > 96 ? `${text.slice(0, 96)}…` : text
}
const time = (at: number) => new Date(at).toISOString().slice(11, 19)
</script>

<template>
  <h1 class="bench-title">Raw RPC</h1>
  <p class="bench-lede">
    The node's JSON-RPC, forwarded through the lab at <code>/rpc</code>. From outside the lab the same calls go to <code>{{ state.config?.nodeHttp ?? 'http://127.0.0.1:35997' }}</code>.
  </p>

  <section class="panel">
    <h2 class="section-title">Make a call</h2>
    <label class="field" style="margin-top: 8px">
      <span>Start from</span>
      <select @change="(use(Number(($event.target as HTMLSelectElement).value)), (($event.target as HTMLSelectElement).value = ''))">
        <option value="">a call the lab itself makes</option>
        <option v-for="(p, i) in presets" :key="p.label" :value="i">{{ p.label }}</option>
      </select>
    </label>
    <div class="fields-2">
      <label class="field">
        <span>Method</span>
        <input type="text" v-model="form.method" spellcheck="false" />
      </label>
      <label class="field">
        <span>Params, a JSON array</span>
        <input type="text" v-model="form.params" spellcheck="false" @keydown.enter="call" />
      </label>
    </div>
    <div class="btn-row" style="margin-top: 12px">
      <button type="button" class="btn btn-primary" :disabled="form.busy" @click="call">{{ form.busy ? 'Calling' : 'Call the node' }}</button>
      <span v-if="form.answer" class="small faint">{{ form.ms }} ms</span>
    </div>
    <pre v-if="form.answer" class="well answer" :class="{ bad: !form.ok }">{{ form.answer }}</pre>
    <p class="note" style="margin-top: 12px">
      <code>embedded.ptlc.getById</code> is the contract's whole read interface. It answers for an entry that is locked, and says <code>data non existent</code> for one that was paid, returned, or never made. There is no call that lists entries; the lab folds them out of <code>ledger.getAccountBlocksByHeight</code> on the contract's address.
    </p>
  </section>

  <section class="panel">
    <h2 class="section-title">Calls the benches made</h2>
    <p class="small">The background polling that keeps the page current is left out; it is the same four calls every two seconds.</p>
    <p v-if="state.rpcLog.length === 0" class="small" style="margin-top: 8px">None yet. Send a block from any bench and its calls are listed here in order.</p>
    <div v-else class="table-scroll" style="margin-top: 8px">
      <table class="grid log">
        <thead>
          <tr>
            <th>At</th>
            <th>Method</th>
            <th>Params</th>
            <th>Answer</th>
            <th>ms</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in state.rpcLog.slice(0, 60)" :key="e.n">
            <td class="data">{{ time(e.at) }}</td>
            <td class="data">{{ e.method }}</td>
            <td class="data faint">{{ brief(e.params) }}</td>
            <td class="data" :class="{ bad: !e.ok }">{{ brief(e.answer) }}</td>
            <td class="data faint">{{ e.ms }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.answer {
  margin-top: 12px;
  max-height: 420px;
  overflow: auto;
}
.answer.bad,
td.bad {
  color: var(--refused);
}
.log td {
  font-size: 0.72rem;
  word-break: break-all;
}
</style>
