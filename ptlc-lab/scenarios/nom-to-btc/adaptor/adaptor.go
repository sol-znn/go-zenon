// Package adaptor implements single-signer BIP-340 adaptor signatures on
// secp256k1: a pre-signature on a point T that becomes an ordinary Schnorr
// signature once the scalar behind T is added, and gives that scalar back to
// whoever holds the pre-signature and sees the completed signature.
//
// The construction is the one in Blockstream's scriptless-scripts atomic swap
// note and libsecp256k1-zkp's schnorr_adaptor module. With the signer's nonce
// k and the adaptor point T, the pre-signature nonce is R = k*G + T and the
// challenge e is taken over x(R). BIP-340 requires the final signature's R to
// have even y, so:
//
//   - y(R) even: s' = k + e*d, s = s' + t, t = s - s'
//   - y(R) odd:  s' = -k + e*d, s = s' - t, t = s' - s
//
// R travels compressed, so its parity travels with it. The Dart SDK branch
// (znn_sdk_dart-ptlc, lib/src/crypto/bip340.dart) implements the same rules,
// and the test vectors in testdata/ are shared between the two.
package adaptor

import (
	"crypto/sha256"
	"errors"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

const (
	// PreSigSize is 33 bytes of compressed R followed by 32 bytes of s'.
	PreSigSize = 65
	nonceTag   = "zenon/ptlc/adaptor-nonce"
)

var (
	ErrBadInput   = errors.New("adaptor: malformed input")
	ErrBadPreSig  = errors.New("adaptor: pre-signature does not verify")
	ErrBadAdaptor = errors.New("adaptor: adaptor point is not a valid compressed point")
)

func taggedHash(tag string, chunks ...[]byte) [32]byte {
	th := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(th[:])
	h.Write(th[:])
	for _, c := range chunks {
		h.Write(c)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func challenge(rx, px [32]byte, msg []byte) btcec.ModNScalar {
	h := taggedHash("BIP0340/challenge", rx[:], px[:], msg)
	var e btcec.ModNScalar
	e.SetBytes(&h)
	return e
}

func xOnly(p *btcec.JacobianPoint) [32]byte {
	p.ToAffine()
	return *p.X.Bytes()
}

func hasEvenY(p *btcec.JacobianPoint) bool {
	p.ToAffine()
	return !p.Y.IsOdd()
}

// evenKey returns the signer's scalar negated if its public key has odd y,
// which is what BIP-340 signs with, and the x-only public key.
func evenKey(priv *btcec.PrivateKey) (btcec.ModNScalar, [32]byte) {
	d := priv.Key
	var p btcec.JacobianPoint
	btcec.ScalarBaseMultNonConst(&d, &p)
	if !hasEvenY(&p) {
		d.Negate()
	}
	return d, xOnly(&p)
}

// Sign produces a pre-signature by priv over msg on the adaptor point T
// (33 compressed bytes). aux is 32 bytes of fresh randomness; the nonce is
// derived from the key, the message, T and aux, so the same inputs with a
// different T never share a nonce.
func Sign(priv *btcec.PrivateKey, msg []byte, adaptor []byte, aux [32]byte) ([]byte, error) {
	tPub, err := btcec.ParsePubKey(adaptor)
	if err != nil || len(adaptor) != 33 {
		return nil, ErrBadAdaptor
	}
	var t btcec.JacobianPoint
	tPub.AsJacobian(&t)

	d, px := evenKey(priv)
	dBytes := d.Bytes()
	kh := taggedHash(nonceTag, dBytes[:], aux[:], adaptor, msg)
	var k btcec.ModNScalar
	if overflow := k.SetBytes(&kh); overflow != 0 || k.IsZero() {
		return nil, errors.New("adaptor: unusable nonce, retry with other aux")
	}

	var rPrime, r btcec.JacobianPoint
	btcec.ScalarBaseMultNonConst(&k, &rPrime)
	btcec.AddNonConst(&rPrime, &t, &r)
	r.ToAffine()
	if (r.X.IsZero() && r.Y.IsZero()) || r.Z.IsZero() {
		return nil, errors.New("adaptor: nonce cancels the adaptor point")
	}
	if !hasEvenY(&r) {
		k.Negate()
	}

	rx := xOnly(&r)
	e := challenge(rx, px, msg)
	var sPrime btcec.ModNScalar
	sPrime.Mul2(&e, &d).Add(&k)

	rPub := btcec.NewPublicKey(&r.X, &r.Y)
	out := make([]byte, 0, PreSigSize)
	out = append(out, rPub.SerializeCompressed()...)
	sb := sPrime.Bytes()
	out = append(out, sb[:]...)

	if !Verify(px[:], msg, adaptor, out) {
		return nil, ErrBadPreSig
	}
	return out, nil
}

// Verify checks that presig was made by the x-only key pubx over msg on the
// adaptor point: s'*G must equal (R - T) + e*P when R has even y, and
// (T - R) + e*P otherwise.
func Verify(pubx []byte, msg []byte, adaptor []byte, presig []byte) bool {
	if len(pubx) != 32 || len(presig) != PreSigSize || len(adaptor) != 33 {
		return false
	}
	pPub, err := schnorr.ParsePubKey(pubx)
	if err != nil {
		return false
	}
	tPub, err := btcec.ParsePubKey(adaptor)
	if err != nil {
		return false
	}
	rPub, err := btcec.ParsePubKey(presig[:33])
	if err != nil {
		return false
	}
	var sPrime btcec.ModNScalar
	if overflow := sPrime.SetByteSlice(presig[33:]); overflow {
		return false
	}

	var p, t, r btcec.JacobianPoint
	pPub.AsJacobian(&p)
	tPub.AsJacobian(&t)
	rPub.AsJacobian(&r)

	var px [32]byte
	copy(px[:], pubx)
	e := challenge(xOnly(&r), px, msg)

	// expected = ±(R - T) + e*P
	var diff, eP, expected, actual btcec.JacobianPoint
	tNeg := t
	tNeg.Y.Negate(1).Normalize()
	btcec.AddNonConst(&r, &tNeg, &diff)
	if !hasEvenY(&r) {
		diff.ToAffine()
		diff.Y.Negate(1).Normalize()
	}
	btcec.ScalarMultNonConst(&e, &p, &eP)
	btcec.AddNonConst(&diff, &eP, &expected)
	btcec.ScalarBaseMultNonConst(&sPrime, &actual)
	expected.ToAffine()
	actual.ToAffine()
	if expected.Z.IsZero() || actual.Z.IsZero() {
		return false
	}
	return expected.X.Equals(&actual.X) && expected.Y.Equals(&actual.Y)
}

// Complete turns a pre-signature into a BIP-340 signature with the adaptor
// secret t (32 bytes, canonical).
func Complete(presig []byte, secret []byte) ([]byte, error) {
	if len(presig) != PreSigSize || len(secret) != 32 {
		return nil, ErrBadInput
	}
	rPub, err := btcec.ParsePubKey(presig[:33])
	if err != nil {
		return nil, ErrBadInput
	}
	var sPrime, t btcec.ModNScalar
	if overflow := sPrime.SetByteSlice(presig[33:]); overflow {
		return nil, ErrBadInput
	}
	if overflow := t.SetByteSlice(secret); overflow || t.IsZero() {
		return nil, ErrBadInput
	}
	var r btcec.JacobianPoint
	rPub.AsJacobian(&r)
	if !hasEvenY(&r) {
		t.Negate()
	}
	var s btcec.ModNScalar
	s.Add2(&sPrime, &t)

	out := make([]byte, 0, 64)
	rx := xOnly(&r)
	out = append(out, rx[:]...)
	sb := s.Bytes()
	out = append(out, sb[:]...)
	return out, nil
}

// Extract recovers the adaptor secret from a pre-signature and the completed
// signature. The caller checks the result against the adaptor point; a
// signature not completed from this pre-signature yields a wrong scalar, not
// an error.
func Extract(presig []byte, sig []byte) ([]byte, error) {
	if len(presig) != PreSigSize || len(sig) != 64 {
		return nil, ErrBadInput
	}
	rPub, err := btcec.ParsePubKey(presig[:33])
	if err != nil {
		return nil, ErrBadInput
	}
	var sPrime, s btcec.ModNScalar
	if overflow := sPrime.SetByteSlice(presig[33:]); overflow {
		return nil, ErrBadInput
	}
	if overflow := s.SetByteSlice(sig[32:]); overflow {
		return nil, ErrBadInput
	}
	var r btcec.JacobianPoint
	rPub.AsJacobian(&r)
	// even y: t = s - s'; odd y: t = s' - s
	var t btcec.ModNScalar
	if hasEvenY(&r) {
		neg := sPrime
		neg.Negate()
		t.Add2(&s, &neg)
	} else {
		neg := s
		neg.Negate()
		t.Add2(&sPrime, &neg)
	}
	tb := t.Bytes()
	return tb[:], nil
}

// PointOf returns t*G compressed, the lock a scalar witness opens.
func PointOf(secret []byte) ([]byte, error) {
	var t btcec.ModNScalar
	if len(secret) != 32 {
		return nil, ErrBadInput
	}
	if overflow := t.SetByteSlice(secret); overflow || t.IsZero() {
		return nil, ErrBadInput
	}
	var p btcec.JacobianPoint
	btcec.ScalarBaseMultNonConst(&t, &p)
	p.ToAffine()
	return btcec.NewPublicKey(&p.X, &p.Y).SerializeCompressed(), nil
}
