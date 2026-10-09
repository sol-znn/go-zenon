// What a scenario run looks like to the page, and how the runner's log turns
// into it.
//
// The flows program prints a log. One kind of line is for a program: "@flow "
// and a JSON object, a step changing state or, last, the result. `apply` folds
// those lines into a view, one at a time, in the order they were printed. It
// is the same fold for a run watched live and for one read back afterwards,
// so a finished run looks the same to whoever opens the page later.

export interface StepDef {
  id: string
  kind: 'local' | 'message' | 'call' | 'pay' | 'wait' | 'read' | 'bitcoin'
  from: string
  to?: string
  label: string
  /** For a call: what the contract or the network is expected to do with it. */
  expect?: 'accepted' | 'refused' | 'rejected'
}

export interface Scenario {
  id: string
  group: string
  title: string
  shows: string
  parties: string[]
  seconds: number
  steps: StepDef[]
}

export interface Group {
  id: string
  title: string
  lede: string
}

export interface Catalogue {
  groups: Group[]
  scenarios: Scenario[]
}

/** One "@flow" line. */
export interface FlowEvent {
  step?: string
  state?: 'active' | 'sent' | 'confirmed' | 'done' | 'failed'
  answer?: 'accepted' | 'refused' | 'rejected'
  detail?: string
  hash?: string
  until?: number
  now?: number
  result?: 'pass' | 'fail'
}

export type StepState = 'pending' | 'active' | 'sent' | 'confirmed' | 'done' | 'failed'

export interface StepView {
  state: StepState
  answer?: 'accepted' | 'refused' | 'rejected'
  detail: string
  hash?: string
  /** A wait: seconds it had to run when it began, and when that was (ms). */
  waitSeconds?: number
  waitFrom?: number
}

export interface RunView {
  id: string
  scenario: string
  /** running until the result line, or until the program ends without one. */
  status: 'running' | 'pass' | 'fail'
  summary: string
  started: number
  ended?: number
  steps: Record<string, StepView>
  /** Everything the program printed that was not for a program. */
  log: { at: number; text: string }[]
}

export interface RunLine {
  n: number
  at: number
  stream: 'out' | 'err' | 'lab'
  text: string
}

export function newRun(id: string, scenario: Scenario, started: number): RunView {
  const steps: Record<string, StepView> = {}
  for (const step of scenario.steps) steps[step.id] = { state: 'pending', detail: '' }
  return { id, scenario: scenario.id, status: 'running', summary: '', started, steps, log: [] }
}

const PREFIX = '@flow '

export function parse(text: string): FlowEvent | null {
  if (!text.startsWith(PREFIX)) return null
  try {
    return JSON.parse(text.slice(PREFIX.length)) as FlowEvent
  } catch {
    return null
  }
}

export function apply(run: RunView, line: RunLine): void {
  const event = parse(line.text)
  if (!event) {
    run.log.push({ at: line.at, text: line.text })
    return
  }
  if (event.result) {
    run.status = event.result
    run.summary = event.detail ?? ''
    run.ended = line.at
    return
  }
  const step = event.step ? run.steps[event.step] : undefined
  if (!step || !event.state) return
  // A step never goes backwards. The Bitcoin scenarios are read off another
  // program's log, where a line that starts a step can follow the line that
  // finished it.
  if (step.state === 'done' || step.state === 'failed') return
  step.state = event.state
  if (event.answer) step.answer = event.answer
  if (event.detail) step.detail = event.detail
  if (event.hash) step.hash = event.hash
  if (event.until && event.now) {
    step.waitSeconds = Math.max(0, event.until - event.now)
    step.waitFrom = line.at
  }
}

/** The program ended. Without a result line it did not pass, whatever it had reached. */
export function close(run: RunView, exit: number, at: number): void {
  if (run.status !== 'running') return
  run.status = 'fail'
  run.ended = at
  run.summary = exit === 0 ? 'The run ended without reporting a result' : 'The run was stopped before it finished'
  for (const step of Object.values(run.steps)) {
    if (step.state !== 'pending' && step.state !== 'done') step.state = 'failed'
  }
}

export function took(run: RunView): string {
  const seconds = Math.max(1, Math.round(((run.ended ?? Date.now()) - run.started) / 1000))
  if (seconds < 90) return `${seconds} s`
  return `${Math.floor(seconds / 60)} min ${String(seconds % 60).padStart(2, '0')} s`
}

export function about(seconds: number): string {
  // Under 90 s rounds to one minute, which reads as "a minute", not "1 minutes".
  if (seconds < 90) return 'about a minute'
  return `about ${Math.round(seconds / 60)} minutes`
}

export const PARTY: Record<string, { name: string; mark: string }> = {
  alice: { name: 'Alice', mark: 'A' },
  bob: { name: 'Bob', mark: 'B' },
  carol: { name: 'Carol', mark: 'C' },
  contract: { name: 'PTLC contract', mark: '' },
  bitcoin: { name: 'Bitcoin', mark: '₿' },
}
