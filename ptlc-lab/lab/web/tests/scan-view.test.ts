// What the benches are handed has to change identity when an entry changes
// state. This went wrong once: the swap bench kept offering Alice's unlock
// after the chain had paid her, because the step it showed was computed from
// an entry object that had been changed in place underneath Vue.

import { readFileSync } from 'node:fs'
import { computed, reactive } from 'vue'
import { describe, expect, it } from 'vitest'

import { emptyScan, entriesForView, foldBlocks, type Entry } from '../src/ptlc/scan.ts'
import type { AccountBlockJson } from '../src/znn/client.ts'

const chain = JSON.parse(readFileSync(new URL('./fixtures/contract-chain.json', import.meta.url), 'utf8')).result
  .list as AccountBlockJson[]

describe('entries handed to a reactive view', () => {
  it('make a computed chain follow an entry from locked to paid', () => {
    const scan = emptyScan()
    const view = reactive({ entries: [] as Entry[] })

    // Heights 1 to 3 of the fixture: a Create, the contract's payout block, the Unlock that caused it.
    foldBlocks(scan, chain.slice(0, 1))
    view.entries = entriesForView(scan)
    const id = view.entries[0].id
    const entry = computed(() => view.entries.find((e) => e.id === id) ?? null)
    const step = computed(() => (entry.value?.state === 'paid' ? 'done' : 'now'))
    expect(step.value).toBe('now')

    foldBlocks(scan, chain.slice(1, 3))
    view.entries = entriesForView(scan)
    expect(entry.value?.state).toBe('paid')
    expect(step.value).toBe('done')
  })

  it('are not the objects the fold keeps', () => {
    const scan = emptyScan()
    foldBlocks(scan, chain)
    const a = entriesForView(scan)
    const b = entriesForView(scan)
    expect(a).toEqual(b)
    expect(a[0]).not.toBe(b[0])
    expect(a[0]).not.toBe(scan.entries.get(a[0].id))
    expect(a[0].createdHeight).toBeGreaterThan(a[a.length - 1].createdHeight)
  })
})
