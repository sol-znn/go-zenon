// The ptlc embedded contract as a client sees it: its address, its four
// methods, the checks it applies to a lock, and the message a key-type witness
// signs. Written against go-zenon branch ptlc/release-0.0.9
// (vm/embedded/definition/ptlc.go, implementation/ptlc.go) and SPEC.md §1.

import { ed25519 } from '@noble/curves/ed25519'
import { secp256k1, schnorr } from '@noble/curves/secp256k1'

import { decodeArguments, encodeCall, methodId, type AbiType } from '../znn/abi.ts'
import {
  EMPTY_ADDRESS,
  concat,
  fromHex,
  hash,
  parseAddress,
  toHex,
  uint64ToBytes,
} from '../znn/primitives.ts'

export const PTLC_CONTRACT = 'z1qxemdeddedxptlcxxxxxxxxxxxxxxxxx6lqady'
/** On mainnet since 2023, never activated. The devnet activates it in genesis. */
export const PTLC_SPORK_ID = 'e59f1e97f1096a18c2bc8421345b2633b11b839adb153e96dc65034c95f83f8d'
export const UNLOCK_DOMAIN = 'zenon-ptlc-unlock:v1'

export const POINT_ED25519 = 0
export const POINT_BIP340 = 1
export const POINT_SECP256K1 = 2
export type PointType = 0 | 1 | 2

/**
 * No unlock is sent with less than this many seconds left before the entry's
 * expiry, by the chain's clock. An unlock is answered two momentums after it is
 * sent, and one the contract answers after the expiry is refused with its
 * witness already public: for a point lock that is the secret itself. The CLI
 * and the scenario programs keep the same margin.
 */
export const REVEAL_MARGIN = 60

export interface PointTypeInfo {
  type: PointType
  name: string
  lock: string
  lockSize: number
  witness: string
  witnessSize: number
  /** What the chain shows once the entry is opened. */
  shown: string
}

export const POINT_TYPES: readonly PointTypeInfo[] = [
  {
    type: 0,
    name: 'Ed25519 key',
    lock: 'a 32-byte Ed25519 public key',
    lockSize: 32,
    witness: 'a 64-byte signature over the unlock message',
    witnessSize: 64,
    shown: 'the signature',
  },
  {
    type: 1,
    name: 'BIP-340 key',
    lock: 'a 32-byte x-only secp256k1 key',
    lockSize: 32,
    witness: 'a 64-byte Schnorr signature over the unlock message',
    witnessSize: 64,
    shown: 'the signature',
  },
  {
    type: 2,
    name: 'secp256k1 point',
    lock: 'a 33-byte compressed point T',
    lockSize: 33,
    witness: 'the 32-byte scalar t with t·G = T',
    witnessSize: 32,
    shown: 'T and t',
  },
]

export function pointTypeInfo(type: number): PointTypeInfo | undefined {
  return POINT_TYPES.find((p) => p.type === type)
}

// ---- methods -------------------------------------------------------------------

export type MethodName = 'Create' | 'Unlock' | 'ProxyUnlock' | 'Reclaim'

interface MethodDef {
  name: MethodName
  inputs: readonly { name: string; type: AbiType }[]
}

/** The ABI, in the contract's own argument names. `signature` carries the witness. */
export const METHODS: readonly MethodDef[] = [
  {
    name: 'Create',
    inputs: [
      { name: 'expirationTime', type: 'int64' },
      { name: 'pointType', type: 'uint8' },
      { name: 'pointLock', type: 'bytes' },
      { name: 'destination', type: 'address' },
    ],
  },
  {
    name: 'Unlock',
    inputs: [
      { name: 'id', type: 'hash' },
      { name: 'signature', type: 'bytes' },
    ],
  },
  {
    name: 'ProxyUnlock',
    inputs: [
      { name: 'id', type: 'hash' },
      { name: 'destination', type: 'address' },
      { name: 'signature', type: 'bytes' },
    ],
  },
  { name: 'Reclaim', inputs: [{ name: 'id', type: 'hash' }] },
]

const types = (m: MethodDef) => m.inputs.map((i) => i.type)
const method = (name: MethodName) => METHODS.find((m) => m.name === name)!

export function methodIdHex(name: MethodName): string {
  return toHex(methodId(name, types(method(name))))
}

export function packCreate(
  expirationTime: number,
  pointType: number,
  pointLock: Uint8Array,
  destination: string,
): Uint8Array {
  return encodeCall('Create', types(method('Create')), [expirationTime, pointType, pointLock, destination])
}

export function packUnlock(id: string, witness: Uint8Array): Uint8Array {
  return encodeCall('Unlock', types(method('Unlock')), [fromHex(id), witness])
}

export function packProxyUnlock(id: string, destination: string, witness: Uint8Array): Uint8Array {
  return encodeCall('ProxyUnlock', types(method('ProxyUnlock')), [fromHex(id), destination, witness])
}

export function packReclaim(id: string): Uint8Array {
  return encodeCall('Reclaim', types(method('Reclaim')), [fromHex(id)])
}

export type DecodedCall =
  | { method: 'Create'; expirationTime: number; pointType: number; pointLock: Uint8Array; destination: string }
  | { method: 'Unlock'; id: string; witness: Uint8Array }
  | { method: 'ProxyUnlock'; id: string; destination: string; witness: Uint8Array }
  | { method: 'Reclaim'; id: string }

/** Reads a send block's data as a ptlc call, or null if it is not exactly one. */
export function decodeCall(data: Uint8Array): DecodedCall | null {
  if (data.length < 4) return null
  const id = toHex(data.subarray(0, 4))
  const def = METHODS.find((m) => methodIdHex(m.name) === id)
  if (!def) return null
  const args = decodeArguments(data.subarray(4), types(def))
  if (!args) return null
  switch (def.name) {
    case 'Create':
      return {
        method: 'Create',
        expirationTime: Number(args[0] as bigint),
        pointType: Number(args[1] as bigint),
        pointLock: args[2] as Uint8Array,
        destination: args[3] as string,
      }
    case 'Unlock':
      return { method: 'Unlock', id: args[0] as string, witness: args[1] as Uint8Array }
    case 'ProxyUnlock':
      return {
        method: 'ProxyUnlock',
        id: args[0] as string,
        destination: args[1] as string,
        witness: args[2] as Uint8Array,
      }
    case 'Reclaim':
      return { method: 'Reclaim', id: args[0] as string }
  }
}

// ---- the contract's own checks ------------------------------------------------

/**
 * `checkPointLock`, so that a lock the contract would refuse is refused here
 * first. A refused Create costs a block and two momentums and returns the
 * funds; one the page could have named in advance is a wasted minute. It also
 * refuses the one lock the contract takes and should not: an Ed25519 key that
 * is no key.
 */
export function lockProblem(pointType: number, lock: Uint8Array): string | null {
  const info = pointTypeInfo(pointType)
  if (!info) return 'the point type is 0, 1 or 2'
  if (lock.length !== info.lockSize) {
    return `a ${info.name} lock is ${info.lockSize} bytes, this is ${lock.length}`
  }
  if (pointType === POINT_ED25519) {
    // The contract checks only the length of an Ed25519 lock (GAPS G-17). Bytes
    // that decode to no point make an entry nothing opens; a point of small
    // order makes one that anything opens, because a signature anybody can
    // write down verifies under it.
    let point
    try {
      point = ed25519.ExtendedPoint.fromHex(lock, false)
    } catch {
      return 'these 32 bytes are not a point on Ed25519, so no signature would ever open the lock'
    }
    if (point.isSmallOrder()) {
      return 'this is a point of small order: a signature anybody can write down opens it. The contract would take the lock; the lab does not send it'
    }
  }
  if (pointType === POINT_BIP340) {
    try {
      schnorr.utils.lift_x(BigInt('0x' + toHex(lock)))
    } catch {
      return 'these 32 bytes are not the x coordinate of a point on secp256k1'
    }
  }
  if (pointType === POINT_SECP256K1) {
    if (lock[0] !== 0x02 && lock[0] !== 0x03) return 'a point lock is compressed: it starts with 02 or 03'
    try {
      secp256k1.ProjectivePoint.fromHex(lock)
    } catch {
      return 'this is not a point on secp256k1'
    }
  }
  return null
}

/** `checkPtlc`'s destination rule. */
export function destinationProblem(pointType: number, destination: string): string | null {
  const zero = destination === EMPTY_ADDRESS
  if (pointType === POINT_SECP256K1 && zero) {
    return 'a point lock must name who it pays: its witness is a bare scalar, and anyone who read it off the claim could otherwise claim first'
  }
  // By the address's type byte, not its name: the contract checks its own list
  // of embedded contracts, and an address of the contract type that is not on
  // the list passes it and can be paid no more than one that is.
  let contractType = false
  try {
    contractType = !zero && parseAddress(destination)[0] === CONTRACT_ADDRESS_BYTE
  } catch {
    contractType = false
  }
  if (contractType) {
    return 'a contract address cannot be the destination: the payout carries no data, no contract accepts that, and every unlock would be refused until the creator takes the funds back'
  }
  return null
}

/** The first byte of a contract's address; a user's is 0 (go-zenon common/types). */
const CONTRACT_ADDRESS_BYTE = 1

/**
 * What a key-type witness signs: SHA3-256 over 101 bytes that bind the chain,
 * the contract, the point type, the entry and the destination. A signature
 * made for one of them verifies for no other.
 */
export function unlockMessage(chainIdentifier: number, pointType: number, id: string, destination: string): Uint8Array {
  return hash(unlockPreimage(chainIdentifier, pointType, id, destination))
}

export function unlockPreimage(
  chainIdentifier: number,
  pointType: number,
  id: string,
  destination: string,
): Uint8Array {
  return concat(
    new TextEncoder().encode(UNLOCK_DOMAIN),
    uint64ToBytes(chainIdentifier),
    parseAddress(PTLC_CONTRACT),
    new Uint8Array([pointType]),
    fromHex(id),
    parseAddress(destination),
  )
}
