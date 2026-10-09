// The arithmetic around the contract: locks and their witnesses for the three
// point types, the tweak that makes two legs of a swap unlinkable, and the
// BIP-340 adaptor signature that carries a scalar to Bitcoin.
//
// The adaptor construction is the Dart SDK's (znn_sdk_dart-ptlc,
// lib/src/crypto/bip340.dart), step for step, down to how the nonce is
// derived, so that the three implementations in this repository (Go, Dart and
// this one) give the same bytes for the same inputs. tests/curve.test.ts holds
// it to the vectors the other two share.

import { ed25519 } from '@noble/curves/ed25519'
import { schnorr, secp256k1 } from '@noble/curves/secp256k1'

import { bigIntToBytes, bytesToBigInt, concat, equalBytes } from '../znn/primitives.ts'
import { POINT_BIP340, POINT_ED25519, POINT_SECP256K1, unlockMessage } from './contract.ts'

const Point = secp256k1.ProjectivePoint
const G = Point.BASE
export const ORDER = secp256k1.CURVE.n

const mod = (x: bigint) => ((x % ORDER) + ORDER) % ORDER
const tagged = schnorr.utils.taggedHash
const xOnly = (p: InstanceType<typeof Point>) => p.toRawBytes(true).subarray(1)

export function randomBytes(n: number): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(n))
}

/** A scalar the contract accepts as a witness: 32 bytes, nonzero, below the order. */
export function scalarProblem(scalar: Uint8Array): string | null {
  if (scalar.length !== 32) return `a scalar is 32 bytes, this is ${scalar.length}`
  const k = bytesToBigInt(scalar)
  if (k === 0n) return 'the scalar is zero'
  if (k >= ORDER) return 'the scalar is not below the group order'
  return null
}

function scalar(bytes: Uint8Array): bigint {
  const problem = scalarProblem(bytes)
  if (problem) throw new Error(problem)
  return bytesToBigInt(bytes)
}

export function randomScalar(): Uint8Array {
  return secp256k1.utils.randomPrivateKey()
}

/** T = t·G, compressed: the lock of a point-type entry. */
export function pointOf(t: Uint8Array): Uint8Array {
  return G.multiply(scalar(t)).toRawBytes(true)
}

/** T + d·G. Two entries locked to T and T + d·G show unrelated points. */
export function tweakPoint(point: Uint8Array, tweak: Uint8Array): Uint8Array {
  const sum = Point.fromHex(point).add(G.multiply(scalar(tweak)))
  if (sum.equals(Point.ZERO)) throw new Error('the tweak cancels the point')
  return sum.toRawBytes(true)
}

/** t + d mod n: the witness of the tweaked point. */
export function tweakScalar(t: Uint8Array, tweak: Uint8Array): Uint8Array {
  const sum = mod(scalar(t) + scalar(tweak))
  if (sum === 0n) throw new Error('the tweak cancels the scalar')
  return bigIntToBytes(sum)
}

// ---- locks and witnesses --------------------------------------------------------

/** A fresh secret of the kind a point type locks to. All three are 32 bytes. */
export function randomSecret(pointType: number): Uint8Array {
  return pointType === POINT_ED25519 ? ed25519.utils.randomPrivateKey() : randomScalar()
}

/** The lock a secret opens: a public key for the key types, the point for the point type. */
export function lockOf(pointType: number, secret: Uint8Array): Uint8Array {
  switch (pointType) {
    case POINT_ED25519:
      return ed25519.getPublicKey(secret)
    case POINT_BIP340:
      return schnorr.getPublicKey(secret)
    case POINT_SECP256K1:
      return pointOf(secret)
  }
  throw new Error('the point type is 0, 1 or 2')
}

/**
 * The bytes that go in an unlock's `signature` argument.
 *
 * For a key type that is a signature over the unlock message, and the
 * destination it names is the only one this witness can pay. For the point
 * type it is the scalar itself, which binds nothing; the entry's fixed
 * destination does.
 */
export function makeWitness(
  pointType: number,
  secret: Uint8Array,
  chainIdentifier: number,
  id: string,
  destination: string,
): Uint8Array {
  switch (pointType) {
    case POINT_ED25519:
      return ed25519.sign(unlockMessage(chainIdentifier, pointType, id, destination), secret)
    case POINT_BIP340:
      return schnorr.sign(unlockMessage(chainIdentifier, pointType, id, destination), secret, randomBytes(32))
    case POINT_SECP256K1:
      scalar(secret)
      return secret.slice()
  }
  throw new Error('the point type is 0, 1 or 2')
}

/** What the contract's `verifyPtlcWitness` will decide, decided here first. */
export function witnessOpens(
  pointType: number,
  lock: Uint8Array,
  witness: Uint8Array,
  chainIdentifier: number,
  id: string,
  destination: string,
): boolean {
  try {
    switch (pointType) {
      case POINT_ED25519:
        return ed25519.verify(witness, unlockMessage(chainIdentifier, pointType, id, destination), lock)
      case POINT_BIP340:
        return schnorr.verify(witness, unlockMessage(chainIdentifier, pointType, id, destination), lock)
      case POINT_SECP256K1:
        return scalarProblem(witness) === null && equalBytes(pointOf(witness), lock)
    }
  } catch {
    return false
  }
  return false
}

// ---- BIP-340 adaptor signatures -------------------------------------------------

function evenSecret(secretKey: Uint8Array): { d: bigint; pBytes: Uint8Array } {
  const d0 = scalar(secretKey)
  const P = G.multiply(d0)
  return { d: P.hasEvenY() ? d0 : ORDER - d0, pBytes: xOnly(P) }
}

function nonce(d: bigint, pBytes: Uint8Array, message: Uint8Array, auxRand: Uint8Array): bigint {
  const t = bigIntToBytes(d)
  const auxHash = tagged('BIP0340/aux', auxRand)
  for (let i = 0; i < 32; i++) t[i] ^= auxHash[i]
  const k = mod(bytesToBigInt(tagged('BIP0340/nonce', t, pBytes, message)))
  if (k === 0n) throw new Error('zero nonce')
  return k
}

function challenge(rBytes: Uint8Array, pBytes: Uint8Array, message: Uint8Array): bigint {
  return mod(bytesToBigInt(tagged('BIP0340/challenge', rBytes, pBytes, message)))
}

export function bip340PublicKey(secretKey: Uint8Array): Uint8Array {
  return schnorr.getPublicKey(secretKey)
}

export function bip340Sign(secretKey: Uint8Array, message: Uint8Array, auxRand: Uint8Array): Uint8Array {
  return schnorr.sign(message, secretKey, auxRand)
}

export function bip340Verify(publicKey: Uint8Array, message: Uint8Array, signature: Uint8Array): boolean {
  try {
    return schnorr.verify(signature, message, publicKey)
  } catch {
    return false
  }
}

/**
 * A pre-signature on the point `adaptor`: 33 bytes of R, compressed so that
 * its parity travels with it, then 32 bytes of s'. `R = k·G + T` and
 * `s' = k + e·d`. It is not a signature, and it says nothing about the secret
 * behind `adaptor`.
 */
export function adaptorSign(
  secretKey: Uint8Array,
  message: Uint8Array,
  adaptor: Uint8Array,
  auxRand: Uint8Array,
): Uint8Array {
  if (auxRand.length !== 32) throw new Error('auxRand is 32 bytes')
  const T = Point.fromHex(adaptor)
  const { d, pBytes } = evenSecret(secretKey)
  // The adaptor point is mixed into the auxiliary randomness so that the same
  // key, message and aux never give the same k for two adaptors.
  const aux = tagged('zenon/ptlc/adaptor-aux', auxRand, adaptor)
  const k0 = nonce(d, pBytes, message, aux)
  const R = G.multiply(k0).add(T)
  if (R.equals(Point.ZERO)) throw new Error('the nonce cancels the adaptor')
  const k = R.hasEvenY() ? k0 : ORDER - k0
  const e = challenge(xOnly(R), pBytes, message)
  const presig = concat(R.toRawBytes(true), bigIntToBytes(mod(k + e * d)))
  if (!adaptorVerify(pBytes, message, adaptor, presig)) throw new Error('the pre-signature does not verify')
  return presig
}

/** s'·G must equal (R - T) + e·P when R has even y, and (T - R) + e·P otherwise. */
export function adaptorVerify(
  publicKey: Uint8Array,
  message: Uint8Array,
  adaptor: Uint8Array,
  presig: Uint8Array,
): boolean {
  if (publicKey.length !== 32 || presig.length !== 65) return false
  try {
    const P = schnorr.utils.lift_x(bytesToBigInt(publicKey))
    const T = Point.fromHex(adaptor)
    const R = Point.fromHex(presig.subarray(0, 33))
    const sPrime = bytesToBigInt(presig.subarray(33))
    if (sPrime >= ORDER || sPrime === 0n) return false
    const e = challenge(xOnly(R), publicKey, message)
    const eP = e === 0n ? Point.ZERO : P.multiply(e)
    const expected = R.hasEvenY() ? R.subtract(T).add(eP) : T.subtract(R).add(eP)
    return !expected.equals(Point.ZERO) && expected.equals(G.multiply(sPrime))
  } catch {
    return false
  }
}

/** Completes a pre-signature with the adaptor secret: an ordinary BIP-340 signature. */
export function adaptorComplete(presig: Uint8Array, secret: Uint8Array): Uint8Array {
  if (presig.length !== 65) throw new Error('a pre-signature is 65 bytes')
  const R = Point.fromHex(presig.subarray(0, 33))
  const sPrime = bytesToBigInt(presig.subarray(33))
  const t = scalar(secret)
  const s = R.hasEvenY() ? mod(sPrime + t) : mod(sPrime - t)
  return concat(xOnly(R), bigIntToBytes(s))
}

/**
 * The adaptor secret, from a pre-signature and the signature completed from
 * it. A signature that was not completed from this pre-signature gives
 * garbage, not an error: check the result against the adaptor point.
 */
export function adaptorExtract(presig: Uint8Array, signature: Uint8Array): Uint8Array {
  if (presig.length !== 65) throw new Error('a pre-signature is 65 bytes')
  if (signature.length !== 64) throw new Error('a signature is 64 bytes')
  const R = Point.fromHex(presig.subarray(0, 33))
  const sPrime = bytesToBigInt(presig.subarray(33))
  const s = bytesToBigInt(signature.subarray(32))
  return bigIntToBytes(R.hasEvenY() ? mod(s - sPrime) : mod(sPrime - s))
}
