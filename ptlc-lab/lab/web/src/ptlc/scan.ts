// Reading the contract's own account chain.
//
// `embedded.ptlc.getById` is the contract's only read call: it answers for an
// entry that is still locked and says "data non existent" for one that was
// never made, was paid, or was returned. Everything else the lab shows (who
// opened an entry, with what witness, what was refused) comes from walking
// the chain of the contract address, which is also where a counterparty reads
// a revealed scalar.
//
// It has to be the contract's chain and not the payee's: a ProxyUnlock sits on
// the submitter's chain, and the submitter can be anyone.
//
// Every call a user makes appears there as a contract-receive block whose
// `pairedAccountBlock` is the user's send, whose `data` is the eight-byte
// outcome, and whose `descendantBlocks` are what the contract sent in answer.

import { BLOCK_TYPE_CONTRACT_RECEIVE } from '../znn/block.ts'
import type { AccountBlockJson } from '../znn/client.ts'
import { bytesToBigInt, fromBase64, toHex } from '../znn/primitives.ts'
import { decodeCall, type DecodedCall } from './contract.ts'

/** The receive block's data when the contract accepted the call. */
const CONTRACT_SUCCESS = 1n

export interface Payout {
  hash: string
  to: string
  amount: string
  tokenStandard: string
}

/** One call the contract processed, accepted or refused. */
export interface ContractEvent {
  /** Height on the contract's chain. */
  height: number
  momentumHeight: number
  timestamp: number
  /** The caller's send block. For a Create this is the entry's id. */
  sendHash: string
  from: string
  ok: boolean
  /** Null when the data is not one of the contract's four calls. */
  call: DecodedCall | null
  amount: string
  tokenStandard: string
  payouts: Payout[]
}

export type EntryState = 'locked' | 'paid' | 'returned'

export interface Entry {
  id: string
  creator: string
  tokenStandard: string
  amount: string
  expirationTime: number
  pointType: number
  /** Hex. */
  pointLock: string
  destination: string
  createdAt: number
  createdHeight: number
  state: EntryState
  /** The accepted Unlock, ProxyUnlock or Reclaim. */
  closedBy?: ContractEvent
  /** Hex: the signature or scalar the accepted unlock published. */
  witness?: string
  paidTo?: string
  /** Calls on this entry the contract refused. */
  refused: ContractEvent[]
}

export interface ScanState {
  /** Highest contract-chain height folded in. */
  height: number
  events: ContractEvent[]
  entries: Map<string, Entry>
}

export function emptyScan(): ScanState {
  return { height: 0, events: [], entries: new Map() }
}

function eventOf(block: AccountBlockJson): ContractEvent | null {
  if (block.blockType !== BLOCK_TYPE_CONTRACT_RECEIVE) return null
  const send = block.pairedAccountBlock
  if (!send) return null
  let status = 0n
  try {
    const data = fromBase64(block.data ?? '')
    if (data.length === 8) status = bytesToBigInt(data)
  } catch {
    /* an outcome that does not decode is not a success */
  }
  let call: DecodedCall | null = null
  try {
    call = decodeCall(fromBase64(send.data ?? ''))
  } catch {
    call = null
  }
  return {
    height: block.height,
    momentumHeight: block.confirmationDetail?.momentumHeight ?? 0,
    timestamp: block.confirmationDetail?.momentumTimestamp ?? 0,
    sendHash: send.hash,
    from: send.address,
    ok: status === CONTRACT_SUCCESS,
    call,
    amount: send.amount,
    tokenStandard: send.tokenStandard,
    payouts: (block.descendantBlocks ?? []).map((d) => ({
      hash: d.hash,
      to: d.toAddress,
      amount: d.amount,
      tokenStandard: d.tokenStandard,
    })),
  }
}

/**
 * Folds blocks of the contract's chain, in height order, into the state.
 * Blocks at or below `state.height` are skipped, so a page fetched twice
 * changes nothing. Returns the events it added.
 */
export function foldBlocks(state: ScanState, blocks: AccountBlockJson[]): ContractEvent[] {
  const added: ContractEvent[] = []
  for (const block of blocks) {
    if (block.height <= state.height) continue
    state.height = block.height
    const event = eventOf(block)
    if (!event) continue
    state.events.push(event)
    added.push(event)

    const call = event.call
    if (!call) continue
    if (call.method === 'Create') {
      // A refused Create made no entry; the contract sent the funds back.
      if (!event.ok) continue
      state.entries.set(event.sendHash, {
        id: event.sendHash,
        creator: event.from,
        tokenStandard: event.tokenStandard,
        amount: event.amount,
        expirationTime: call.expirationTime,
        pointType: call.pointType,
        pointLock: toHex(call.pointLock),
        destination: call.destination,
        createdAt: event.timestamp,
        createdHeight: event.height,
        state: 'locked',
        refused: [],
      })
      continue
    }
    const entry = state.entries.get(call.id)
    if (!entry) continue
    if (!event.ok) {
      entry.refused.push(event)
      continue
    }
    entry.closedBy = event
    if (call.method === 'Reclaim') {
      entry.state = 'returned'
    } else {
      entry.state = 'paid'
      entry.witness = toHex(call.witness)
      entry.paidTo = event.payouts[0]?.to
    }
  }
  return added
}

/**
 * The entries, newest first, as copies.
 *
 * Copies, every time. The fold changes an entry in place when it is paid or
 * returned, and it does so on the plain object, where a reactive view is not
 * looking. A bench that derives anything from an entry (which step of a swap
 * is next, say) keeps its old answer if it is handed the same object again:
 * the lookup returns what it returned before, so nothing downstream is told
 * to look. tests/scan-view.test.ts holds this.
 */
export function entriesForView(state: ScanState): Entry[] {
  return [...state.entries.values()]
    .map((entry) => ({ ...entry, refused: entry.refused.slice() }))
    .sort((a, b) => b.createdHeight - a.createdHeight)
}

/** The witness of the accepted unlock of `id`, as a counterparty would read it. */
export function revealedWitness(state: ScanState, id: string): string | undefined {
  return state.entries.get(id)?.witness
}
