// Building, signing and publishing an account block with a key the page holds.
//
// The hash computation is `chain/nom.AccountBlock.ComputeHash`, field for field
// and width for width. The order is a consensus rule: a field in the wrong
// place, or an amount serialised at a different width, produces a hash the node
// disagrees with, and it reports that as a bad signature, an error that points
// at the key rather than at the encoding. So this mirrors the original line by
// line instead of being written more tidily.

import type { Account } from './wallet.ts'
import { ZenonClient, type RequiredPow } from './client.ts'
import { minePoWNonce, powDataHash, type MiningOptions } from './pow.ts'
import {
  EMPTY_ADDRESS,
  EMPTY_HASH_DIGEST,
  ZERO_HASH_HEX,
  ZERO_ZTS_STRING,
  bigIntToBytes,
  concat,
  fromBase64,
  fromHex,
  hash,
  leftPad,
  parseAddress,
  parseZts,
  toBase64,
  toHex,
  uint64ToBytes,
} from './primitives.ts'

/** The only two block types a user account can produce. */
export const BLOCK_TYPE_USER_SEND = 2
export const BLOCK_TYPE_USER_RECEIVE = 3
export const BLOCK_TYPE_CONTRACT_SEND = 4
export const BLOCK_TYPE_CONTRACT_RECEIVE = 5

/** The only version go-zenon's verifier accepts. */
const BLOCK_VERSION = 1

const ZERO_NONCE = '0000000000000000'

/** An account block in the shape `ledger.publishRawTransaction` expects. */
export interface BlockJson {
  version: number
  chainIdentifier: number
  blockType: number
  hash: string
  previousHash: string
  height: number
  momentumAcknowledged: { hash: string; height: number }
  address: string
  toAddress: string
  /** A decimal string: go-zenon's `AccountBlockMarshal` reads amounts as text. */
  amount: string
  tokenStandard: string
  fromBlockHash: string
  descendantBlocks: never[]
  /** Base64. */
  data: string
  fusedPlasma: number
  difficulty: number
  nonce: string
  basePlasma: number
  usedPlasma: number
  changesHash: string
  publicKey: string
  signature: string
}

/** What a caller decides about a block. Everything else is the signer's. */
export interface BlockSpec {
  blockType?: number
  toAddress: string
  /** Base units. */
  amount?: bigint
  tokenStandard?: string
  data?: Uint8Array
  /** Only for a receive block, which names the send it answers. */
  fromBlockHash?: string
}

/**
 * A block with every field present and the signer's fields zeroed.
 *
 * It is what a wallet extension is handed, and it is complete on purpose:
 * Syrius's `AccountBlockTemplate.fromJson` runs `Hash.parse`, `Address.parse`
 * and `TokenStandard.parse` over whatever is there, so a missing field is a
 * parse error inside the wallet, not a default.
 */
export function blockTemplate(spec: BlockSpec, chainIdentifier: number): BlockJson {
  return {
    version: BLOCK_VERSION,
    chainIdentifier,
    blockType: spec.blockType ?? BLOCK_TYPE_USER_SEND,
    hash: ZERO_HASH_HEX,
    previousHash: ZERO_HASH_HEX,
    height: 0,
    momentumAcknowledged: { hash: ZERO_HASH_HEX, height: 0 },
    address: EMPTY_ADDRESS,
    toAddress: spec.toAddress,
    amount: (spec.amount ?? 0n).toString(),
    tokenStandard: spec.tokenStandard ?? ZERO_ZTS_STRING,
    fromBlockHash: spec.fromBlockHash ?? ZERO_HASH_HEX,
    descendantBlocks: [],
    data: toBase64(spec.data ?? new Uint8Array(0)),
    fusedPlasma: 0,
    difficulty: 0,
    nonce: '',
    basePlasma: 0,
    usedPlasma: 0,
    changesHash: ZERO_HASH_HEX,
    publicKey: '',
    signature: '',
  }
}

/**
 * `chain/nom.AccountBlock.ComputeHash`.
 *
 * Note what is absent: basePlasma, usedPlasma, changesHash, publicKey and
 * signature are all set on the block and none is committed to.
 */
export function computeBlockHash(b: BlockJson): Uint8Array {
  return hash(
    uint64ToBytes(b.version),
    uint64ToBytes(b.chainIdentifier),
    uint64ToBytes(b.blockType),
    fromHex(b.previousHash),
    uint64ToBytes(b.height),
    concat(fromHex(b.momentumAcknowledged.hash), uint64ToBytes(b.momentumAcknowledged.height)),
    parseAddress(b.address),
    parseAddress(b.toAddress),
    bigIntToBytes(BigInt(b.amount || '0')),
    parseZts(b.tokenStandard),
    fromHex(b.fromBlockHash),
    // A block a client authors has no descendants: those are what a contract
    // emits in answer to one.
    EMPTY_HASH_DIGEST,
    hash(b.data ? fromBase64(b.data) : new Uint8Array(0)),
    uint64ToBytes(b.fusedPlasma),
    uint64ToBytes(b.difficulty),
    // Left-padded to 8: the nonce is eight bytes and a shorter hex string is
    // its leading zeros elided.
    leftPad(fromHex(b.nonce || ZERO_NONCE), 8),
  )
}

/**
 * Completes, signs and publishes one block from `account`, and answers its
 * hash. A published `ptlc.Create` hash is also the entry's id, so the id is
 * known the moment the block is built, two momentums before the entry exists.
 */
export async function publishBlock(
  client: ZenonClient,
  account: Account,
  chainIdentifier: number,
  spec: BlockSpec,
  options: MiningOptions = {},
): Promise<BlockJson> {
  const [frontier, momentum] = await Promise.all([
    client.frontierAccountBlock(account.address),
    client.frontierMomentum(),
  ])

  const block = blockTemplate(spec, chainIdentifier)
  block.previousHash = frontier ? frontier.hash : ZERO_HASH_HEX
  block.height = frontier ? frontier.height + 1 : 1
  block.momentumAcknowledged = { hash: momentum.hash, height: momentum.height }
  block.address = account.address
  block.nonce = ZERO_NONCE

  const pow: RequiredPow = await client.requiredPow(account.address, block.blockType, block.toAddress, block.data)

  if (pow.requiredDifficulty > 0) {
    block.fusedPlasma = pow.availablePlasma
    block.difficulty = pow.requiredDifficulty
    block.nonce = toHex(
      await minePoWNonce(pow.requiredDifficulty, powDataHash(block.address, block.previousHash), options),
    )
  } else {
    block.fusedPlasma = pow.basePlasma
  }
  block.basePlasma = pow.basePlasma
  block.usedPlasma = pow.basePlasma

  const digest = computeBlockHash(block)
  block.hash = toHex(digest)
  block.publicKey = toBase64(account.publicKey)
  block.signature = toBase64(account.sign(digest))

  await client.publish(block)
  return block
}
