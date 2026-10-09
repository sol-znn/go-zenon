// The scenarios page's state: the catalogue, the newest run of each scenario,
// and which one, if any, is running now.
//
// Runs live in labd, not here. Whoever opens the page sees the runs the lab
// has, a run started by somebody else included, and watches it as it goes.
// One scenario runs at a time for everybody: they all sign as the same two
// devnet accounts, and an account chain takes one block at a time.

import { reactive } from 'vue'
import { apply, close, newRun, parse, type Catalogue, type RunLine, type RunView, type Scenario } from './flow.ts'

interface RunSummary {
  id: string
  kind: string
  flow?: string
  title: string
  started: number
  ended?: number
  exit?: number
}

export const state = reactive({
  catalogue: null as Catalogue | null,
  /** Why there is nothing to show, when there is nothing to show. */
  error: '',
  /** The newest run of each scenario the lab still remembers. */
  runs: {} as Record<string, RunView>,
  /** What holds the dev accounts now: a scenario of this page, or one of the console's. */
  busy: null as { id: string; title: string; flow?: string } | null,
  /** The scenario whose flow is unfolded. */
  open: '',
  startError: {} as Record<string, string>,
  starting: '',
  momentum: null as { height: number; timestamp: number } | null,
  nodeDown: false,
})

const scenario = (id: string): Scenario | undefined => state.catalogue?.scenarios.find((s) => s.id === id)

async function json<T>(response: Response): Promise<T> {
  const body = (await response.json().catch(() => null)) as (T & { error?: string }) | null
  if (!response.ok || body === null) throw new Error(body?.error ?? `the lab answered HTTP ${response.status}`)
  return body
}

// ---- following a run -----------------------------------------------------------

// Lines of a live run are shown a beat apart. A step a party takes alone is
// printed as started and finished in the same millisecond, and a page that
// applied both at once would show a step that was never seen to happen. A
// line that is already old (the stream replays a run from its first line) is
// applied at once, so joining late does not mean watching it all again.
const BEAT = 420
const followers = new Map<string, { source: EventSource; queue: (RunLine | { exit: number })[]; timer: number }>()

function pump(run: RunView): void {
  const f = followers.get(run.id)
  if (!f || f.timer) return
  const next = (): void => {
    f.timer = 0
    const item = f.queue.shift()
    if (!item) return
    if ('exit' in item) {
      close(run, item.exit, Date.now())
      followers.delete(run.id)
      void sync()
      return
    }
    apply(run, item)
    const paced = parse(item.text)?.step !== undefined && Date.now() - item.at < 4000
    if (paced) f.timer = window.setTimeout(next, BEAT)
    else next()
  }
  next()
}

function follow(run: RunView): void {
  if (followers.has(run.id)) return
  const source = new EventSource(`/api/runs/${run.id}/stream`)
  const f = { source, queue: [] as (RunLine | { exit: number })[], timer: 0 }
  followers.set(run.id, f)
  source.onmessage = (event) => {
    f.queue.push(JSON.parse(event.data) as RunLine)
    pump(run)
  }
  source.addEventListener('done', (event) => {
    source.close()
    f.queue.push({ exit: (JSON.parse((event as MessageEvent).data) as { exit: number }).exit })
    pump(run)
  })
  // EventSource reconnects by itself and labd resumes from the last line it
  // sent, so an error here is a pause, not an end. If labd restarted, the run
  // is gone, and the next sync says so.
}

async function readBack(summary: RunSummary, sc: Scenario): Promise<void> {
  const full = await json<RunSummary & { output: RunLine[] }>(await fetch(`/api/runs/${summary.id}`))
  const run = newRun(summary.id, sc, summary.started)
  for (const line of full.output) apply(run, line)
  close(run, summary.exit ?? -1, summary.ended ?? summary.started)
  run.ended = run.ended ?? summary.ended
  state.runs[sc.id] = run
}

// ---- keeping up with the lab ---------------------------------------------------

let syncing = false

export async function sync(): Promise<void> {
  if (syncing || !state.catalogue) return
  syncing = true
  try {
    const list = await json<{ runs: RunSummary[]; busy?: { id: string; title: string } }>(await fetch('/api/runs'))
    const newest = new Map<string, RunSummary>()
    for (const summary of list.runs) {
      // labd lists the newest first.
      if (summary.kind === 'flow' && summary.flow && !newest.has(summary.flow)) newest.set(summary.flow, summary)
    }
    for (const [id, summary] of newest) {
      const sc = scenario(id)
      if (!sc) continue
      const known = state.runs[id]
      if (known?.id === summary.id && (known.status !== 'running' || followers.has(summary.id))) continue
      if (summary.exit === undefined) {
        const run = known?.id === summary.id ? known : newRun(summary.id, sc, summary.started)
        state.runs[id] = run
        follow(state.runs[id])
      } else {
        // Finished, and either new to this page or one it was following
        // whose stream it lost.
        await readBack(summary, sc)
      }
    }
    // A run this page still shows as running, which the lab no longer has:
    // labd restarted, and its runs went with it.
    for (const [id, run] of Object.entries(state.runs)) {
      if (run.status === 'running' && !list.runs.some((r) => r.id === run.id)) {
        followers.get(run.id)?.source.close()
        followers.delete(run.id)
        close(run, -1, Date.now())
        run.summary = 'The lab restarted while this was running'
        state.runs[id] = run
      }
    }
    state.busy = list.busy ? { ...list.busy, flow: list.runs.find((r) => r.id === list.busy!.id)?.flow } : null
    if (state.busy?.flow && !state.open) state.open = state.busy.flow
    state.error = ''
  } catch (err) {
    state.error = err instanceof Error ? err.message : String(err)
  } finally {
    syncing = false
  }
}

export async function run(id: string): Promise<void> {
  const sc = scenario(id)
  if (!sc || state.starting) return
  state.starting = id
  delete state.startError[id]
  state.open = id
  try {
    const summary = await json<RunSummary>(
      await fetch('/api/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: 'flow', flow: id }),
      }),
    )
    state.runs[id] = newRun(summary.id, sc, summary.started)
    state.busy = { id: summary.id, title: summary.title, flow: id }
    follow(state.runs[id])
  } catch (err) {
    state.startError[id] = err instanceof Error ? err.message : String(err)
    void sync()
  } finally {
    state.starting = ''
  }
}

export async function stop(runId: string): Promise<void> {
  await fetch(`/api/runs/${runId}`, { method: 'DELETE' }).catch(() => undefined)
}

async function readChain(): Promise<void> {
  try {
    const answer = await json<{ result?: { height: number; timestamp: number } }>(
      await fetch('/rpc', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ledger.getFrontierMomentum', params: [] }),
      }),
    )
    if (!answer.result) throw new Error('no momentum')
    if (answer.result.height !== state.momentum?.height) state.momentum = { height: answer.result.height, timestamp: answer.result.timestamp }
    state.nodeDown = false
  } catch {
    state.nodeDown = true
  }
}

export async function start(): Promise<void> {
  void readChain()
  window.setInterval(readChain, 4000)
  try {
    state.catalogue = await json<Catalogue>(await fetch('/api/flows'))
  } catch (err) {
    state.error = err instanceof Error ? err.message : String(err)
    return
  }
  await sync()
  window.setInterval(sync, 3000)
}

export const tally = (): { pass: number; fail: number; running: number; none: number } => {
  const out = { pass: 0, fail: 0, running: 0, none: 0 }
  for (const sc of state.catalogue?.scenarios ?? []) {
    const r = state.runs[sc.id]
    if (!r) out.none++
    else out[r.status]++
  }
  return out
}
