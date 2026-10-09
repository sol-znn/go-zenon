// The scenarios page draws a run from the lines the flows program prints.
// These hold the fold to the things the page must not get wrong: a refusal
// that was expected is a finished step, a run that ends without a result did
// not pass, and a step never goes backwards.

import { describe, expect, it } from 'vitest'
import { apply, close, newRun, parse, type RunLine, type Scenario } from '../src/scenarios/flow.ts'

const scenario: Scenario = {
  id: 'wrong-secret',
  group: 'safety',
  title: 'A wrong secret is refused',
  shows: '',
  parties: ['alice', 'bob', 'contract'],
  seconds: 90,
  steps: [
    { id: 'lock', kind: 'call', from: 'alice', to: 'contract', label: 'Alice locks', expect: 'accepted' },
    { id: 'wrong', kind: 'call', from: 'bob', to: 'contract', label: 'Bob tries a wrong secret', expect: 'refused' },
    { id: 'wait', kind: 'wait', from: 'contract', label: 'The deadline passes' },
    { id: 'paid', kind: 'pay', from: 'contract', to: 'bob', label: 'Bob receives 1 ZNN' },
  ],
}

let n = 0
const line = (text: string, at = 1000 + n): RunLine => ({ n: n++, at, stream: 'out', text })
const flow = (event: object, at?: number): RunLine => line(`@flow ${JSON.stringify(event)}`, at)

describe('a run, folded from its log', () => {
  it('starts with every step pending', () => {
    const run = newRun('7', scenario, 1000)
    expect(run.status).toBe('running')
    expect(Object.values(run.steps).every((s) => s.state === 'pending')).toBe(true)
  })

  it('follows a call through sent, confirmed and answered', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ step: 'lock', state: 'active' }))
    apply(run, flow({ step: 'lock', state: 'sent', hash: 'ab'.repeat(32) }))
    expect(run.steps.lock).toMatchObject({ state: 'sent', hash: 'ab'.repeat(32) })
    apply(run, flow({ step: 'lock', state: 'confirmed', detail: 'On the chain at height 12' }))
    apply(run, flow({ step: 'lock', state: 'done', answer: 'accepted', detail: 'The contract accepted' }))
    expect(run.steps.lock).toMatchObject({ state: 'done', answer: 'accepted', detail: 'The contract accepted' })
  })

  it('takes an expected refusal as a finished step, not a failed one', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ step: 'wrong', state: 'done', answer: 'refused', detail: 'The contract refused, as it should' }))
    expect(run.steps.wrong.state).toBe('done')
    expect(run.steps.wrong.answer).toBe('refused')
  })

  it('never moves a settled step backwards', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ step: 'lock', state: 'done', answer: 'accepted' }))
    apply(run, flow({ step: 'lock', state: 'active' }))
    expect(run.steps.lock.state).toBe('done')
  })

  it('keeps a wait as seconds to go from when it began', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ step: 'wait', state: 'active', until: 1_700_000_100, now: 1_700_000_040 }, 5000))
    expect(run.steps.wait).toMatchObject({ state: 'active', waitSeconds: 60, waitFrom: 5000 })
  })

  it('ends on the result line', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ result: 'pass', detail: 'Bob was paid' }, 9000))
    expect(run).toMatchObject({ status: 'pass', summary: 'Bob was paid', ended: 9000 })
    close(run, 0, 9500)
    expect(run.status).toBe('pass')
  })

  it('does not pass a run that ended without a result, and fails what was in flight', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, flow({ step: 'lock', state: 'done', answer: 'accepted' }))
    apply(run, flow({ step: 'wrong', state: 'sent' }))
    close(run, -1, 9000)
    expect(run.status).toBe('fail')
    expect(run.steps.lock.state).toBe('done')
    expect(run.steps.wrong.state).toBe('failed')
    expect(run.steps.paid.state).toBe('pending')
  })

  it('keeps what was printed for a person, and ignores a step it was never told about', () => {
    const run = newRun('7', scenario, 1000)
    apply(run, line('alice funds bcrt1p…'))
    apply(run, line('@flow not json'))
    apply(run, flow({ step: 'no-such-step', state: 'done' }))
    expect(run.log.map((l) => l.text)).toEqual(['alice funds bcrt1p…', '@flow not json'])
    expect(parse('@flow {"step":"lock","state":"active"}')).toEqual({ step: 'lock', state: 'active' })
  })
})
