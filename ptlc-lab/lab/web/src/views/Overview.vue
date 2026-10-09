<script setup lang="ts">
import { computed } from 'vue'
import MermaidDiagram from '../components/MermaidDiagram.vue'
import HexValue from '../components/HexValue.vue'
import { POINT_TYPES, PTLC_CONTRACT, PTLC_SPORK_ID } from '../ptlc/contract.ts'
import { state } from '../store.ts'

defineProps<{ param: string }>()

const lifecycle = `stateDiagram-v2
  direction LR
  [*] --> Locked: Create
  Locked --> Paid: Unlock or ProxyUnlock, before expiry
  Locked --> Returned: Reclaim, at or after expiry
  Paid --> [*]
  Returned --> [*]`

const counts = computed(() => ({
  locked: state.entries.filter((e) => e.state === 'locked').length,
  paid: state.entries.filter((e) => e.state === 'paid').length,
  returned: state.entries.filter((e) => e.state === 'returned').length,
  refused: state.events.filter((e) => !e.ok).length,
}))

const short = (sha: string | undefined) => (sha ? sha.slice(0, 7) : 'not reported')
</script>

<template>
  <h1 class="bench-title">Every way into the ptlc contract, on one bench</h1>
  <p class="bench-lede">
    A ptlc entry holds funds until someone shows the secret behind a point on a curve, or until a time passes and its creator takes them back. This lab runs the contract on a devnet and lets you make each of its four calls through each client that exists for it, then watch what the chain did.
  </p>

  <section class="panel">
    <h2 class="section-title">What is running</h2>
    <dl class="kv">
      <dt>Node</dt>
      <dd v-if="state.momentum">
        <span class="tag paid">answering</span> chain {{ state.momentum.chainIdentifier }}, momentum {{ state.momentum.height.toLocaleString('en') }}
      </dd>
      <dd v-else><span class="tag refused">not answering</span> {{ state.nodeError || 'reading' }}</dd>

      <dt>Its RPC</dt>
      <dd>
        <code>{{ state.config?.nodeHttp ?? 'http://127.0.0.1:35997' }}</code> over HTTP,
        <code>{{ state.config?.walletNode ?? 'ws://127.0.0.1:35998' }}</code> over WebSocket
        <span class="small">for any client; a wallet dials the second</span>
      </dd>

      <template v-if="state.peers !== null">
        <dt>Its peers</dt>
        <dd>
          <span class="tag" :class="state.peers >= 4 ? 'paid' : 'locked'">{{ state.peers }} of 4</span>
          <span v-if="state.peers < 4" class="small">
            The node a block is published to passes it only to its own peers. A pillar it does not reach learns of the block from somebody else's momentum, so calls wait for the right pillar's turn and answers come several momentums late.
          </span>
        </dd>
      </template>

      <dt>Contract</dt>
      <dd>
        <span class="tag" :class="state.spork ? 'paid' : state.spork === false ? 'refused' : ''">
          {{ state.spork ? 'spork active' : state.spork === false ? 'spork not active' : 'checking' }}
        </span>
        <HexValue :value="PTLC_CONTRACT" full />
      </dd>

      <dt>Spork</dt>
      <dd><HexValue :value="PTLC_SPORK_ID" :head="16" :tail="8" /> <span class="small">the id mainnet has held since 2023, never activated there</span></dd>

      <dt>Entries so far</dt>
      <dd>
        <a href="#/entries">{{ counts.locked }} locked, {{ counts.paid }} paid, {{ counts.returned }} returned</a>; {{ counts.refused }} calls refused
      </dd>

      <dt>bitcoind</dt>
      <dd v-if="state.btcHeight !== null"><span class="tag paid">answering</span> regtest, block {{ state.btcHeight }}</dd>
      <dd v-else><span class="tag refused">not answering</span> <span class="small">only the bitcoin scenarios need it</span></dd>

      <dt>Built from</dt>
      <dd class="small">
        go-zenon <span class="data">{{ short(state.config?.goZenonBase) }}</span> + patches,
        znn_sdk_dart <span class="data">{{ short(state.config?.dartSdkBase) }}</span> + patches,
        znn_cli_dart <span class="data">{{ short(state.config?.dartCliBase) }}</span> + patch
      </dd>
    </dl>
    <p v-if="state.config?.public" class="note warn" style="margin-top: 12px">
      This lab is on a public address, and you are not alone in it. Alice, Bob and Carol are the devnet's published keys, so everyone with this page open signs as the same three accounts. An account chain takes one block at a time: when two people send from Alice in the same moment the node refuses one of the blocks, and that call has to be made again. Entries on the tape that you did not make are somebody else's. A scenario holds Alice and Bob for as long as it runs, for everybody.
    </p>
  </section>

  <section class="panel">
    <h2 class="section-title">The ways in</h2>
    <div class="table-scroll">
      <table class="grid">
        <thead>
          <tr>
            <th>Client</th>
            <th>Who signs</th>
            <th>Here</th>
            <th>What it cannot do</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td><strong>JSON-RPC</strong><br /><span class="small">any HTTP client</span></td>
            <td>nobody, it only reads</td>
            <td><a href="#/rpc">Raw RPC</a>, and the tape on the right</td>
            <td><code>embedded.ptlc.getById</code> is the only read call. Who opened an entry, and with what, has to be read off the contract's chain.</td>
          </tr>
          <tr>
            <td><strong>A key the page holds</strong><br /><span class="small">Alice, Bob, Carol</span></td>
            <td>this page, in JavaScript</td>
            <td>pick one in the header, then <a href="#/lock">Lock funds</a> or <a href="#/entries">Entries</a></td>
            <td>Nothing here; the keys are the devnet's public ones, and worth nothing elsewhere.</td>
          </tr>
          <tr>
            <td><strong>Syrius extension</strong><br /><span class="small">v0.3.4 and later</span></td>
            <td>your wallet, after you approve each block</td>
            <td><a href="#/wallet">Syrius and accounts</a>, then pick Syrius in the header</td>
            <td>It shows the ptlc contract as unrecognized, with a warning. It will not sign a 32-byte message, so its own key cannot be an Ed25519 lock.</td>
          </tr>
          <tr>
            <td><strong>znn-cli</strong><br /><span class="small">the Dart reference client</span></td>
            <td>the CLI, with the devnet keystore</td>
            <td><a href="#/programs">CLI and scenarios</a></td>
            <td>Expiry is whole hours, 1 to 168, so a refund cannot be watched in one sitting.</td>
          </tr>
          <tr>
            <td><strong>Go programs</strong><br /><span class="small">the bitcoin swap</span></td>
            <td>the program, both parties in one process</td>
            <td><a href="#/programs">CLI and scenarios</a></td>
            <td>It is a scripted run, not a client: no step waits for a person.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <section class="panel">
    <h2 class="section-title">Three kinds of lock</h2>
    <div class="table-scroll">
      <table class="grid">
        <thead>
          <tr>
            <th>pointType</th>
            <th>The lock is</th>
            <th>It opens with</th>
            <th>The chain then shows</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in POINT_TYPES" :key="p.type">
            <td><span class="data">{{ p.type }}</span> {{ p.name }}</td>
            <td>{{ p.lock }}</td>
            <td>{{ p.witness }}</td>
            <td>{{ p.shown }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="note" style="margin-top: 12px">
      A key lock keeps its secret off the chain: a swap moves it inside an adaptor signature. A point lock publishes its scalar when it is opened, which is what lets the other party read it with no signing session, and why such an entry must name the one address it pays.
    </p>
  </section>

  <section class="panel">
    <h2 class="section-title">The life of an entry</h2>
    <MermaidDiagram :code="lifecycle" label="An entry is locked by Create, then either paid by an unlock before its expiry or returned by a reclaim at or after it." />
    <p class="small" style="margin-top: 8px; max-width: 68ch">
      Expiry is judged by the timestamp of the frontier momentum, not by anyone's clock. A refused call changes nothing and moves nothing; a refused Create sends the funds straight back.
    </p>
  </section>
</template>
