package adaptor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func scalarBytes(v uint32) []byte {
	var k btcec.ModNScalar
	k.SetInt(v)
	b := k.Bytes()
	return b[:]
}

// Every combination of key parity and nonce parity must complete to a
// signature btcec accepts and extract the secret that went in.
func TestAllParities(t *testing.T) {
	var evenP, oddP, evenR, oddR int
	for i := uint32(1); i <= 40; i++ {
		priv, pub := btcec.PrivKeyFromBytes(scalarBytes(1000 + i*7919))
		if pub.Y().Bit(0) == 0 {
			evenP++
		} else {
			oddP++
		}
		secret := scalarBytes(50000 + i*104729)
		T, err := PointOf(secret)
		if err != nil {
			t.Fatal(err)
		}
		msg := sha256.Sum256([]byte{byte(i)})
		aux := sha256.Sum256([]byte{byte(i), byte(i)})

		presig, err := Sign(priv, msg[:], T, aux)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if presig[0] == 0x02 {
			evenR++
		} else {
			oddR++
		}
		px := schnorr.SerializePubKey(pub)
		if !Verify(px, msg[:], T, presig) {
			t.Fatalf("case %d: pre-signature does not verify", i)
		}
		wrongT, _ := PointOf(scalarBytes(50001 + i*104729))
		if Verify(px, msg[:], wrongT, presig) {
			t.Fatalf("case %d: pre-signature verifies on the wrong point", i)
		}
		// a pre-signature is not a signature
		if s, err := schnorr.ParseSignature(append(append([]byte{}, presig[1:33]...), presig[33:]...)); err == nil && s.Verify(msg[:], pub) {
			t.Fatalf("case %d: pre-signature is already a signature", i)
		}

		sig, err := Complete(presig, secret)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := schnorr.ParseSignature(sig)
		if err != nil {
			t.Fatalf("case %d: completed signature does not parse: %v", i, err)
		}
		if !parsed.Verify(msg[:], pub) {
			t.Fatalf("case %d: completed signature does not verify", i)
		}
		got, err := Extract(presig, sig)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, secret) {
			t.Fatalf("case %d: extracted %x, want %x", i, got, secret)
		}
	}
	for name, n := range map[string]int{"even P": evenP, "odd P": oddP, "even R": evenR, "odd R": oddR} {
		if n == 0 {
			t.Fatalf("no %s case was exercised", name)
		}
	}
}

// vectors.json is written by the Dart SDK branch's test
// (znn_sdk_dart-ptlc/test/crypto/adaptor_vectors_test.dart) and read here, so
// that the two implementations agree byte for byte on pre-signatures made
// from the same key, message, adaptor and auxiliary randomness. The nonce
// derivation differs between the two by design, so what is compared is that
// each side verifies, completes and extracts the other's pre-signatures.
func TestDartVectors(t *testing.T) {
	path := "../../../test/vectors/adaptor/dart-vectors.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no Dart vectors at %s: %v", path, err)
	}
	var rows []struct {
		SecretKey string `json:"secretKey"`
		PublicKey string `json:"publicKey"`
		Message   string `json:"message"`
		Secret    string `json:"secret"`
		Adaptor   string `json:"adaptor"`
		PreSig    string `json:"preSignature"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for i, r := range rows {
		px := mustHex(t, r.PublicKey)
		msg := mustHex(t, r.Message)
		T := mustHex(t, r.Adaptor)
		presig := mustHex(t, r.PreSig)
		secret := mustHex(t, r.Secret)
		if !Verify(px, msg, T, presig) {
			t.Fatalf("row %d: Dart pre-signature does not verify here", i)
		}
		sig, err := Complete(presig, secret)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(sig, mustHex(t, r.Signature)) {
			t.Fatalf("row %d: completed signature differs from Dart's", i)
		}
		got, err := Extract(presig, sig)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, secret) {
			t.Fatalf("row %d: extracted secret differs", i)
		}
		gotT, _ := PointOf(secret)
		if !bytes.Equal(gotT, T) {
			t.Fatalf("row %d: adaptor point is not the secret's point", i)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
