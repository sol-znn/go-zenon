// Package keyswap is one party's side of the key swap of zenon-ptlc/SPEC.md §5:
// two NoM entries on BIP340 key locks, tied by a single-signer adaptor
// signature, with no secret and no point on the chain.
//
// Alice holds t. Each party locks to a key made for this swap and names the
// other as the only address its entry may pay. Each hands the other a
// pre-signature on T = t·G over its own entry's unlock message. Alice
// completes Bob's with t and claims; her signature on the chain gives Bob t,
// and he completes hers.
//
// This file is the protocol with no chain and no disk in it: what crosses
// between the parties, and every check one makes of the other. party.go drives
// it against a chain and keeps what must survive a crash.
package keyswap

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	"zenon-ptlc/scenarios/nom-to-btc/adaptor"
)

// Policy is the time a party insists on. The figures are the chain's, not the
// protocol's: a call to the contract is answered two momentums after it is
// sent, and judged against the clock then.
type Policy struct {
	// RevealMargin is how long before Bob's expiry Alice stops being willing
	// to claim. A claim answered after the expiry is refused with the completed
	// signature on the chain, which gives Bob t and both entries (GAPS G-18).
	RevealMargin int64
	// Answer is how long a party allows for one of its calls to be answered.
	Answer int64
}

// DevnetPolicy suits the lab's devnet, where a momentum takes up to 15 s on its
// host. A client for another chain measures its own.
var DevnetPolicy = Policy{RevealMargin: 60, Answer: 45}

// MinGap is the least Alice's entry must outlast Bob's. Alice can publish her
// signature right up to Bob's expiry; Bob then needs one call answered.
func (p Policy) MinGap() int64 { return 2 * p.Answer }

// bobLockBy is the least time Bob's entry must still have when Bob locks: his
// Create answered, then Alice's claim sent RevealMargin before the end.
func (p Policy) bobLockBy() int64 { return p.Answer + p.RevealMargin }

// aliceLockBy is the same when Alice locks, one answered call earlier.
func (p Policy) aliceLockBy() int64 { return 2*p.Answer + p.RevealMargin }

// Leg is one entry as the two agreed it: who is paid what, on which key, until
// when. The creator is the other party.
type Leg struct {
	Key         []byte                   `json:"key"` // 32-byte x-only key of the entry's creator
	Destination types.Address            `json:"destination"`
	Token       types.ZenonTokenStandard `json:"token"`
	Amount      *big.Int                 `json:"amount"`
	Expiry      int64                    `json:"expiry"`
}

// Terms is the whole swap. AliceLeg is the entry Alice makes, paying Bob.
type Terms struct {
	T        []byte `json:"t"` // 33-byte compressed point; Alice holds the scalar
	AliceLeg Leg    `json:"aliceLeg"`
	BobLeg   Leg    `json:"bobLeg"`
}

// What crosses between the parties. Each is small and self-contained, so that
// any transport that can carry JSON can carry the swap.
type (
	// Offer is Alice's proposal. It fixes everything but Bob's key and address.
	Offer struct {
		T           []byte                   `json:"t"`
		AliceKey    []byte                   `json:"aliceKey"`
		Alice       types.Address            `json:"alice"`
		AliceToken  types.ZenonTokenStandard `json:"aliceToken"`
		AliceAmount *big.Int                 `json:"aliceAmount"`
		AliceExpiry int64                    `json:"aliceExpiry"`
		BobToken    types.ZenonTokenStandard `json:"bobToken"`
		BobAmount   *big.Int                 `json:"bobAmount"`
		BobExpiry   int64                    `json:"bobExpiry"`
	}
	// Accept is Bob's answer: the key his entry will be locked to and the
	// address Alice's entry must name.
	Accept struct {
		BobKey []byte        `json:"bobKey"`
		Bob    types.Address `json:"bob"`
	}
	// Locked says "my entry is on the chain, and here is the pre-signature
	// that opens it once t is known".
	Locked struct {
		ID     types.Hash `json:"id"`
		PreSig []byte     `json:"preSig"`
	}
	Message struct {
		Kind   string  `json:"kind"`
		Offer  *Offer  `json:"offer,omitempty"`
		Accept *Accept `json:"accept,omitempty"`
		Locked *Locked `json:"locked,omitempty"`
	}
)

const (
	KindOffer       = "offer"
	KindAccept      = "accept"
	KindAliceLocked = "alice-locked"
	KindBobLocked   = "bob-locked"
)

// NewKey makes a key for one swap. It is never an account key and is never
// used for a second swap: the x-only public key goes on the chain as the lock,
// and a key seen twice links two swaps.
func NewKey() (*btcec.PrivateKey, error) { return btcec.NewPrivateKey() }

// NewSecret makes t and T.
func NewSecret() (t, point []byte, err error) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, nil, err
	}
	return key.Serialize(), key.PubKey().SerializeCompressed(), nil
}

func XOnly(key *btcec.PrivateKey) []byte { return schnorr.SerializePubKey(key.PubKey()) }

func validKey(x []byte) error {
	if len(x) != 32 {
		return errors.New("not 32 bytes")
	}
	_, err := schnorr.ParsePubKey(x)
	return err
}

func validPoint(p []byte) error {
	if len(p) != 33 {
		return errors.New("not a compressed point")
	}
	_, err := btcec.ParsePubKey(p)
	return err
}

func payable(a types.Address) error {
	if a.IsZero() {
		return errors.New("no address")
	}
	if types.IsEmbeddedAddress(a) {
		return errors.New("a contract's address")
	}
	return nil
}

// Wanted is what Bob agreed to before any message arrived: the trade itself.
// An offer that names other amounts is not the swap he is here for.
type Wanted struct {
	GiveToken  types.ZenonTokenStandard
	GiveAmount *big.Int
	GetToken   types.ZenonTokenStandard
	GetAmount  *big.Int
}

// CheckOffer is Bob's test of Alice's proposal, before he gives her anything.
// now is the chain's clock as Bob reads it.
func CheckOffer(o *Offer, want Wanted, now int64, pol Policy) error {
	if err := validPoint(o.T); err != nil {
		return fmt.Errorf("T: %v", err)
	}
	if err := validKey(o.AliceKey); err != nil {
		return fmt.Errorf("Alice's key: %v", err)
	}
	if err := payable(o.Alice); err != nil {
		return fmt.Errorf("Alice's address: %v", err)
	}
	if o.AliceAmount == nil || o.BobAmount == nil || o.AliceAmount.Sign() <= 0 || o.BobAmount.Sign() <= 0 {
		return errors.New("an amount is missing")
	}
	if o.AliceToken != want.GetToken || o.AliceAmount.Cmp(want.GetAmount) != 0 {
		return fmt.Errorf("Alice offers %s of %s; the trade was %s of %s", o.AliceAmount, o.AliceToken, want.GetAmount, want.GetToken)
	}
	if o.BobToken != want.GiveToken || o.BobAmount.Cmp(want.GiveAmount) != 0 {
		return fmt.Errorf("Alice asks %s of %s; the trade was %s of %s", o.BobAmount, o.BobToken, want.GiveAmount, want.GiveToken)
	}
	// Alice holds the secret, so her entry has to be the one that lasts: when
	// her claim makes t public, hers must still be open to Bob.
	if gap := o.AliceExpiry - o.BobExpiry; gap < pol.MinGap() {
		return fmt.Errorf("Alice's entry outlasts Bob's by %d s; he needs %d to claim after her", gap, pol.MinGap())
	}
	if left := o.BobExpiry - now; left < pol.aliceLockBy() {
		return fmt.Errorf("Bob's entry would end in %d s; two locks and a claim need %d", left, pol.aliceLockBy())
	}
	return nil
}

// CheckAccept is Alice's test of Bob's answer.
func CheckAccept(a *Accept) error {
	if err := validKey(a.BobKey); err != nil {
		return fmt.Errorf("Bob's key: %v", err)
	}
	if err := payable(a.Bob); err != nil {
		return fmt.Errorf("Bob's address: %v", err)
	}
	return nil
}

// TermsOf joins an offer and its acceptance.
func TermsOf(o *Offer, a *Accept) Terms {
	return Terms{
		T:        o.T,
		AliceLeg: Leg{Key: o.AliceKey, Destination: a.Bob, Token: o.AliceToken, Amount: o.AliceAmount, Expiry: o.AliceExpiry},
		BobLeg:   Leg{Key: a.BobKey, Destination: o.Alice, Token: o.BobToken, Amount: o.BobAmount, Expiry: o.BobExpiry},
	}
}

// CheckEntry holds an entry read from the chain to the leg that was agreed.
// Every field matters, and the destination most: a key lock that names nobody
// is one its key's owner can sign back to themselves at any time, which undoes
// the swap for whoever locked against it.
func CheckEntry(info *definition.PtlcInfo, leg Leg) error {
	switch {
	case info == nil:
		return errors.New("the entry is not on the chain")
	case info.PointType != definition.PointTypeBIP340:
		return fmt.Errorf("the lock is of type %d, not a BIP340 key", info.PointType)
	case !bytes.Equal(info.PointLock, leg.Key):
		return errors.New("the entry is locked to another key")
	case info.Destination != leg.Destination:
		if info.Destination.IsZero() {
			return errors.New("the entry names no destination, so its key's owner can take it back at will")
		}
		return fmt.Errorf("the entry pays %s", info.Destination)
	case info.TokenStandard != leg.Token || info.Amount == nil || info.Amount.Cmp(leg.Amount) != 0:
		return fmt.Errorf("the entry holds %s of %s", info.Amount, info.TokenStandard)
	case info.ExpirationTime != leg.Expiry:
		return fmt.Errorf("the entry expires at %d, not %d", info.ExpirationTime, leg.Expiry)
	}
	return nil
}

// UnlockMessage is what a key lock's signature signs: the chain, the contract,
// the lock type, the entry and the address paid.
func UnlockMessage(chain uint64, id types.Hash, destination types.Address) []byte {
	return definition.GetPtlcUnlockMessage(chain, definition.PointTypeBIP340, id, destination)
}

// PreSign makes the pre-signature for the entry id that key locks and that
// pays leg.Destination. The nonce comes from the key, the message, T and fresh
// randomness (adaptor.Sign), so signing again never reuses a nonce against
// another challenge; a party still keeps the one pre-signature it sent and
// sends that again, so that the other side only ever holds one.
func PreSign(key *btcec.PrivateKey, chain uint64, id types.Hash, leg Leg, T []byte) ([]byte, error) {
	if !bytes.Equal(XOnly(key), leg.Key) {
		return nil, errors.New("this is not the key the entry is locked to")
	}
	var aux [32]byte
	if _, err := rand.Read(aux[:]); err != nil {
		return nil, err
	}
	return adaptor.Sign(key, UnlockMessage(chain, id, leg.Destination), T, aux)
}

// VerifyPreSig is the receiver's check: the pre-signature is by the lock's key,
// over this entry paying this address, and t is exactly what it lacks.
func VerifyPreSig(presig []byte, chain uint64, id types.Hash, leg Leg, T []byte) error {
	if !adaptor.Verify(leg.Key, UnlockMessage(chain, id, leg.Destination), T, presig) {
		return errors.New("the pre-signature does not verify for this entry, this address and this T")
	}
	return nil
}

// Complete adds t to a pre-signature and checks the result the way the
// contract will, so that a signature that would be refused is never published.
func Complete(presig, t []byte, chain uint64, id types.Hash, leg Leg) ([]byte, error) {
	sig, err := adaptor.Complete(presig, t)
	if err != nil {
		return nil, err
	}
	parsed, err := schnorr.ParseSignature(sig)
	if err != nil {
		return nil, err
	}
	key, err := schnorr.ParsePubKey(leg.Key)
	if err != nil {
		return nil, err
	}
	if !parsed.Verify(UnlockMessage(chain, id, leg.Destination), key) {
		return nil, errors.New("the completed signature does not verify: the secret is not the one behind T")
	}
	return sig, nil
}

// Extract reads t out of a witness seen on the chain, given the pre-signature
// it was completed from. A witness that was not, or a call with any other 64
// bytes in it, yields a scalar that is not behind T and is reported as such.
// The contract's answer does not matter: a refused call's witness is as good.
func Extract(presig, witness, T []byte) ([]byte, error) {
	if len(witness) != 64 || len(presig) != adaptor.PreSigSize {
		return nil, errors.New("not a signature")
	}
	if !bytes.Equal(witness[:32], presig[1:33]) {
		return nil, errors.New("the signature does not use the pre-signature's nonce")
	}
	t, err := adaptor.Extract(presig, witness)
	if err != nil {
		return nil, err
	}
	point, err := adaptor.PointOf(t)
	if err != nil || !bytes.Equal(point, T) {
		return nil, errors.New("what the signature yields is not the scalar behind T")
	}
	return t, nil
}
