<script setup lang="ts">
// The two clients that are programs: the Dart CLI and the Go scenarios. labd
// runs them in the lab container and this bench shows what they print. Their
// blocks land on the same contract chain as everything else, so the tape and
// the Entries bench follow them without being told.
import { computed, reactive, ref } from 'vue'
import RunOutput from '../components/RunOutput.vue'
import { startRun, type RunSummary, type StartRun } from '../api.ts'
import { labKeys, state } from '../store.ts'
import { errorText } from '../lib.ts'

defineProps<{ param: string }>()

// ---- znn-cli -------------------------------------------------------------------

const cli = reactive({ index: 1, line: 'ptlc.keygen', runId: null as string | null, error: '', busy: false })

const indexes = [
  { index: 1, name: 'Alice', address: labKeys[0].account.address },
  { index: 3, name: 'Bob', address: labKeys[1].account.address },
]

const lockedEntry = computed(() => state.entries.find((e) => e.state === 'locked'))
const anyEntry = computed(() => state.entries[0])
const presets = computed(() => [
  { label: 'List the ptlc commands', line: 'ptlc' },
  { label: 'Make a secret and its point', line: 'ptlc.keygen' },
  { label: 'Show an entry', line: `ptlc.get ${lockedEntry.value?.id ?? anyEntry.value?.id ?? '<id>'}` },
  { label: 'Inspect a call block', line: `ptlc.inspect ${state.events[0]?.sendHash ?? '<block hash>'}` },
  { label: 'Balance', line: `balance ${indexes.find((i) => i.index === cli.index)?.address}` },
  { label: 'Receive everything waiting', line: 'receiveAll' },
  { label: 'Frontier momentum', line: 'frontierMomentum' },
])

/** Splits a command line on spaces, keeping a quoted run as one argument. */
function split(line: string): string[] {
  const args: string[] = []
  const pattern = /"([^"]*)"|'([^']*)'|(\S+)/g
  let m: RegExpExecArray | null
  while ((m = pattern.exec(line)) !== null) args.push(m[1] ?? m[2] ?? m[3])
  return args
}

async function runCli() {
  const args = split(cli.line.replace(/^\s*znn-cli\s+/, ''))
  if (args.length === 0) return
  cli.error = ''
  cli.busy = true
  try {
    const run = await startRun({ kind: 'cli', index: cli.index, args })
    cli.runId = run.id
  } catch (err) {
    cli.error = errorText(err)
    cli.busy = false
  }
}

// ---- scenarios -----------------------------------------------------------------

const scenario = reactive({
  runId: null as string | null,
  title: '',
  error: '',
  znn: 10,
  sats: 100000,
  expiry: 240,
  locktime: 30,
  znnAmount: 10,
  qsrAmount: 100,
})

async function runScenario(request: StartRun) {
  scenario.error = ''
  try {
    const run = await startRun(request)
    scenario.runId = run.id
    scenario.title = run.title
  } catch (err) {
    scenario.error = errorText(err)
  }
}

const pastRuns = computed(() => state.runs.filter((r) => r.kind !== 'cli').slice(0, 8))
function show(run: RunSummary) {
  scenario.runId = run.id
  scenario.title = run.title
}
const when = (ms: number) => new Date(ms).toISOString().slice(11, 19) + ' UTC'
const cfg = computed(() => state.config)
const ran = ref(0)
</script>

<template>
  <h1 class="bench-title">CLI and scenarios</h1>
  <p class="bench-lede">
    Two clients that are programs, run in the lab's container with the devnet keystore. What they do to the contract shows up on the tape like anything else.
  </p>
  <p v-if="state.busyRun" class="note warn" style="margin-top: 12px">
    {{ state.busyRun.title }} is running. It signs with both dev accounts, so the CLI and Alice's and Bob's lab keys wait until it ends.
  </p>

  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">znn-cli</h2>
      <span class="small faint" v-if="cfg">keystore {{ cfg.keystore }}, node {{ cfg.cliNode }}</span>
    </div>
    <p class="small prose">
      The Dart reference client, built from <code>znn_cli_dart</code> with this repository's patch: seventeen <code>ptlc.*</code> commands beside the ones it already had. The lab sets the node, the chain and the keystore; you give the command.
    </p>
    <p v-if="cfg && !cfg.cli" class="note bad" style="margin-top: 10px">
      labd has no znn-cli to run. In the lab image it is at /opt/znn-cli; outside it, point LAB_CLI at a build of the znn_cli_dart-ptlc worktree.
    </p>
    <div class="fields-2">
      <label class="field">
        <span>Run as</span>
        <select v-model.number="cli.index">
          <option v-for="i in indexes" :key="i.index" :value="i.index">{{ i.name }}, keystore index {{ i.index }}</option>
        </select>
      </label>
      <label class="field">
        <span>Fill in</span>
        <select @change="((cli.line = ($event.target as HTMLSelectElement).value), (($event.target as HTMLSelectElement).value = ''))">
          <option value="">a command to start from</option>
          <option v-for="p in presets" :key="p.label" :value="p.line">{{ p.label }}</option>
        </select>
      </label>
    </div>
    <label class="field">
      <span>Command</span>
      <input type="text" v-model="cli.line" spellcheck="false" @keydown.enter="runCli" />
      <span class="hint">
        For example <code>ptlc.create ZNN 1 1 2 &lt;point&gt; &lt;destination&gt;</code>: one ZNN, one hour, a point lock. Every bench's call preview gives the command for the call it is about to make.
      </span>
    </label>
    <div class="btn-row" style="margin-top: 12px">
      <button type="button" class="btn btn-primary" :disabled="cli.busy || !!state.busyRun || (cfg ? !cfg.cli : false)" @click="runCli">
        {{ cli.busy ? 'Running' : 'Run it' }}
      </button>
    </div>
    <p v-if="cli.error" class="note bad" style="margin-top: 10px" role="alert">{{ cli.error }}</p>
    <RunOutput :run-id="cli.runId" @done="((cli.busy = false), ran++)" />
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">NoM to NoM, through the CLI</h2>
      <span class="small faint">scenarios/nom-to-nom/run.sh</span>
    </div>
    <p class="small prose">
      The same swap as the step-by-step bench, scripted: Alice and Bob are two keystore indexes, every call is a <code>znn-cli</code> command, and Bob finds the scalar with <code>ptlc.monitor</code>. About three minutes.
    </p>
    <div class="fields-2">
      <label class="field">
        <span>Bob's ZNN</span>
        <input type="number" v-model.number="scenario.znnAmount" min="1" max="1000" />
      </label>
      <label class="field">
        <span>Alice's QSR</span>
        <input type="number" v-model.number="scenario.qsrAmount" min="1" max="1000" />
      </label>
    </div>
    <div class="btn-row" style="margin-top: 12px">
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!!state.busyRun || (cfg ? !cfg.nomToNom || !cfg.cli : false)"
        @click="runScenario({ kind: 'nom-to-nom', znnAmount: scenario.znnAmount, qsrAmount: scenario.qsrAmount })"
      >
        Run the swap
      </button>
    </div>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2 class="section-title">NoM to BTC</h2>
      <span class="small faint">scenarios/nom-to-btc</span>
    </div>
    <p class="small prose">
      Alice has regtest bitcoin, Bob has devnet ZNN. She funds a Taproot output and pre-signs Bob's claim as an adaptor signature on <code>T</code>; he locks ZNN to <code>T</code>; her unlock publishes <code>t</code>; he completes the Bitcoin signature with it. A Go program plays both, checking every value against a chain.
    </p>
    <p class="small" style="margin-top: 6px">
      bitcoind:
      <span v-if="state.btcHeight !== null"><span class="tag paid">regtest</span> block {{ state.btcHeight }}</span>
      <span v-else><span class="tag refused">not answering</span> {{ state.btcError }}</span>
    </p>
    <div class="fields-2">
      <label class="field">
        <span>Alice's sats</span>
        <input type="number" v-model.number="scenario.sats" min="10000" step="10000" />
      </label>
      <label class="field">
        <span>Bob's ZNN</span>
        <input type="number" v-model.number="scenario.znn" min="1" max="1000" />
      </label>
      <label class="field">
        <span>NoM expiry, seconds</span>
        <input type="number" v-model.number="scenario.expiry" min="120" max="3600" />
      </label>
      <label class="field">
        <span>Bitcoin refund height, blocks from the tip</span>
        <input type="number" v-model.number="scenario.locktime" min="5" max="500" />
      </label>
    </div>
    <div class="btn-row" style="margin-top: 12px">
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!!state.busyRun || state.btcHeight === null || (cfg ? !cfg.nomToBtc : false)"
        @click="runScenario({ kind: 'nom-to-btc', scenario: 'happy', sats: scenario.sats, znn: scenario.znn, expiry: scenario.expiry, locktime: scenario.locktime })"
      >
        Run it: Alice reveals
      </button>
      <button
        type="button"
        class="btn"
        :disabled="!!state.busyRun || state.btcHeight === null || (cfg ? !cfg.nomToBtc : false)"
        @click="runScenario({ kind: 'nom-to-btc', scenario: 'refund', sats: scenario.sats, znn: scenario.znn, expiry: scenario.expiry, locktime: scenario.locktime })"
      >
        Run it: Alice never reveals
      </button>
    </div>
    <p class="small" style="margin-top: 8px; max-width: 68ch">
      The first takes about two minutes. The second waits out the NoM expiry before Bob can reclaim, so it takes that long and a little more.
    </p>
  </section>

  <section v-if="scenario.runId || scenario.error || pastRuns.length" class="panel">
    <div class="panel-head">
      <h2 class="section-title">{{ scenario.title || 'Scenario runs' }}</h2>
    </div>
    <p v-if="scenario.error" class="note bad" role="alert">{{ scenario.error }}</p>
    <RunOutput :run-id="scenario.runId" tall />
    <div v-if="pastRuns.length" class="table-scroll" style="margin-top: 12px">
      <table class="grid">
        <thead>
          <tr>
            <th>Started</th>
            <th>Run</th>
            <th>Outcome</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in pastRuns" :key="r.id">
            <td class="small">{{ when(r.started) }}</td>
            <td>{{ r.title }}</td>
            <td>
              <span v-if="r.exit === undefined" class="tag locked">running</span>
              <span v-else-if="r.exit === 0" class="tag paid">finished in {{ Math.round(((r.ended ?? r.started) - r.started) / 1000) }} s</span>
              <span v-else class="tag refused">exit {{ r.exit }}</span>
            </td>
            <td><button type="button" class="btn btn-quiet" :disabled="r.id === scenario.runId" @click="show(r)">Show output</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="small faint" style="margin-top: 8px">labd keeps the last 40 runs and forgets them when the container restarts.</p>
  </section>
</template>
