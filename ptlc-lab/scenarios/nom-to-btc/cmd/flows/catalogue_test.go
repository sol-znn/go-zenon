package main

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

// The catalogue is what the page draws before anything runs, so it has to be
// drawable: every step between parties the scenario names, every call with an
// outcome to hold it to.
func TestCatalogueIsDrawable(t *testing.T) {
	groupIDs := map[string]bool{}
	for _, g := range groups {
		groupIDs[g.ID] = true
	}
	seen := map[string]bool{}
	for _, s := range scenarios {
		if seen[s.ID] {
			t.Errorf("%s is listed twice", s.ID)
		}
		seen[s.ID] = true
		if !groupIDs[s.Group] {
			t.Errorf("%s is in group %q, which does not exist", s.ID, s.Group)
		}
		if s.run == nil || s.Title == "" || s.Shows == "" || s.Seconds == 0 {
			t.Errorf("%s is incomplete", s.ID)
		}
		party := map[string]bool{}
		for _, p := range s.Parties {
			party[p] = true
		}
		steps := map[string]bool{}
		for _, st := range s.Steps {
			if steps[st.ID] {
				t.Errorf("%s: step %s is listed twice", s.ID, st.ID)
			}
			steps[st.ID] = true
			if !party[st.From] || (st.To != "" && !party[st.To]) {
				t.Errorf("%s: step %s runs between %q and %q, and the scenario names %v", s.ID, st.ID, st.From, st.To, s.Parties)
			}
			if st.Kind == kindCall && st.Expect != accepted && st.Expect != refused && st.Expect != rejected {
				t.Errorf("%s: call %s has no expected outcome", s.ID, st.ID)
			}
			if st.Kind != kindCall && st.Expect != "" {
				t.Errorf("%s: step %s is not a call and expects %q", s.ID, st.ID, st.Expect)
			}
			// A block the node turns away has to be turned away for the
			// step's reason, or a node that is down passes for the rule.
			if st.Expect == rejected && st.Because == "" {
				t.Errorf("%s: step %s expects the network to turn it away and names no reason", s.ID, st.ID)
			}
		}
	}
	// The two Bitcoin scenarios are driven by another program's log. Every
	// step they promise needs a line that completes it.
	for path, id := range map[string]string{"happy": "swap-btc", "refund": "swap-btc-refund", "bad-terms": "swap-btc-bad-terms"} {
		done := map[string]bool{}
		for _, rule := range swapRules[path] {
			if rule.state == "done" {
				done[rule.step] = true
			}
		}
		for _, st := range find(id).Steps {
			if !done[st.ID] {
				t.Errorf("%s: no log line completes step %s", id, st.ID)
			}
		}
	}
}

// The tweaked lock opens with t + d and with nothing else, which is the whole
// of what links a swap's two entries.
func TestTweakedLockOpensWithTweakedSecret(t *testing.T) {
	for i := 0; i < 50; i++ {
		lock, err := newSecretLock()
		if err != nil {
			t.Fatal(err)
		}
		d, _ := btcec.NewPrivateKey()
		point, open := lock.tweaked(d)
		secret, err := open(lock.t)
		if err != nil {
			t.Fatal(err)
		}
		opened, _ := btcec.PrivKeyFromBytes(secret)
		if !bytes.Equal(opened.PubKey().SerializeCompressed(), point) {
			t.Fatal("t + d does not open T + d·G")
		}
		if bytes.Equal(point, lock.point) {
			t.Fatal("the tweaked lock is the untweaked one")
		}
	}
}

func TestImpossibleKeyIsNotAKey(t *testing.T) {
	for i := 0; i < 20; i++ {
		x, err := impossibleKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := schnorr.ParsePubKey(x); err == nil || len(x) != 32 {
			t.Fatal("impossibleKey returned a key that exists")
		}
	}
}

// The signature "for another contract" differs from the real one in the
// contract's address and nothing else, so its refusal is the contract binding
// at work and not some other difference.
func TestUnlockMessageForIsTheContractsOwn(t *testing.T) {
	id := types.HexToHashPanic("4bd1b48fe7253f6f30d9bdc6e0ef3ec28e9d2dbcc0ae60e4abf7cb3d4f8b1bd2")
	dest := types.ParseAddressPanic("z1qqjnwjjpnue8xmmpanz6csze6tcmtzzdtfsww7")
	for _, chain := range []uint64{1, 69} {
		got := unlockMessageFor(types.PtlcContract, chain, definition.PointTypeBIP340, id, dest)
		want := definition.GetPtlcUnlockMessage(chain, definition.PointTypeBIP340, id, dest)
		if !bytes.Equal(got, want) {
			t.Fatalf("chain %d: unlockMessageFor(ptlc) is not the contract's message", chain)
		}
		if bytes.Equal(unlockMessageFor(types.HtlcContract, chain, definition.PointTypeBIP340, id, dest), want) {
			t.Fatalf("chain %d: the message for another contract is the same message", chain)
		}
	}
}

// The weak-key scenario's lock and signature: the identity point and the
// signature (identity, 0) verify for any message under crypto/ed25519, which
// is the verifier the contract calls (wallet.VerifySignature).
func TestIdentityKeyOpensForAnyMessage(t *testing.T) {
	for _, msg := range [][]byte{[]byte("pay Carol"), make([]byte, 32)} {
		if !ed25519.Verify(identityKey(), msg, forgedSignature()) {
			t.Fatalf("the forged signature does not verify for %q", msg)
		}
	}
}
