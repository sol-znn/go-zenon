package btc

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"

	"zenon-ptlc/scenarios/nom-to-btc/adaptor"
)

func keys(t *testing.T) (alice, bob *btcec.PrivateKey) {
	t.Helper()
	a := sha256.Sum256([]byte("alice"))
	b := sha256.Sum256([]byte("bob"))
	alice, _ = btcec.PrivKeyFromBytes(a[:])
	bob, _ = btcec.PrivKeyFromBytes(b[:])
	return
}

func xonly(p *btcec.PrivateKey) [32]byte {
	var x [32]byte
	copy(x[:], schnorr.SerializePubKey(p.PubKey()))
	return x
}

// The whole Bitcoin leg with no node: Alice pre-signs Bob's claim as an
// adaptor on T, the completed signature plus Bob's own spends the output in
// btcd's script engine, and the scalar Bob needs is what Alice reveals. The
// refund leaf spends only after the locktime, and only for Alice.
func TestClaimAndRefundInEngine(t *testing.T) {
	alice, bob := keys(t)
	params := &chaincfg.RegressionNetParams
	const locktime = 200

	out, err := NewSwapOutput(xonly(alice), xonly(bob), locktime, params)
	if err != nil {
		t.Fatal(err)
	}
	funding := wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}
	const value = int64(1_000_000)

	bobAddr, _ := KeyAddress(bob.PubKey(), params)
	aliceAddr, _ := KeyAddress(alice.PubKey(), params)

	// Alice's swap secret and its point
	secret := sha256.Sum256([]byte("the swap secret"))
	T, err := adaptor.PointOf(secret[:])
	if err != nil {
		t.Fatal(err)
	}

	// Bob builds the claim and Alice pre-signs it
	claim, err := NewSpend(funding, value, out.PkScript, bobAddr, 500, 0, wire.MaxTxInSequenceNum)
	if err != nil {
		t.Fatal(err)
	}
	h, err := claim.Sighash(out.ClaimLeaf)
	if err != nil {
		t.Fatal(err)
	}
	aux := sha256.Sum256([]byte("aux"))
	presig, err := adaptor.Sign(alice, h, T, aux)
	if err != nil {
		t.Fatal(err)
	}
	if !adaptor.Verify(out.AliceX[:], h, T, presig) {
		t.Fatal("Bob rejects the pre-signature")
	}

	// before the reveal Bob has nothing that spends
	bobSig, err := claim.SignLeaf(out.ClaimLeaf, bob)
	if err != nil {
		t.Fatal(err)
	}
	notYet := append(append([]byte{}, presig[1:33]...), presig[33:]...)
	if err := claim.SetClaimWitness(out, notYet, bobSig); err != nil {
		t.Fatal(err)
	}
	if err := claim.Execute(locktime - 50); err == nil {
		t.Fatal("the pre-signature spent the output")
	}

	// after the reveal he completes and spends
	aliceSig, err := adaptor.Complete(presig, secret[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.SetClaimWitness(out, aliceSig, bobSig); err != nil {
		t.Fatal(err)
	}
	if err := claim.Execute(locktime - 50); err != nil {
		t.Fatalf("claim does not spend: %v", err)
	}
	// and the signature on chain gives Alice's secret back to anyone holding the pre-signature
	got, err := adaptor.Extract(presig, aliceSig)
	if err != nil || !bytes.Equal(got, secret[:]) {
		t.Fatalf("extracted %x, want %x (%v)", got, secret, err)
	}

	// Bob alone cannot claim, whatever he puts in Alice's slot
	bogus, _ := claim.SignLeaf(out.ClaimLeaf, bob)
	if err := claim.SetClaimWitness(out, bogus, bobSig); err != nil {
		t.Fatal(err)
	}
	if err := claim.Execute(locktime - 50); err == nil {
		t.Fatal("Bob claimed without Alice's signature")
	}

	// refund: Alice alone, only once the locktime is reached
	refund, err := NewSpend(funding, value, out.PkScript, aliceAddr, 500, locktime, wire.MaxTxInSequenceNum-1)
	if err != nil {
		t.Fatal(err)
	}
	refundSig, err := refund.SignLeaf(out.RefundLeaf, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := refund.SetRefundWitness(out, refundSig); err != nil {
		t.Fatal(err)
	}
	if err := refund.Execute(locktime); err != nil {
		t.Fatalf("refund does not spend at the locktime: %v", err)
	}
	early, _ := NewSpend(funding, value, out.PkScript, aliceAddr, 500, locktime-1, wire.MaxTxInSequenceNum-1)
	earlySig, _ := early.SignLeaf(out.RefundLeaf, alice)
	_ = early.SetRefundWitness(out, earlySig)
	if err := early.Execute(locktime - 1); err == nil {
		t.Fatal("refund spent before the locktime")
	}
	bobRefund, _ := NewSpend(funding, value, out.PkScript, bobAddr, 500, locktime, wire.MaxTxInSequenceNum-1)
	bobRefundSig, _ := bobRefund.SignLeaf(out.RefundLeaf, bob)
	_ = bobRefund.SetRefundWitness(out, bobRefundSig)
	if err := bobRefund.Execute(locktime); err == nil {
		t.Fatal("Bob took the refund")
	}
}
