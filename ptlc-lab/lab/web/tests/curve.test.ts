// The page's curve code against the vectors the Go and Dart implementations
// are held to: the official BIP-340 rows, and the adaptor pre-signatures the
// Dart SDK writes and the Go scenario reads.

import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { sha256 } from '@noble/hashes/sha2'

import {
  adaptorComplete,
  adaptorExtract,
  adaptorSign,
  adaptorVerify,
  bip340PublicKey,
  bip340Sign,
  bip340Verify,
  lockOf,
  makeWitness,
  pointOf,
  randomScalar,
  randomSecret,
  scalarProblem,
  tweakPoint,
  tweakScalar,
  witnessOpens,
} from '../src/ptlc/curve.ts'
import { fromHex, toHex } from '../src/znn/primitives.ts'

const vectors = (path: string) => readFileSync(new URL(`../../../test/vectors/${path}`, import.meta.url), 'utf8')

const ALICE = 'z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d'
const BOB = 'z1qp3yph55qgresyytz83anynr2f4z39x2z3ej3e'
const ID = '47f57134b5d8308335b5c146e586cfdcb62dc476c0279dd6c04963ecca586a0e'

describe('BIP-340', () => {
  const rows = vectors('bip340/test-vectors.csv')
    .split(/\r?\n/)
    .slice(1)
    .filter((line) => line.trim() !== '')
    .map((line) => line.split(','))

  it(`passes the ${rows.length} official vectors`, () => {
    expect(rows.length).toBeGreaterThanOrEqual(15)
    for (const [index, sk, pk, aux, msg, sig, result] of rows) {
      const expected = result === 'TRUE'
      expect(bip340Verify(fromHex(pk), fromHex(msg), fromHex(sig)), `verify row ${index}`).toBe(expected)
      if (sk) {
        expect(toHex(bip340PublicKey(fromHex(sk))).toUpperCase(), `key row ${index}`).toBe(pk)
        expect(toHex(bip340Sign(fromHex(sk), fromHex(msg), fromHex(aux))).toUpperCase(), `sign row ${index}`).toBe(sig)
      }
    }
  })
})

describe('adaptor signatures', () => {
  const rows = JSON.parse(vectors('adaptor/dart-vectors.json')) as {
    secretKey: string
    publicKey: string
    message: string
    secret: string
    adaptor: string
    preSignature: string
    signature: string
  }[]

  it(`agrees with the Dart SDK on ${rows.length} pre-signatures`, () => {
    expect(rows.length).toBe(16)
    rows.forEach((row, at) => {
      const i = at + 1
      const sk = fromHex(row.secretKey)
      const msg = fromHex(row.message)
      const T = fromHex(row.adaptor)
      const pre = fromHex(row.preSignature)
      const sig = fromHex(row.signature)

      // The inputs the Dart test derives, so that the nonce can be checked too.
      expect(toHex(sha256(new Uint8Array([0x42, i])))).toBe(row.message)
      expect(toHex(adaptorSign(sk, msg, T, sha256(new Uint8Array([0x43, i]))))).toBe(row.preSignature)

      expect(toHex(bip340PublicKey(sk))).toBe(row.publicKey)
      expect(toHex(pointOf(fromHex(row.secret)))).toBe(row.adaptor)
      expect(adaptorVerify(fromHex(row.publicKey), msg, T, pre)).toBe(true)
      expect(bip340Verify(fromHex(row.publicKey), msg, pre.subarray(1))).toBe(false)
      expect(toHex(adaptorComplete(pre, fromHex(row.secret)))).toBe(row.signature)
      expect(bip340Verify(fromHex(row.publicKey), msg, sig)).toBe(true)
      expect(toHex(adaptorExtract(pre, sig))).toBe(row.secret)
    })
  })

  it('holds for random keys in every parity of key and nonce', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 48; i++) {
      const sk = randomScalar()
      const t = randomScalar()
      const T = pointOf(t)
      const msg = sha256(new Uint8Array([i]))
      const pre = adaptorSign(sk, msg, T, randomScalar())
      const pk = bip340PublicKey(sk)
      seen.add(`${pointOf(sk)[0]}${pre[0]}`)
      expect(adaptorVerify(pk, msg, T, pre)).toBe(true)
      const sig = adaptorComplete(pre, t)
      expect(bip340Verify(pk, msg, sig)).toBe(true)
      expect(toHex(adaptorExtract(pre, sig))).toBe(toHex(t))
      // A pre-signature for another point does not verify for this one.
      expect(adaptorVerify(pk, msg, pointOf(randomScalar()), pre)).toBe(false)
    }
    expect(seen.size).toBe(4)
  })
})

describe('tweaks', () => {
  it('commute with the base point: (t + d)·G = T + d·G', () => {
    for (let i = 0; i < 8; i++) {
      const t = randomScalar()
      const d = randomScalar()
      expect(toHex(pointOf(tweakScalar(t, d)))).toBe(toHex(tweakPoint(pointOf(t), d)))
    }
  })
})

describe('scalars', () => {
  it('are canonical or refused, as verifyPointScalar has it', () => {
    expect(scalarProblem(new Uint8Array(32))).toMatch(/zero/)
    expect(scalarProblem(fromHex('ff'.repeat(32)))).toMatch(/group order/)
    expect(scalarProblem(new Uint8Array(31))).toMatch(/32 bytes/)
    expect(scalarProblem(fromHex('fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364140'))).toBeNull()
    expect(scalarProblem(fromHex('fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141'))).toMatch(/group order/)
  })
})

describe('witnesses', () => {
  it('open their own lock and no other, for each point type', () => {
    for (const pointType of [0, 1, 2]) {
      const secret = randomSecret(pointType)
      const lock = lockOf(pointType, secret)
      const witness = makeWitness(pointType, secret, 69, ID, ALICE)
      expect(witnessOpens(pointType, lock, witness, 69, ID, ALICE), `type ${pointType}`).toBe(true)
      expect(witnessOpens(pointType, lockOf(pointType, randomSecret(pointType)), witness, 69, ID, ALICE)).toBe(false)
    }
  })

  it('of a key type are bound to chain, entry and destination', () => {
    for (const pointType of [0, 1]) {
      const secret = randomSecret(pointType)
      const lock = lockOf(pointType, secret)
      const witness = makeWitness(pointType, secret, 69, ID, ALICE)
      expect(witnessOpens(pointType, lock, witness, 1, ID, ALICE)).toBe(false)
      expect(witnessOpens(pointType, lock, witness, 69, ID, BOB)).toBe(false)
      expect(witnessOpens(pointType, lock, witness, 69, '22'.repeat(32), ALICE)).toBe(false)
    }
  })

  it('of the point type bind nothing, which is why the entry fixes the destination', () => {
    const secret = randomSecret(2)
    const witness = makeWitness(2, secret, 69, ID, ALICE)
    expect(witnessOpens(2, lockOf(2, secret), witness, 1, '22'.repeat(32), BOB)).toBe(true)
  })
})
