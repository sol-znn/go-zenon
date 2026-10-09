// go-zenon's embedded-contract ABI (`vm/abi`), both directions, for the types
// the ptlc and plasma contracts use.
//
// The rules, read out of `vm/abi/pack.go` and `method.go`:
//
//   - method id = the first 4 bytes of SHA3-256("Name(type,type,...)"), over
//     the raw type strings from the ABI JSON. SHA3, not Keccak.
//   - 32-byte words, Ethereum-style head/tail: static arguments inline,
//     dynamic ones (`bytes`) as an offset in the head and [length,
//     right-padded data] in the tail.
//   - address, hash, tokenStandard: LEFT-padded to 32.
//   - int/uint: U256, left-padded.
//
// The decoder is strict the way the Syrius approval screen's is: an encoding
// that is not the canonical one is not decoded at all, so what the lab shows
// for a block is what the contract will read from it.

import {
  bytesToBigInt,
  concat,
  formatAddress,
  hash,
  leftPad,
  parseAddress,
  toHex,
} from './primitives.ts'

const WORD = 32

export type AbiType = 'address' | 'hash' | 'int64' | 'uint8' | 'uint256' | 'bytes'
export type AbiValue = string | number | bigint | Uint8Array

function rightPadToWord(b: Uint8Array): Uint8Array {
  const padded = new Uint8Array(Math.ceil(b.length / WORD) * WORD)
  padded.set(b)
  return padded
}

function u256(v: bigint): Uint8Array {
  if (v < 0n) throw new Error(`negative value ${v} is not encodable here`)
  const out = new Uint8Array(WORD)
  let x = v
  for (let i = WORD - 1; i >= 0 && x > 0n; i--) {
    out[i] = Number(x & 0xffn)
    x >>= 8n
  }
  return out
}

function toBigInt(v: AbiValue): bigint {
  if (typeof v === 'bigint') return v
  if (typeof v === 'number') {
    if (!Number.isInteger(v)) throw new Error(`${v} is not an integer`)
    return BigInt(v)
  }
  if (typeof v === 'string') return BigInt(v)
  throw new Error('cannot read bytes as a number')
}

function encodeStatic(type: AbiType, value: AbiValue): Uint8Array {
  switch (type) {
    case 'address':
      return leftPad(typeof value === 'string' ? parseAddress(value) : (value as Uint8Array), WORD)
    case 'hash': {
      const raw = value as Uint8Array
      if (raw.length !== WORD) throw new Error(`a hash is 32 bytes, got ${raw.length}`)
      return raw
    }
    case 'int64':
    case 'uint8':
    case 'uint256':
      return u256(toBigInt(value))
    default:
      throw new Error(`${type} is not static`)
  }
}

export function methodId(name: string, types: readonly AbiType[]): Uint8Array {
  return hash(new TextEncoder().encode(`${name}(${types.join(',')})`)).subarray(0, 4)
}

/** ABI-encoded call data: the 4-byte method id followed by the packed arguments. */
export function encodeCall(name: string, types: readonly AbiType[], values: readonly AbiValue[]): Uint8Array {
  if (types.length !== values.length) {
    throw new Error(`${name} takes ${types.length} arguments, got ${values.length}`)
  }
  const head: Uint8Array[] = []
  const tail: Uint8Array[] = []
  let tailOffset = types.length * WORD

  types.forEach((type, i) => {
    if (type === 'bytes') {
      head.push(u256(BigInt(tailOffset)))
      const raw = values[i] as Uint8Array
      const packed = concat(u256(BigInt(raw.length)), rightPadToWord(raw))
      tail.push(packed)
      tailOffset += packed.length
    } else {
      head.push(encodeStatic(type, values[i]))
    }
  })

  return concat(methodId(name, types), ...head, ...tail)
}

export type Decoded = string | bigint | Uint8Array

/**
 * Decodes packed arguments, or returns null when they are not the canonical
 * encoding of `types`: a short buffer, padding that is not zero, a tail that
 * does not start where the previous one ended, or bytes left over.
 */
export function decodeArguments(data: Uint8Array, types: readonly AbiType[]): Decoded[] | null {
  if (data.length < types.length * WORD || data.length % WORD !== 0) return null
  let end = types.length * WORD
  const out: Decoded[] = []

  for (let i = 0; i < types.length; i++) {
    const type = types[i]
    const word = data.subarray(i * WORD, (i + 1) * WORD)
    switch (type) {
      case 'address': {
        if (word.subarray(0, 12).some((b) => b !== 0)) return null
        out.push(formatAddress(word.subarray(12)))
        break
      }
      case 'hash':
        out.push(toHex(word))
        break
      case 'uint8':
      case 'int64':
      case 'uint256': {
        const v = bytesToBigInt(word)
        // A negative int64 is sign-extended to the word; the contract's own
        // values (an expiry) are never negative, so one is not decoded here.
        const limit = type === 'uint8' ? 1n << 8n : type === 'int64' ? 1n << 63n : 1n << 256n
        if (v >= limit) return null
        out.push(v)
        break
      }
      case 'bytes': {
        if (bytesToBigInt(word) !== BigInt(end)) return null
        if (data.length < end + WORD) return null
        const length = bytesToBigInt(data.subarray(end, end + WORD))
        if (length > BigInt(data.length)) return null
        const size = Number(length)
        const padded = Math.ceil(size / WORD) * WORD
        const start = end + WORD
        if (start + padded > data.length) return null
        if (data.subarray(start + size, start + padded).some((b) => b !== 0)) return null
        out.push(data.slice(start, start + size))
        end = start + padded
        break
      }
    }
  }
  return end === data.length ? out : null
}
