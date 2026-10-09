// The page's encoder and message against go-zenon's: the method ids SPEC.md
// pins, the unlock-message known answers the Go and Dart suites share, and a
// contract chain a devnet actually produced.

import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import {
  decodeCall,
  destinationProblem,
  lockProblem,
  methodIdHex,
  packCreate,
  packProxyUnlock,
  packReclaim,
  packUnlock,
  unlockMessage,
  unlockPreimage,
} from '../src/ptlc/contract.ts'
import { emptyScan, foldBlocks } from '../src/ptlc/scan.ts'
import { blockTemplate, computeBlockHash, type BlockJson } from '../src/znn/block.ts'
import type { AccountBlockJson } from '../src/znn/client.ts'
import { EMPTY_ADDRESS, formatAddress, fromBase64, fromHex, toHex, ZNN_ZTS } from '../src/znn/primitives.ts'
import { Account } from '../src/znn/wallet.ts'

const vectors = (path: string) => new URL(`../../../test/vectors/${path}`, import.meta.url)
const fixture = (name: string) => JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), 'utf8'))

const ALICE = 'z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d'
const BOB = 'z1qp3yph55qgresyytz83anynr2f4z39x2z3ej3e'
const MNEMONIC =
  'abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million'

describe('method ids', () => {
  it('are the ones go-zenon computes (SPEC.md §1.1)', () => {
    expect(methodIdHex('Create')).toBe('22cec8ee')
    expect(methodIdHex('Unlock')).toBe('d33791d3')
    expect(methodIdHex('ProxyUnlock')).toBe('8bffc216')
    expect(methodIdHex('Reclaim')).toBe('7e003c8d')
  })
})

describe('unlock message', () => {
  const rows = JSON.parse(readFileSync(vectors('ptlc/unlock-message.json'), 'utf8')) as {
    chainIdentifier: number
    pointType: number
    id: string
    destination: string
    message: string
  }[]

  it(`matches go-zenon on ${rows.length} known answers`, () => {
    expect(rows.length).toBeGreaterThan(0)
    for (const row of rows) {
      expect(toHex(unlockMessage(row.chainIdentifier, row.pointType, row.id, row.destination))).toBe(row.message)
    }
  })

  it('hashes 101 bytes: domain, chain, contract, type, id, destination', () => {
    expect(unlockPreimage(69, 1, '11'.repeat(32), ALICE).length).toBe(20 + 8 + 20 + 1 + 32 + 20)
  })
})

describe('calls', () => {
  const id = '47f57134b5d8308335b5c146e586cfdcb62dc476c0279dd6c04963ecca586a0e'
  const point = fromHex('039ad08961b3521b59e9e601f2793ab49129b9bf2260c34a72463441383ffa15ca')
  const scalar = fromHex('0000000000000000000000000000000000000000000000000000000000004217')

  it('decode to what was encoded', () => {
    expect(decodeCall(packCreate(1791500000, 2, point, ALICE))).toEqual({
      method: 'Create',
      expirationTime: 1791500000,
      pointType: 2,
      pointLock: point,
      destination: ALICE,
    })
    expect(decodeCall(packUnlock(id, scalar))).toEqual({ method: 'Unlock', id, witness: scalar })
    expect(decodeCall(packProxyUnlock(id, BOB, scalar))).toEqual({
      method: 'ProxyUnlock',
      id,
      destination: BOB,
      witness: scalar,
    })
    expect(decodeCall(packReclaim(id))).toEqual({ method: 'Reclaim', id })
  })

  it('are refused when they are not the canonical encoding', () => {
    const good = packUnlock(id, scalar)
    expect(decodeCall(good.subarray(0, good.length - 1))).toBeNull()
    expect(decodeCall(new Uint8Array([...good, ...new Uint8Array(32)]))).toBeNull()
    const badOffset = good.slice()
    badOffset[4 + 63] ^= 1
    expect(decodeCall(badOffset)).toBeNull()
    expect(decodeCall(fromHex('deadbeef'))).toBeNull()
  })
})

describe('lock rules', () => {
  it('follow checkPointLock', () => {
    expect(lockProblem(2, fromHex('039ad08961b3521b59e9e601f2793ab49129b9bf2260c34a72463441383ffa15ca'))).toBeNull()
    expect(lockProblem(2, fromHex('04' + '11'.repeat(32)))).toMatch(/02 or 03/)
    expect(lockProblem(2, fromHex('02' + 'ff'.repeat(32)))).toMatch(/not a point/)
    expect(lockProblem(2, new Uint8Array(32))).toMatch(/33 bytes/)
    expect(lockProblem(1, fromHex('eecbc362762d57ce8cae5057d3e146c819eb18cf667e8dc1844853556e153e12'))).toBeNull()
    expect(lockProblem(1, fromHex('ff'.repeat(32)))).toMatch(/x coordinate/)
    expect(lockProblem(0, fromHex('ea4a6c63e29c520abef5507b132ec5f9954776aebebe7b92421eea691446d22c'))).toBeNull()
    expect(lockProblem(3, new Uint8Array(32))).toMatch(/0, 1 or 2/)
  })

  // The one rule the page holds a lock to that the contract does not: an
  // Ed25519 key has to be a key. The vectors are the ones the CLI's test uses.
  it('refuse an Ed25519 lock that is no point, or one of small order', () => {
    const smallOrder = [
      '0100000000000000000000000000000000000000000000000000000000000000',
      '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05',
      '0000000000000000000000000000000000000000000000000000000000000000',
      'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a',
      'ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f',
      'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa',
      '0000000000000000000000000000000000000000000000000000000000000080',
      '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85',
    ]
    for (const key of smallOrder) expect(lockProblem(0, fromHex(key))).toMatch(/small order/)
    expect(lockProblem(0, fromHex('02' + '00'.repeat(31)))).toMatch(/not a point/)
    expect(lockProblem(0, fromHex('ed' + 'ff'.repeat(30) + '7f'))).toMatch(/not a point/)
  })

  it('follow checkPtlc on the destination', () => {
    expect(destinationProblem(2, EMPTY_ADDRESS)).toMatch(/must name who it pays/)
    expect(destinationProblem(1, EMPTY_ADDRESS)).toBeNull()
    expect(destinationProblem(2, ALICE)).toBeNull()
    expect(destinationProblem(0, 'z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp')).toMatch(/contract address/)
  })
})

describe('dev accounts', () => {
  it('derive to the addresses the devnet funds', () => {
    expect(Account.fromMnemonic(MNEMONIC, 1).address).toBe(ALICE)
    expect(Account.fromMnemonic(MNEMONIC, 3).address).toBe(BOB)
  })
})

describe('a contract chain the devnet produced', () => {
  const chain = fixture('contract-chain.json').result.list as AccountBlockJson[]

  it('gives back each send block hash from its fields', () => {
    let checked = 0
    for (const block of chain) {
      const send = block.pairedAccountBlock as (AccountBlockJson & BlockJson) | null
      if (!send || send.blockType !== 2) continue
      expect(toHex(computeBlockHash(send))).toBe(send.hash)
      checked++
    }
    expect(checked).toBeGreaterThan(20)
  })

  it('folds into entries and outcomes', () => {
    const state = emptyScan()
    const events = foldBlocks(state, chain)
    expect(state.height).toBe(41)
    // Contract-send blocks sit on the chain too and are not calls.
    expect(events.length).toBe(chain.filter((b) => b.blockType === 5).length)
    expect(events.every((e) => e.call !== null)).toBe(true)

    const refused = events.filter((e) => !e.ok)
    expect(refused.map((e) => [e.height, e.call!.method])).toEqual([
      [18, 'Create'],
      [20, 'Unlock'],
      [24, 'ProxyUnlock'],
      [38, 'Reclaim'],
      [39, 'Unlock'],
    ])
    // The refused Create made no entry and got its funds back.
    expect(state.entries.has(refused[0].sendHash)).toBe(false)
    expect(refused[0].payouts).toHaveLength(1)
    expect(refused[0].payouts[0].to).toBe(refused[0].from)

    const entries = [...state.entries.values()]
    expect(entries.length).toBe(events.filter((e) => e.ok && e.call!.method === 'Create').length)
    for (const entry of entries) {
      if (entry.state === 'paid') {
        expect(entry.witness).toBeDefined()
        expect(entry.paidTo).toBeDefined()
        expect(entry.closedBy!.payouts[0].amount).toBe(entry.amount)
      }
      if (entry.state === 'returned') expect(entry.closedBy!.payouts[0].to).toBe(entry.creator)
    }
    expect(entries.some((e) => e.state === 'paid')).toBe(true)
    expect(entries.some((e) => e.state === 'returned')).toBe(true)

    // Folding the same blocks again changes nothing.
    expect(foldBlocks(state, chain)).toHaveLength(0)
  })

  it('reads a proxy unlock submitted by someone other than the payee', () => {
    const state = emptyScan()
    foldBlocks(state, chain)
    const proxied = [...state.entries.values()].find((e) => e.closedBy?.call?.method === 'ProxyUnlock')!
    expect(proxied).toBeDefined()
    expect(proxied.paidTo).not.toBe(proxied.closedBy!.from)
    expect(proxied.witness).toBeDefined()
  })
})

describe('the template a wallet is handed', () => {
  it('carries every field, with the signer\'s zeroed', () => {
    const data = packReclaim('11'.repeat(32))
    const t = blockTemplate({ toAddress: ALICE, amount: 5n, tokenStandard: ZNN_ZTS, data }, 69)
    expect(Object.keys(t).sort()).toEqual(
      [
        'address', 'amount', 'basePlasma', 'blockType', 'chainIdentifier', 'changesHash', 'data',
        'descendantBlocks', 'difficulty', 'fromBlockHash', 'fusedPlasma', 'hash', 'height',
        'momentumAcknowledged', 'nonce', 'previousHash', 'publicKey', 'signature', 'toAddress',
        'tokenStandard', 'usedPlasma', 'version',
      ].sort(),
    )
    expect(t.amount).toBe('5')
    expect(fromBase64(t.data)).toEqual(data)
    expect(t.address).toBe(EMPTY_ADDRESS)
    // It must survive the JSON round trip the extension's transport puts it through.
    expect(JSON.parse(JSON.stringify(t))).toEqual(t)
  })
})

describe('destination by address type', () => {
  it('refuses an address of the contract type that is on no list', () => {
    // twenty bytes, the first 1: the contract type, but no embedded contract's
    const core = new Uint8Array(20)
    core[0] = 1
    core[19] = 7
    const made = formatAddress(core)
    expect(destinationProblem(1, made)).toMatch(/contract address/)
    expect(destinationProblem(1, ALICE)).toBeNull()
  })
})
