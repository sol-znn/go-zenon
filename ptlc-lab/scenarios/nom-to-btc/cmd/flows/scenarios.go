package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	"zenon-ptlc/scenarios/nom-to-btc/keyswap"
	"zenon-ptlc/scenarios/nom-to-btc/nom"
)

// An entry made by a scenario that is not about time gets ten minutes: long
// enough that nothing here races it, short enough that a run that died half
// way leaves funds its owner can soon take back.
const comfortable = int64(600)

func (r *runner) in(seconds int64) (int64, error) {
	now, err := r.cl.Now()
	return now + seconds, err
}

// ---- locks and keys ----------------------------------------------------------

// secretLock is a secret and the point lock it opens: 32 bytes t, and the
// compressed point t·G.
type secretLock struct {
	key   *btcec.PrivateKey
	t     []byte
	point []byte
}

func newSecretLock() (*secretLock, error) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return &secretLock{key: key, t: key.Serialize(), point: key.PubKey().SerializeCompressed()}, nil
}

// tweaked returns the lock for t + d: the point T + d·G and the secret that
// opens it. Someone who sees only the two points cannot tell they are related,
// which is why the two entries of a swap do not look like a pair.
func (s *secretLock) tweaked(d *btcec.PrivateKey) (point []byte, open func(t []byte) ([]byte, error)) {
	var T, D, sum btcec.JacobianPoint
	s.key.PubKey().AsJacobian(&T)
	d.PubKey().AsJacobian(&D)
	btcec.AddNonConst(&T, &D, &sum)
	sum.ToAffine()
	point = btcec.NewPublicKey(&sum.X, &sum.Y).SerializeCompressed()
	open = func(t []byte) ([]byte, error) {
		var a btcec.ModNScalar
		if overflow := a.SetByteSlice(t); overflow {
			return nil, errors.New("the secret read from the chain is not a scalar")
		}
		a.Add(&d.Key)
		if a.IsZero() {
			return nil, errors.New("the tweaked secret is zero")
		}
		b := a.Bytes()
		return b[:], nil
	}
	return point, open
}

// A secret that is not the lock's: any other valid scalar.
func wrongSecret() ([]byte, error) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return key.Serialize(), nil
}

// bitcoinKey is a BIP-340 key: the 32-byte x-only public key is the lock, and
// a Schnorr signature over the contract's unlock message opens it.
type bitcoinKey struct {
	key  *btcec.PrivateKey
	lock []byte
}

func newBitcoinKey() (*bitcoinKey, error) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return &bitcoinKey{key: key, lock: schnorr.SerializePubKey(key.PubKey())}, nil
}

// sign says "pay destination" for one entry. The message binds the chain, the
// lock type, the entry and the address, so the signature is good for nothing
// else.
func (k *bitcoinKey) sign(r *runner, id types.Hash, destination types.Address) ([]byte, error) {
	message, err := r.cl.UnlockMessage(nom.PointTypeBIP340, id, destination)
	if err != nil {
		return nil, err
	}
	return k.signMessage(message)
}

func (k *bitcoinKey) signMessage(message []byte) ([]byte, error) {
	sig, err := schnorr.Sign(k.key, message)
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}

// unlockMessageFor is the contract's unlock message with any contract address
// in place of its own: what a signature made for another contract signs.
// With types.PtlcContract it is definition.GetPtlcUnlockMessage, which the
// catalogue test holds it to.
func unlockMessageFor(contract types.Address, chain uint64, pointType uint8, id types.Hash, destination types.Address) []byte {
	return crypto.Hash(common.JoinBytes(
		[]byte(definition.PtlcUnlockMessageDomain),
		common.Uint64ToBytes(chain),
		contract.Bytes(),
		[]byte{pointType},
		id.Bytes(),
		destination.Bytes(),
	))
}

// An x coordinate with no point on the curve above it: a BIP-340 "key" nobody
// can hold. About half of all 32-byte strings are one.
func impossibleKey() ([]byte, error) {
	for {
		x := make([]byte, 32)
		if _, err := rand.Read(x); err != nil {
			return nil, err
		}
		if _, err := schnorr.ParsePubKey(x); err != nil {
			return x, nil
		}
	}
}

var nobody = types.Address{}

// ---- three ways to lock ------------------------------------------------------

func runSecret(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	r.note("secret", "A random number, and a lock only that number opens")
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	r.note("tell", "Off the chain; nobody else sees it yet")
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Bob opened the lock with the secret and was paid 1 ZNN", nil
}

func runWalletKey(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	// The lock is Bob's account key itself. No destination is fixed: the
	// signature will name it.
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeEd25519, r.bob.Public, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	message, err := r.cl.UnlockMessage(nom.PointTypeEd25519, id, r.bob.Address)
	if err != nil {
		return "", err
	}
	signature := r.bob.Sign(message)
	r.note("sign", "The signature is only good for this lock, and only to pay Bob")
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, signature) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Bob's wallet signature opened the lock and he was paid 1 ZNN", nil
}

func runBitcoinKey(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	key, err := newBitcoinKey()
	if err != nil {
		return "", err
	}
	r.note("key", "The same kind of key a Taproot address has")
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, key.lock, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	signature, err := key.sign(r, id, r.bob.Address)
	if err != nil {
		return "", err
	}
	r.note("sign", "A Schnorr signature, the kind Bitcoin checks")
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, signature) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "A Bitcoin-style signature opened the lock and Bob was paid 1 ZNN", nil
}

func runRelay(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	key, err := newBitcoinKey()
	if err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, key.lock, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	signature, err := key.sign(r, id, r.bob.Address)
	if err != nil {
		return "", err
	}
	r.note("sign", "Bob needs no funds and sends nothing himself")
	r.note("hand", "Carol could be a service that sends calls for others")
	deliver, err := r.call("deliver", func() (types.Hash, error) { return r.cl.ProxyUnlock(r.carol, id, r.bob.Address, signature) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", deliver, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Carol delivered Bob's signature and the 1 ZNN went to Bob", nil
}

// ---- what the contract refuses -----------------------------------------------

func runWrongSecret(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	wrong, err := wrongSecret()
	if err != nil {
		return "", err
	}
	r.note("secret", "A random number, and a lock only that number opens")
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	if _, err := r.call("wrong", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, wrong) }); err != nil {
		return "", err
	}
	unlock, err := r.call("right", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "The wrong secret was refused, and the right one still paid Bob", nil
}

func runStolenSignature(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	key, err := newBitcoinKey()
	if err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, key.lock, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	signature, err := key.sign(r, id, r.bob.Address)
	if err != nil {
		return "", err
	}
	r.note("sign", "The signature covers who is paid")
	if _, err := r.call("steal", func() (types.Hash, error) { return r.cl.ProxyUnlock(r.carol, id, r.carol.Address, signature) }); err != nil {
		return "", err
	}
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, signature) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Carol's copy of the signature was refused, and Bob was paid", nil
}

func runFrontRun(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	if _, err := r.call("open", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	}); err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	// Carol has the real secret. It does not help her: the entry pays Bob.
	if _, err := r.call("snoop", func() (types.Hash, error) { return r.cl.Unlock(r.carol, id, lock.t) }); err != nil {
		return "", err
	}
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Knowing the secret did not let Carol take the funds; Bob was paid", nil
}

func runOnce(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	if _, err := r.call("again", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) }); err != nil {
		return "", err
	}
	return "The lock paid Bob once, and the second try did nothing", nil
}

func runBadLocks(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	bad, err := impossibleKey()
	if err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	if _, err := r.call("impossible", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, bad, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	}); err != nil {
		return "", err
	}
	// A good point under a type number the contract does not have.
	if _, err := r.call("no-type", func() (types.Hash, error) {
		return r.cl.Create(r.alice, 3, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	}); err != nil {
		return "", err
	}
	if _, err := r.call("empty", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, big.NewInt(0))
	}); err != nil {
		return "", err
	}
	// The same lock with a deadline a minute in the past. The node takes the
	// block, because only the contract knows the time, and the contract sends
	// the funds back.
	past, err := r.in(-60)
	if err != nil {
		return "", err
	}
	late, err := r.call("late", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, past, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	if err := r.paid("refund", late, r.alice.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "The three broken locks never left Alice's account, and the expired one was refunded", nil
}

func runSignedElsewhere(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	key, err := newBitcoinKey()
	if err != nil {
		return "", err
	}
	r.note("key", "The same kind of key a Taproot address has")
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, key.lock, nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	chain, err := r.cl.ChainIdentifier()
	if err != nil {
		return "", err
	}
	// Zenon's mainnet is chain 1; the devnet this runs on is another.
	other := uint64(1)
	if chain == other {
		other = 3
	}
	elsewhere, err := key.signMessage(unlockMessageFor(types.PtlcContract, other, nom.PointTypeBIP340, id, r.bob.Address))
	if err != nil {
		return "", err
	}
	if _, err := r.call("other-chain", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, elsewhere) }); err != nil {
		return "", err
	}
	forHtlc, err := key.signMessage(unlockMessageFor(types.HtlcContract, chain, nom.PointTypeBIP340, id, r.bob.Address))
	if err != nil {
		return "", err
	}
	if _, err := r.call("other-contract", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, forHtlc) }); err != nil {
		return "", err
	}
	signature, err := key.sign(r, id, r.bob.Address)
	if err != nil {
		return "", err
	}
	unlock, err := r.call("unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, signature) })
	if err != nil {
		return "", err
	}
	if err := r.paid("paid", unlock, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Bob's signatures for another network and another contract were refused, and the one for this lock paid him", nil
}

// ---- deadlines ---------------------------------------------------------------

func runDeadline(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	// Long enough for the lock and the early reclaim to be answered before it:
	// each takes two momentums, and a momentum can take 15 s on this host.
	expiry, err := r.in(100)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, lock.point, r.bob.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	if _, err := r.call("early", func() (types.Hash, error) { return r.cl.Reclaim(r.alice, id) }); err != nil {
		return "", err
	}
	if err := r.waitUntil("wait", expiry); err != nil {
		return "", err
	}
	// Bob has the right secret and Carol has plasma. Neither is enough now.
	if err := together(
		func() error {
			_, err := r.call("toolate", func() (types.Hash, error) { return r.cl.Unlock(r.bob, id, lock.t) })
			return err
		},
		func() error {
			_, err := r.call("stranger", func() (types.Hash, error) { return r.cl.Reclaim(r.carol, id) })
			return err
		},
	); err != nil {
		return "", err
	}
	reclaim, err := r.call("reclaim", func() (types.Hash, error) { return r.cl.Reclaim(r.alice, id) })
	if err != nil {
		return "", err
	}
	if err := r.paid("back", reclaim, r.alice.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "Only Alice could take the funds back, and only after the deadline", nil
}

// ---- swaps -------------------------------------------------------------------

// swapTerms is one swap: the secret Alice holds, the tweak the two agree, and
// the two entries. Alice's, 10 QSR for Bob on T + d·G, is made first and ends
// last. Bob's, 1 ZNN for Alice on T, is made once hers is on the chain and ends
// gap seconds before hers. Whenever Alice claims Bob's entry, and so publishes
// t, hers stays open to Bob for gap seconds more at the least.
//
// Until 2026-10-09 this ran the other way round, Bob first and Bob last, which
// is the order the "mistakes" group shows losing: the holder of the secret can
// claim the other entry without ever funding her own, or wait hers out, take it
// back, and claim afterwards.
type swapTerms struct {
	lock        *secretLock
	open        func([]byte) ([]byte, error)
	tweaked     []byte
	aliceEntry  types.Hash
	bobEntry    types.Hash
	aliceExpiry int64
	bobExpiry   int64
}

// aliceLocks makes the secret, the tweak and Alice's entry, which lasts
// aliceAfter seconds from now on the chain's clock.
func aliceLocks(r *runner, aliceAfter int64) (*swapTerms, error) {
	if err := r.need(r.alice, tenQsr, types.QsrTokenStandard); err != nil {
		return nil, err
	}
	if err := r.need(r.bob, oneZnn, types.ZnnTokenStandard); err != nil {
		return nil, err
	}
	lock, err := newSecretLock()
	if err != nil {
		return nil, err
	}
	tweak, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	point, open := lock.tweaked(tweak)
	r.note("agree", "With a second number they both know, so the two locks will not look alike")
	s := &swapTerms{lock: lock, open: open, tweaked: point}
	if s.aliceExpiry, err = r.in(aliceAfter); err != nil {
		return nil, err
	}
	s.aliceEntry, err = r.call("alice-lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.alice, point, r.bob.Address, s.aliceExpiry, types.QsrTokenStandard, tenQsr)
	})
	return s, err
}

// bobLocks reads Alice's entry, holds it to the terms, and makes Bob's, ending
// gap seconds before hers. checkStep, when given, is the step the reading is
// reported as; otherwise a problem is reported on Bob's lock.
func bobLocks(r *runner, s *swapTerms, gap int64, checkStep string) error {
	step := checkStep
	if step == "" {
		step = "bob-lock"
	}
	if checkStep != "" {
		r.active(checkStep, "")
	}
	entry, err := r.cl.WaitEntry(s.aliceEntry)
	if err != nil {
		return r.fail(step, "Alice's lock is not on the chain")
	}
	now, err := r.cl.Now()
	if err != nil {
		return r.fail(step, "Could not read the chain's clock")
	}
	s.bobExpiry = s.aliceExpiry - gap
	switch {
	case entry.Destination != r.bob.Address || entry.Amount.Cmp(tenQsr) != 0 || entry.TokenStandard != types.QsrTokenStandard ||
		entry.ExpirationTime != s.aliceExpiry || string(entry.PointLock) != string(s.tweaked):
		return r.fail(step, "Alice's lock is not what was agreed, so Bob locks nothing")
	case s.bobExpiry-now < 60:
		return r.fail(step, "Alice's lock leaves Bob too little time for a lock of his own that ends before hers")
	}
	if checkStep != "" {
		r.done(checkStep, "Right amount, right lock, pays Bob, and it outlasts the one he is about to make")
	}
	s.bobEntry, err = r.call("bob-lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.bob, s.lock.point, r.alice.Address, s.bobExpiry, types.ZnnTokenStandard, oneZnn)
	})
	return err
}

// opens reports whether a witness read from the chain is the scalar behind a
// point: what Bob checks before he relies on it.
func opens(witness, point []byte) bool {
	if len(witness) != 32 {
		return false
	}
	priv, _ := btcec.PrivKeyFromBytes(witness)
	return string(priv.PubKey().SerializeCompressed()) == string(point)
}

func runSwap(r *runner) (string, error) {
	s, err := aliceLocks(r, comfortable)
	if err != nil {
		return "", err
	}
	if err := bobLocks(r, s, comfortable/2, "bob-check"); err != nil {
		return "", err
	}
	// Alice reads Bob's entry before her secret leaves her hands.
	entry, err := r.cl.WaitEntry(s.bobEntry)
	if err != nil || entry.Destination != r.alice.Address || entry.Amount.Cmp(oneZnn) != 0 ||
		entry.TokenStandard != types.ZnnTokenStandard || string(entry.PointLock) != string(s.lock.point) {
		return "", r.fail("alice-unlock", "Bob's lock is not what was agreed, so Alice keeps her secret")
	}
	from, err := r.cl.ContractHeight()
	if err != nil {
		return "", err
	}
	aliceUnlock, err := r.call("alice-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.alice, s.bobEntry, s.lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("alice-paid", aliceUnlock, r.alice.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	// Bob was never told the secret. He finds it where Alice had to put it.
	r.active("bob-read", "")
	seen, _, err := r.cl.ScanWitness(s.bobEntry, from)
	if err != nil || seen == nil {
		return "", r.fail("bob-read", "Alice's secret is not on the contract's chain")
	}
	if !opens(seen, s.lock.point) {
		return "", r.fail("bob-read", "What the chain shows does not open Bob's lock")
	}
	bobSecret, err := s.open(seen)
	if err != nil {
		return "", r.fail("bob-read", err.Error())
	}
	r.done("bob-read", "It is in Alice's unlock, public to everyone now")
	bobUnlock, err := r.call("bob-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, s.aliceEntry, bobSecret) })
	if err != nil {
		return "", err
	}
	if err := r.paid("bob-paid", bobUnlock, r.bob.Address, tenQsr, types.QsrTokenStandard); err != nil {
		return "", err
	}
	return "Alice got 1 ZNN and Bob got 10 QSR, with nobody in between", nil
}

func runSwapAbandoned(r *runner) (string, error) {
	// Long enough for both locks to be answered before Bob's deadline.
	s, err := aliceLocks(r, 210)
	if err != nil {
		return "", err
	}
	if err := bobLocks(r, s, 80, ""); err != nil {
		return "", err
	}
	r.note("stall", "Her secret stays secret, so neither lock can be opened")
	if err := r.waitUntil("wait-bob", s.bobExpiry); err != nil {
		return "", err
	}
	bobReclaim, err := r.call("bob-reclaim", func() (types.Hash, error) { return r.cl.Reclaim(r.bob, s.bobEntry) })
	if err != nil {
		return "", err
	}
	if err := r.paid("bob-back", bobReclaim, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	if err := r.waitUntil("wait-alice", s.aliceExpiry); err != nil {
		return "", err
	}
	aliceReclaim, err := r.call("alice-reclaim", func() (types.Hash, error) { return r.cl.Reclaim(r.alice, s.aliceEntry) })
	if err != nil {
		return "", err
	}
	if err := r.paid("alice-back", aliceReclaim, r.alice.Address, tenQsr, types.QsrTokenStandard); err != nil {
		return "", err
	}
	return fmt.Sprintf("Both took their own funds back: %s to Bob, then %s to Alice", coins(oneZnn, types.ZnnTokenStandard), coins(tenQsr, types.QsrTokenStandard)), nil
}

// ---- the swap on key locks -----------------------------------------------------

// keySwap is the swap of SPEC.md §5: two key locks, each to a key its maker
// made for this swap, each naming the other party as the only address it may
// pay, and a pre-signature each way. The checks and the signing are
// keyswap's, the same the swap's own client makes (cmd/keyswap); the calls go
// through this runner so that the page can show each one answered.
type keySwap struct {
	secret, point        []byte
	aliceKey, bobKey     *btcec.PrivateKey
	aliceLeg, bobLeg     keyswap.Leg
	aliceEntry, bobEntry types.Hash
	forBob, forAlice     []byte // the pre-signatures, named by who holds them
	chain, from          uint64
}

// keySwapLocks takes the swap as far as both entries locked and both
// pre-signatures exchanged. aliceNames is the destination Alice's entry is
// made with: Bob, or nobody, which is the mistake. When it is nobody Bob does
// not look, and the step "bob-skips" says what his client would have said.
func keySwapLocks(r *runner, aliceNames types.Address) (*keySwap, error) {
	if err := r.need(r.alice, tenQsr, types.QsrTokenStandard); err != nil {
		return nil, err
	}
	if err := r.need(r.bob, oneZnn, types.ZnnTokenStandard); err != nil {
		return nil, err
	}
	s := &keySwap{}
	var err error
	if s.secret, s.point, err = keyswap.NewSecret(); err != nil {
		return nil, err
	}
	if s.aliceKey, err = keyswap.NewKey(); err != nil {
		return nil, err
	}
	if s.bobKey, err = keyswap.NewKey(); err != nil {
		return nil, err
	}
	if s.chain, err = r.cl.ChainIdentifier(); err != nil {
		return nil, err
	}
	if s.from, err = r.cl.ContractHeight(); err != nil {
		return nil, err
	}
	aliceExpiry, err := r.in(comfortable)
	if err != nil {
		return nil, err
	}
	s.aliceLeg = keyswap.Leg{Key: keyswap.XOnly(s.aliceKey), Destination: r.bob.Address, Token: types.QsrTokenStandard, Amount: tenQsr, Expiry: aliceExpiry}
	s.bobLeg = keyswap.Leg{Key: keyswap.XOnly(s.bobKey), Destination: r.alice.Address, Token: types.ZnnTokenStandard, Amount: oneZnn, Expiry: aliceExpiry - comfortable/2}
	r.note("agree", "Keys made for this swap and used for nothing else. Alice also makes a secret, and keeps it")

	if s.aliceEntry, err = r.call("alice-lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeBIP340, s.aliceLeg.Key, aliceNames, s.aliceLeg.Expiry, s.aliceLeg.Token, s.aliceLeg.Amount)
	}); err != nil {
		return nil, err
	}
	if s.forBob, err = keyswap.PreSign(s.aliceKey, s.chain, s.aliceEntry, s.aliceLeg, s.point); err != nil {
		return nil, r.fail("alice-presign", err.Error())
	}
	r.note("alice-presign", "It says \"pay Bob\" for her lock, and only the secret is missing from it")

	checkStep := "bob-check"
	if aliceNames != r.bob.Address {
		checkStep = "bob-skips"
	}
	r.active(checkStep, "")
	entry, err := r.cl.WaitEntry(s.aliceEntry)
	if err != nil {
		return nil, r.fail(checkStep, "Alice's lock is not on the chain")
	}
	if err := keyswap.VerifyPreSig(s.forBob, s.chain, s.aliceEntry, s.aliceLeg, s.point); err != nil {
		return nil, r.fail(checkStep, "Alice's half-finished signature is not what she said: "+err.Error())
	}
	problem := keyswap.CheckEntry(entry, s.aliceLeg)
	switch {
	case aliceNames == r.bob.Address && problem != nil:
		return nil, r.fail(checkStep, "Alice's lock is not what was agreed, so Bob locks nothing: "+problem.Error())
	case aliceNames == r.bob.Address:
		r.done(checkStep, "Right amount, her key, pays only Bob, outlasts the lock he is about to make, and the signature fits it")
	case problem == nil:
		return nil, r.fail(checkStep, "The lab's client found nothing wrong with a lock that names nobody")
	default:
		r.done(checkStep, "The lab's client would have stopped here: "+problem.Error())
	}

	if s.bobEntry, err = r.call("bob-lock", func() (types.Hash, error) {
		return r.cl.Create(r.bob, nom.PointTypeBIP340, s.bobLeg.Key, s.bobLeg.Destination, s.bobLeg.Expiry, s.bobLeg.Token, s.bobLeg.Amount)
	}); err != nil {
		return nil, err
	}
	if s.forAlice, err = keyswap.PreSign(s.bobKey, s.chain, s.bobEntry, s.bobLeg, s.point); err != nil {
		return nil, r.fail("bob-presign", err.Error())
	}
	r.note("bob-presign", "It says \"pay Alice\" for his lock, with the same piece missing")
	return s, nil
}

// aliceClaims is Alice completing Bob's pre-signature and taking his entry.
func (s *keySwap) aliceClaims(r *runner) error {
	sig, err := keyswap.Complete(s.forAlice, s.secret, s.chain, s.bobEntry, s.bobLeg)
	if err != nil {
		return r.fail("alice-unlock", err.Error())
	}
	unlock, err := r.call("alice-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.alice, s.bobEntry, sig) })
	if err != nil {
		return err
	}
	return r.paid("alice-paid", unlock, r.alice.Address, oneZnn, types.ZnnTokenStandard)
}

// bobReads is Bob finding Alice's signature on the contract's chain and
// working the secret out of it. It returns the signature that would open her
// entry for him.
func (s *keySwap) bobReads(r *runner) ([]byte, error) {
	r.active("bob-read", "")
	seen, _, err := r.cl.ScanWitness(s.bobEntry, s.from)
	if err != nil || seen == nil {
		return nil, r.fail("bob-read", "Alice's signature is not on the contract's chain")
	}
	secret, err := keyswap.Extract(s.forAlice, seen, s.point)
	if err != nil {
		return nil, r.fail("bob-read", "Her signature does not give the secret: "+err.Error())
	}
	sig, err := keyswap.Complete(s.forBob, secret, s.chain, s.aliceEntry, s.aliceLeg)
	if err != nil {
		return nil, r.fail("bob-read", err.Error())
	}
	r.done("bob-read", "Her finished signature, less the half-finished one he gave her, is the secret. Only he can do that sum")
	return sig, nil
}

func runKeySwap(r *runner) (string, error) {
	s, err := keySwapLocks(r, r.bob.Address)
	if err != nil {
		return "", err
	}
	// Alice reads Bob's entry and checks his pre-signature before her secret
	// is used for anything.
	r.active("alice-check", "")
	entry, err := r.cl.WaitEntry(s.bobEntry)
	if err != nil {
		return "", r.fail("alice-check", "Bob's lock is not on the chain")
	}
	if err := keyswap.CheckEntry(entry, s.bobLeg); err != nil {
		return "", r.fail("alice-check", "Bob's lock is not what was agreed, so Alice keeps her secret: "+err.Error())
	}
	if err := keyswap.VerifyPreSig(s.forAlice, s.chain, s.bobEntry, s.bobLeg, s.point); err != nil {
		return "", r.fail("alice-check", "Bob's half-finished signature is not what he said: "+err.Error())
	}
	now, err := r.cl.Now()
	if err != nil {
		return "", r.fail("alice-check", "Could not read the chain's clock")
	}
	if left := s.bobLeg.Expiry - now; left < keyswap.DevnetPolicy.RevealMargin {
		return "", r.fail("alice-check", fmt.Sprintf("Only %d s left on Bob's lock, too little to take it safely", left))
	}
	r.done("alice-check", "Right amount, his key, pays only Alice, and the signature fits it")

	if err := s.aliceClaims(r); err != nil {
		return "", err
	}
	bobSig, err := s.bobReads(r)
	if err != nil {
		return "", err
	}
	bobUnlock, err := r.call("bob-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, s.aliceEntry, bobSig) })
	if err != nil {
		return "", err
	}
	if err := r.paid("bob-paid", bobUnlock, r.bob.Address, tenQsr, types.QsrTokenStandard); err != nil {
		return "", err
	}

	// What this swap is for. Every byte the two unlocks put on the chain is
	// read back, and neither the secret nor the point it opens is among them.
	r.active("hidden", "")
	var shown []byte
	for _, id := range []types.Hash{s.aliceEntry, s.bobEntry} {
		attempts, err := r.cl.ScanAttempts(id, s.from)
		if err != nil || len(attempts) == 0 {
			return "", r.fail("hidden", "Could not read the unlocks back from the contract's chain")
		}
		for _, a := range attempts {
			shown = append(append(shown, a.Witness...), 0xff)
		}
	}
	shown = append(append(shown, s.aliceLeg.Key...), s.bobLeg.Key...)
	if bytes.Contains(shown, s.secret) || bytes.Contains(shown, s.point[1:]) {
		return "", r.fail("hidden", "The secret, or its lock, is on the chain")
	}
	r.done("hidden", "Two keys nobody has seen before and one ordinary signature by each. Nothing ties the two locks together")
	return "Alice got 1 ZNN and Bob got 10 QSR, and the secret that tied the two locks together never appeared", nil
}

// ---- mistakes the contract cannot stop -----------------------------------------

// runKeySwapOpen is the key swap with Alice's entry naming no destination. The
// contract allows that for a key lock, and the key is hers: she signs the
// entry back to herself, outright, and then claims Bob's.
func runKeySwapOpen(r *runner) (string, error) {
	s, err := keySwapLocks(r, nobody)
	if err != nil {
		return "", err
	}
	back, err := schnorr.Sign(s.aliceKey, keyswap.UnlockMessage(s.chain, s.aliceEntry, r.alice.Address))
	if err != nil {
		return "", err
	}
	aliceBack, err := r.call("alice-back", func() (types.Hash, error) { return r.cl.Unlock(r.alice, s.aliceEntry, back.Serialize()) })
	if err != nil {
		return "", err
	}
	if err := r.paid("alice-back-paid", aliceBack, r.alice.Address, tenQsr, types.QsrTokenStandard); err != nil {
		return "", err
	}
	if err := s.aliceClaims(r); err != nil {
		return "", err
	}
	bobSig, err := s.bobReads(r)
	if err != nil {
		return "", err
	}
	if _, err := r.call("bob-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, s.aliceEntry, bobSig) }); err != nil {
		return "", err
	}
	return "Alice has Bob's 1 ZNN and all of her QSR. Her lock named nobody, so her own key could take it back, and Bob locked against it without looking", nil
}

func runWrongOrder(r *runner) (string, error) {
	if err := r.need(r.bob, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	lock, err := newSecretLock()
	if err != nil {
		return "", err
	}
	r.note("agree", "Alice keeps the secret behind it to herself, as she should")
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	bobEntry, err := r.call("bob-lock", func() (types.Hash, error) {
		return r.cl.CreatePoint(r.bob, lock.point, r.alice.Address, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	from, err := r.cl.ContractHeight()
	if err != nil {
		return "", err
	}
	take, err := r.call("alice-take", func() (types.Hash, error) { return r.cl.Unlock(r.alice, bobEntry, lock.t) })
	if err != nil {
		return "", err
	}
	if err := r.paid("alice-paid", take, r.alice.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	r.note("walk", "Nothing obliges her to, and the contract has no way to know there was a deal")
	r.active("bob-read", "")
	seen, _, err := r.cl.ScanWitness(bobEntry, from)
	if err != nil || seen == nil || !opens(seen, lock.point) {
		return "", r.fail("bob-read", "Alice's secret is not on the contract's chain")
	}
	r.done("bob-read", "He has the secret, and no lock of Alice's for it to open")
	return "Alice has Bob's 1 ZNN and all of her QSR. Bob locked first without holding the secret: the order was the mistake, not the contract", nil
}

func runLateReveal(r *runner) (string, error) {
	// Bob's lock ends 130 s after Alice's starts, which leaves him a minute and
	// a half once hers is answered; hers ends 200 s after his, room for him to
	// take his back and hers once the secret is out.
	s, err := aliceLocks(r, 330)
	if err != nil {
		return "", err
	}
	if err := bobLocks(r, s, 200, ""); err != nil {
		return "", err
	}
	if err := r.waitUntil("wait", s.bobExpiry); err != nil {
		return "", err
	}
	from, err := r.cl.ContractHeight()
	if err != nil {
		return "", err
	}
	if _, err := r.call("alice-late", func() (types.Hash, error) { return r.cl.Unlock(r.alice, s.bobEntry, s.lock.t) }); err != nil {
		return "", err
	}
	r.active("bob-read", "")
	attempts, err := r.cl.ScanAttempts(s.bobEntry, from)
	if err != nil {
		return "", r.fail("bob-read", "Could not read the contract's chain")
	}
	var seen []byte
	for _, a := range attempts {
		if !a.Accepted && a.From == r.alice.Address && opens(a.Witness, s.lock.point) {
			seen = a.Witness
		}
	}
	if seen == nil {
		return "", r.fail("bob-read", "Alice's refused call is not on the contract's chain")
	}
	bobSecret, err := s.open(seen)
	if err != nil {
		return "", r.fail("bob-read", err.Error())
	}
	r.done("bob-read", "The contract said no to Alice, but her call, secret and all, is on the chain for anyone")
	reclaim, err := r.call("bob-reclaim", func() (types.Hash, error) { return r.cl.Reclaim(r.bob, s.bobEntry) })
	if err != nil {
		return "", err
	}
	if err := r.paid("bob-back", reclaim, r.bob.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	unlock, err := r.call("bob-unlock", func() (types.Hash, error) { return r.cl.Unlock(r.bob, s.aliceEntry, bobSecret) })
	if err != nil {
		return "", err
	}
	if err := r.paid("bob-paid", unlock, r.bob.Address, tenQsr, types.QsrTokenStandard); err != nil {
		return "", err
	}
	return "Alice's late call was refused and gave her secret away: Bob has his 1 ZNN back and her 10 QSR", nil
}

// identityKey is the Ed25519 encoding of the curve's neutral point. As a public
// key it makes the verification equation [S]B = R + [k]A read [S]B = R, so the
// signature R = that same point, S = 0 checks out for every message.
func identityKey() []byte {
	key := make([]byte, 32)
	key[0] = 1
	return key
}

func forgedSignature() []byte {
	sig := make([]byte, 64)
	sig[0] = 1
	return sig
}

func runWeakKey(r *runner) (string, error) {
	if err := r.need(r.alice, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	expiry, err := r.in(comfortable)
	if err != nil {
		return "", err
	}
	id, err := r.call("lock", func() (types.Hash, error) {
		return r.cl.Create(r.alice, nom.PointTypeEd25519, identityKey(), nobody, expiry, types.ZnnTokenStandard, oneZnn)
	})
	if err != nil {
		return "", err
	}
	r.note("forge", "Sixty-four bytes written down by hand: a 1, then zeros")
	take, err := r.call("take", func() (types.Hash, error) { return r.cl.Unlock(r.carol, id, forgedSignature()) })
	if err != nil {
		return "", err
	}
	if err := r.paid("carol-paid", take, r.carol.Address, oneZnn, types.ZnnTokenStandard); err != nil {
		return "", err
	}
	return "The contract took a lock that any signature opens, and Carol took the 1 ZNN. The contract leaves this check to wallets: the lab's console and CLI refuse such a key", nil
}
