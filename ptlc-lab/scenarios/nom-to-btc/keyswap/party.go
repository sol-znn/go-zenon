package keyswap

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/wallet"

	"zenon-ptlc/scenarios/nom-to-btc/nom"
)

// Chain is what a party needs of the NoM chain. Nothing in it waits: a party
// asks, and comes back later. *nom.Client is one.
type Chain interface {
	Now() (int64, error)
	ChainIdentifier() (uint64, error)
	AccountHeight(types.Address) (uint64, error)
	ContractHeight() (uint64, error)
	Create(key *wallet.KeyPair, pointType uint8, lock []byte, destination types.Address, expiry int64, token types.ZenonTokenStandard, amount *big.Int) (types.Hash, error)
	FindCreate(creator types.Address, lock []byte, since uint64) (types.Hash, bool, error)
	Unlock(key *wallet.KeyPair, id types.Hash, witness []byte) (types.Hash, error)
	Reclaim(key *wallet.KeyPair, id types.Hash) (types.Hash, error)
	Answered(call types.Hash) (done, accepted bool, err error)
	GetEntry(id types.Hash) (*definition.PtlcInfo, error)
	ScanAttempts(id types.Hash, from uint64) ([]nom.Attempt, error)
}

// Link carries messages to the other party and holds the ones that came from
// it. Recv returns the message of one kind if it has arrived and nil if not,
// and returns it again if asked again: a party that read a message and could
// not save what it learned has lost nothing. A party sends each message again
// whenever it starts, so a link may lose what was in flight when either side
// stopped, and may be handed the same message twice.
type Link interface {
	Send(Message) error
	Recv(kind string) *Message
}

// The stages of a party, in the order it passes through them. A stage is saved
// before the party acts on it, so the stage on disk is always one whose
// preconditions are on disk too.
const (
	StageOffered    = "offered"     // Alice: secret, key and offer made
	StageAccepted   = "accepted"    // both keys and addresses known
	StageChecking   = "checking"    // the other side's entry and pre-signature received, not yet verified
	StageLocking    = "locking"     // about to Create, or has and does not know it yet
	StageLocked     = "locked"      // own Create sent, id known
	StagePresigned  = "presigned"   // own entry accepted, pre-signature made
	StageRevealing  = "revealing"   // Alice: about to claim, or has
	StageSecret     = "secret"      // Bob: t read off the chain
	StageClaiming   = "claiming"    // unlock sent
	StageSweeping   = "sweeping"    // Bob: paid, and seeing to his own entry
	StageWaitingOut = "waiting-out" // nothing more to gain: waiting for own expiry
	StageReclaiming = "reclaiming"  // reclaim sent
	StageDone       = "done"
)

// How a swap ended, for the party whose state says so.
const (
	OutcomeSwapped  = "swapped"        // paid the other side and was paid
	OutcomeRefunded = "refunded"       // own funds back, nothing gained or lost
	OutcomeNothing  = "nothing-locked" // ended before this party locked anything
	OutcomeBoth     = "took-both"      // Bob: Alice revealed too late; he has her funds and his own
	OutcomeLost     = "lost"           // paid, or revealed, and was not paid
)

type Status int

const (
	Progress Status = iota // did something; call Step again
	Waiting                // nothing to do until the chain or the other party moves
	Done
)

// ErrCrash is what Step returns when the test hook Crash fires: the party
// stopped dead between an action and writing it down.
var ErrCrash = errors.New("keyswap: stopped before saving")

// Deal is what Alice proposes: what she gives and gets, how long her entry
// lasts from now, and how much sooner Bob's ends.
type Deal struct {
	GiveToken  types.ZenonTokenStandard
	GiveAmount *big.Int
	GetToken   types.ZenonTokenStandard
	GetAmount  *big.Int
	AliceFor   int64
	Gap        int64
}

// Party is one side of one swap. It is driven by calling Step until it says
// Done; everything it knows between calls is in its Store, so a Party made
// again over the same Store carries on where the last one stopped.
//
// Wallet signs the account blocks. It is the one part a wallet extension will
// take over: the swap's own keys never sign a block, and the account key never
// signs anything but one.
type Party struct {
	Wallet *wallet.KeyPair
	Chain  Chain
	Link   Link
	Policy Policy
	Log    func(format string, args ...interface{})

	// Crash, when set, is asked at each point where an action has been taken
	// and not yet saved. Returning true stops the party there.
	Crash func(point string) bool
	// RevealLate makes Alice claim however little time is left. It exists to
	// show what the margin is for and has no other use.
	RevealLate bool

	deal   Deal
	want   Wanted
	store  *Store
	st     *State
	sent   map[string]bool
	cursor uint64
}

func load(store *Store, role string, account types.Address) (*State, error) {
	st, err := store.Load()
	if err != nil {
		return nil, err
	}
	if st == nil {
		return &State{Role: role, Account: account}, nil
	}
	if st.Role != role || st.Account != account {
		return nil, fmt.Errorf("the state in this directory is %s's swap as %s, not %s's as %s", st.Account, st.Role, account, role)
	}
	return st, nil
}

// NewAlice starts Alice's side of a swap, or resumes the one in store.
func NewAlice(store *Store, key *wallet.KeyPair, chain Chain, link Link, deal Deal) (*Party, error) {
	st, err := load(store, "alice", key.Address)
	if err != nil {
		return nil, err
	}
	return &Party{Wallet: key, Chain: chain, Link: link, Policy: DevnetPolicy, Log: func(string, ...interface{}) {}, deal: deal, store: store, st: st, sent: map[string]bool{}}, nil
}

// NewBob starts Bob's side, or resumes it.
func NewBob(store *Store, key *wallet.KeyPair, chain Chain, link Link, want Wanted) (*Party, error) {
	st, err := load(store, "bob", key.Address)
	if err != nil {
		return nil, err
	}
	return &Party{Wallet: key, Chain: chain, Link: link, Policy: DevnetPolicy, Log: func(string, ...interface{}) {}, want: want, store: store, st: st, sent: map[string]bool{}}, nil
}

// State is the party's state as last saved.
func (p *Party) State() State { return *p.st }

// Step does the next thing the party can do without waiting, and saves it.
// An error leaves the state as it was: it is the chain or the disk failing,
// and the same step can be tried again.
func (p *Party) Step() (Status, error) {
	if p.st.Stage == StageDone {
		return Done, nil
	}
	if p.st.Role == "alice" {
		return p.stepAlice()
	}
	return p.stepBob()
}

func (p *Party) to(stage string) (Status, error) {
	prev := p.st.Stage
	p.st.Stage = stage
	if err := p.store.Save(p.st); err != nil {
		p.st.Stage = prev
		return Waiting, err
	}
	return Progress, nil
}

func (p *Party) finish(outcome, reason string) (Status, error) {
	p.st.Outcome, p.st.Reason = outcome, reason
	p.Log("done: %s%s", outcome, because(reason))
	return p.to(StageDone)
}

func because(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func (p *Party) crashed(point string) bool {
	return p.Crash != nil && p.Crash(point)
}

// sendOnce sends a message the first time a Party reaches the stage that owns
// it. A Party made again after a stop sends it again, which is how a message
// lost in flight is recovered.
func (p *Party) sendOnce(m Message) error {
	if p.sent[m.Kind] {
		return nil
	}
	if err := p.Link.Send(m); err != nil {
		return err
	}
	p.sent[m.Kind] = true
	return nil
}

func (p *Party) key() *btcec.PrivateKey {
	key, _ := btcec.PrivKeyFromBytes(p.st.Key)
	return key
}

// lock makes the party's own entry, or finds the one it made before it
// stopped. The stage was saved as StageLocking before this ran, with the
// account's height at that moment, so a Create that was published and not
// written down is above that height on the party's own chain.
func (p *Party) lock(leg Leg) (Status, error) {
	id, found, err := p.Chain.FindCreate(p.Wallet.Address, leg.Key, p.st.Since)
	if err != nil {
		return Waiting, err
	}
	if !found {
		if id, err = p.Chain.Create(p.Wallet, definition.PointTypeBIP340, leg.Key, leg.Destination, leg.Expiry, leg.Token, leg.Amount); err != nil {
			return Waiting, err
		}
		p.Log("locks %s of %s for %s until %d: entry %s", leg.Amount, leg.Token, leg.Destination, leg.Expiry, id)
		if p.crashed("created") {
			return Waiting, ErrCrash
		}
	} else {
		p.Log("found the entry it made before it stopped: %s", id)
	}
	p.st.Mine.ID = id
	return p.to(StageLocked)
}

// presign waits for the party's own Create to be answered and makes the
// pre-signature the other side will complete.
func (p *Party) presign(leg Leg) (Status, error) {
	done, accepted, err := p.Chain.Answered(p.st.Mine.ID)
	if err != nil || !done {
		return Waiting, err
	}
	if !accepted {
		// A refused Create is refunded by the contract in the same answer.
		return p.finish(OutcomeNothing, "the contract refused the lock and returned the funds")
	}
	chain, err := p.Chain.ChainIdentifier()
	if err != nil {
		return Waiting, err
	}
	presig, err := PreSign(p.key(), chain, p.st.Mine.ID, leg, p.st.Terms.T)
	if err != nil {
		return Waiting, err
	}
	p.st.Mine.PreSig = presig
	return p.to(StagePresigned)
}

// accepted reports whether anyone's unlock of id was accepted since From.
func (p *Party) accepted(id types.Hash) (bool, error) {
	attempts, err := p.Chain.ScanAttempts(id, p.st.From)
	if err != nil {
		return false, err
	}
	for _, a := range attempts {
		if a.Accepted {
			return true, nil
		}
	}
	return false, nil
}

// ---- Alice -------------------------------------------------------------------

func (p *Party) stepAlice() (Status, error) {
	st, pol := p.st, p.Policy
	now, err := p.Chain.Now()
	if err != nil {
		return Waiting, err
	}
	switch st.Stage {
	case "":
		if p.deal.Gap < pol.MinGap() || p.deal.AliceFor-p.deal.Gap < pol.aliceLockBy() {
			return Waiting, fmt.Errorf("the deal's times are too short: a gap of %d s (at least %d) and %d s for Bob's entry (at least %d)",
				p.deal.Gap, pol.MinGap(), p.deal.AliceFor-p.deal.Gap, pol.aliceLockBy())
		}
		t, point, err := NewSecret()
		if err != nil {
			return Waiting, err
		}
		key, err := NewKey()
		if err != nil {
			return Waiting, err
		}
		st.Secret, st.Key = t, key.Serialize()
		st.Offer = &Offer{
			T: point, AliceKey: XOnly(key), Alice: p.Wallet.Address,
			AliceToken: p.deal.GiveToken, AliceAmount: p.deal.GiveAmount, AliceExpiry: now + p.deal.AliceFor,
			BobToken: p.deal.GetToken, BobAmount: p.deal.GetAmount, BobExpiry: now + p.deal.AliceFor - p.deal.Gap,
		}
		return p.to(StageOffered)

	case StageOffered:
		if err := p.sendOnce(Message{Kind: KindOffer, Offer: st.Offer}); err != nil {
			return Waiting, err
		}
		m := p.Link.Recv(KindAccept)
		if m == nil || m.Accept == nil {
			if st.Offer.BobExpiry-now < pol.aliceLockBy() {
				return p.finish(OutcomeNothing, "Bob did not accept in time")
			}
			return Waiting, nil
		}
		if err := CheckAccept(m.Accept); err != nil {
			return p.finish(OutcomeNothing, "refused Bob's answer: "+err.Error())
		}
		terms := TermsOf(st.Offer, m.Accept)
		st.Accept, st.Terms = m.Accept, &terms
		return p.to(StageAccepted)

	case StageAccepted:
		if st.Terms.BobLeg.Expiry-now < pol.aliceLockBy() {
			return p.finish(OutcomeNothing, "too little time left to lock safely")
		}
		if st.Since, err = p.Chain.AccountHeight(p.Wallet.Address); err != nil {
			return Waiting, err
		}
		if st.From, err = p.Chain.ContractHeight(); err != nil {
			return Waiting, err
		}
		return p.to(StageLocking)

	case StageLocking:
		return p.lock(st.Terms.AliceLeg)

	case StageLocked:
		return p.presign(st.Terms.AliceLeg)

	case StagePresigned:
		if err := p.sendOnce(Message{Kind: KindAliceLocked, Locked: &st.Mine}); err != nil {
			return Waiting, err
		}
		m := p.Link.Recv(KindBobLocked)
		if m == nil || m.Locked == nil {
			if st.Terms.BobLeg.Expiry-now < pol.RevealMargin {
				return p.waitOut("Bob did not lock in time")
			}
			return Waiting, nil
		}
		st.Theirs = *m.Locked
		return p.to(StageChecking)

	case StageChecking:
		// Alice reads Bob's entry and checks his pre-signature before her
		// secret leaves her hands. Anything wrong and she keeps it, and waits
		// for her own entry to expire.
		info, err := p.Chain.GetEntry(st.Theirs.ID)
		if err != nil {
			return Waiting, err
		}
		if info == nil {
			if st.Terms.BobLeg.Expiry-now < pol.RevealMargin {
				return p.waitOut("Bob's entry never appeared")
			}
			return Waiting, nil
		}
		if err := CheckEntry(info, st.Terms.BobLeg); err != nil {
			return p.waitOut("refused Bob's entry: " + err.Error())
		}
		chain, err := p.Chain.ChainIdentifier()
		if err != nil {
			return Waiting, err
		}
		if err := VerifyPreSig(st.Theirs.PreSig, chain, st.Theirs.ID, st.Terms.BobLeg, st.Terms.T); err != nil {
			return p.waitOut("refused Bob's pre-signature: " + err.Error())
		}
		return p.to(StageRevealing)

	case StageRevealing:
		// From here on her signature may already be on the chain: the stage
		// was saved before the claim was sent. A claim that was answered shows
		// on the contract's chain, and settles the matter either way.
		attempts, err := p.Chain.ScanAttempts(st.Theirs.ID, st.From)
		if err != nil {
			return Waiting, err
		}
		for _, a := range attempts {
			if a.From == p.Wallet.Address {
				if a.Accepted {
					return p.finish(OutcomeSwapped, "")
				}
				return p.finish(OutcomeLost, "the claim was answered after Bob's expiry, and it made the secret public")
			}
		}
		if left := st.Terms.BobLeg.Expiry - now; left < pol.RevealMargin && !p.RevealLate {
			return p.waitOut(fmt.Sprintf("%d s left on Bob's entry, under the %d s a claim needs: the secret stays unrevealed", left, pol.RevealMargin))
		}
		chain, err := p.Chain.ChainIdentifier()
		if err != nil {
			return Waiting, err
		}
		sig, err := Complete(st.Theirs.PreSig, st.Secret, chain, st.Theirs.ID, st.Terms.BobLeg)
		if err != nil {
			return p.waitOut("could not complete Bob's pre-signature: " + err.Error())
		}
		call, err := p.Chain.Unlock(p.Wallet, st.Theirs.ID, sig)
		if err != nil {
			return Waiting, err
		}
		p.Log("claims Bob's entry with the completed signature: %s", call)
		if p.crashed("claimed") {
			return Waiting, ErrCrash
		}
		st.Call = call
		return p.to(StageClaiming)

	case StageClaiming:
		done, accepted, err := p.Chain.Answered(st.Call)
		if err != nil || !done {
			return Waiting, err
		}
		if !accepted {
			// Refused because an earlier claim of hers got there first (she
			// stopped and sent it twice), or because it was too late.
			if was, err := p.accepted(st.Theirs.ID); err != nil {
				return Waiting, err
			} else if !was {
				return p.finish(OutcomeLost, "the claim was answered after Bob's expiry, and it made the secret public")
			}
		}
		return p.finish(OutcomeSwapped, "")

	case StageWaitingOut:
		if now < st.Terms.AliceLeg.Expiry {
			return Waiting, nil
		}
		return p.reclaim()

	case StageReclaiming:
		return p.reclaimed()
	}
	return Waiting, fmt.Errorf("alice has no stage %q", st.Stage)
}

// waitOut is the end of a swap that will not complete for a party that has
// locked: nothing is gained by acting, and the funds come back at the expiry.
func (p *Party) waitOut(reason string) (Status, error) {
	p.st.Reason = reason
	p.Log("gives up on the swap and waits for its own entry to expire%s", because(reason))
	return p.to(StageWaitingOut)
}

func (p *Party) reclaim() (Status, error) {
	call, err := p.Chain.Reclaim(p.Wallet, p.st.Mine.ID)
	if err != nil {
		return Waiting, err
	}
	p.Log("takes its own entry back: %s", call)
	if p.crashed("reclaimed") {
		return Waiting, ErrCrash
	}
	p.st.Call = call
	return p.to(StageReclaiming)
}

// reclaimed reads the answer to a reclaim. A refused one with the entry gone
// is a second reclaim sent after a stop, behind the first.
func (p *Party) reclaimed() (Status, error) {
	done, accepted, err := p.Chain.Answered(p.st.Call)
	if err != nil || !done {
		return Waiting, err
	}
	if accepted {
		return p.finish(OutcomeRefunded, p.st.Reason)
	}
	info, err := p.Chain.GetEntry(p.st.Mine.ID)
	if err != nil {
		return Waiting, err
	}
	if info != nil {
		// Answered a moment before the expiry on the chain's clock.
		return p.to(StageWaitingOut)
	}
	if taken, err := p.accepted(p.st.Mine.ID); err != nil {
		return Waiting, err
	} else if taken {
		return p.finish(OutcomeLost, "the other side opened the entry")
	}
	return p.finish(OutcomeRefunded, p.st.Reason)
}

// ---- Bob ---------------------------------------------------------------------

func (p *Party) stepBob() (Status, error) {
	st, pol := p.st, p.Policy
	now, err := p.Chain.Now()
	if err != nil {
		return Waiting, err
	}
	switch st.Stage {
	case "":
		m := p.Link.Recv(KindOffer)
		if m == nil || m.Offer == nil {
			return Waiting, nil
		}
		if err := CheckOffer(m.Offer, p.want, now, pol); err != nil {
			return p.finish(OutcomeNothing, "refused the offer: "+err.Error())
		}
		key, err := NewKey()
		if err != nil {
			return Waiting, err
		}
		st.Key, st.Offer = key.Serialize(), m.Offer
		st.Accept = &Accept{BobKey: XOnly(key), Bob: p.Wallet.Address}
		terms := TermsOf(st.Offer, st.Accept)
		st.Terms = &terms
		return p.to(StageAccepted)

	case StageAccepted:
		if err := p.sendOnce(Message{Kind: KindAccept, Accept: st.Accept}); err != nil {
			return Waiting, err
		}
		m := p.Link.Recv(KindAliceLocked)
		if m == nil || m.Locked == nil {
			if st.Terms.BobLeg.Expiry-now < pol.bobLockBy() {
				return p.finish(OutcomeNothing, "Alice did not lock in time")
			}
			return Waiting, nil
		}
		// Saved before it is checked, and so before Bob locks: this is the
		// pre-signature he will need when t appears.
		st.Theirs = *m.Locked
		return p.to(StageChecking)

	case StageChecking:
		if left := st.Terms.BobLeg.Expiry - now; left < pol.bobLockBy() {
			return p.finish(OutcomeNothing, fmt.Sprintf("%d s left for an entry that needs %d: too late to lock safely", left, pol.bobLockBy()))
		}
		info, err := p.Chain.GetEntry(st.Theirs.ID)
		if err != nil || info == nil {
			return Waiting, err
		}
		if err := CheckEntry(info, st.Terms.AliceLeg); err != nil {
			return p.finish(OutcomeNothing, "refused Alice's entry: "+err.Error())
		}
		chain, err := p.Chain.ChainIdentifier()
		if err != nil {
			return Waiting, err
		}
		if err := VerifyPreSig(st.Theirs.PreSig, chain, st.Theirs.ID, st.Terms.AliceLeg, st.Terms.T); err != nil {
			return p.finish(OutcomeNothing, "refused Alice's pre-signature: "+err.Error())
		}
		if st.Since, err = p.Chain.AccountHeight(p.Wallet.Address); err != nil {
			return Waiting, err
		}
		if st.From, err = p.Chain.ContractHeight(); err != nil {
			return Waiting, err
		}
		return p.to(StageLocking)

	case StageLocking:
		return p.lock(st.Terms.BobLeg)

	case StageLocked:
		return p.presign(st.Terms.BobLeg)

	case StagePresigned:
		if err := p.sendOnce(Message{Kind: KindBobLocked, Locked: &st.Mine}); err != nil {
			return Waiting, err
		}
		if found, err := p.watch(); err != nil || found {
			return Progress, err
		}
		if now >= st.Terms.BobLeg.Expiry {
			return p.reclaim()
		}
		return Waiting, nil

	case StageReclaiming:
		// Alice may still claim while the reclaim is on its way, and a claim
		// that loses to it is refused with her signature on the chain.
		if found, err := p.watch(); err != nil || found {
			return Progress, err
		}
		done, accepted, err := p.Chain.Answered(st.Call)
		if err != nil || !done {
			return Waiting, err
		}
		if accepted {
			return p.finish(OutcomeRefunded, "Alice never claimed")
		}
		info, err := p.Chain.GetEntry(st.Mine.ID)
		if err != nil {
			return Waiting, err
		}
		if info != nil {
			return p.to(StagePresigned)
		}
		// The entry is gone and not by this reclaim: Alice's claim got in
		// first, and the next look at the contract's chain finds it.
		return Waiting, nil

	case StageSecret:
		info, err := p.Chain.GetEntry(st.Theirs.ID)
		if err != nil {
			return Waiting, err
		}
		if info == nil {
			if mine, err := p.accepted(st.Theirs.ID); err != nil {
				return Waiting, err
			} else if mine {
				return p.to(StageSweeping)
			}
			return p.finish(OutcomeLost, "Alice's entry expired and she took it back before Bob's claim was answered")
		}
		chain, err := p.Chain.ChainIdentifier()
		if err != nil {
			return Waiting, err
		}
		sig, err := Complete(st.Theirs.PreSig, st.Secret, chain, st.Theirs.ID, st.Terms.AliceLeg)
		if err != nil {
			return Waiting, err
		}
		call, err := p.Chain.Unlock(p.Wallet, st.Theirs.ID, sig)
		if err != nil {
			return Waiting, err
		}
		p.Log("claims Alice's entry with the completed signature: %s", call)
		if p.crashed("claimed") {
			return Waiting, ErrCrash
		}
		st.Call = call
		return p.to(StageClaiming)

	case StageClaiming:
		done, accepted, err := p.Chain.Answered(st.Call)
		if err != nil || !done {
			return Waiting, err
		}
		if accepted {
			return p.to(StageSweeping)
		}
		// Refused. With the entry gone it was behind an earlier claim of his
		// own, sent before a stop, or behind Alice's reclaim: StageSecret
		// tells the two apart. With the entry still there it was too late.
		info, err := p.Chain.GetEntry(st.Theirs.ID)
		if err != nil {
			return Waiting, err
		}
		if info != nil {
			return p.finish(OutcomeLost, "the contract refused the claim: it was answered after Alice's expiry")
		}
		return p.to(StageSecret)

	case StageSweeping:
		// Bob has been paid. His own entry is Alice's if her claim was
		// accepted, and still his to take back if it was refused.
		info, err := p.Chain.GetEntry(st.Mine.ID)
		if err != nil {
			return Waiting, err
		}
		if info == nil {
			paidAlice, err := p.accepted(st.Mine.ID)
			if err != nil {
				return Waiting, err
			}
			if paidAlice {
				return p.finish(OutcomeSwapped, "")
			}
			return p.finish(OutcomeBoth, "Alice's claim was refused as too late, and it made the secret public")
		}
		if now < st.Terms.BobLeg.Expiry {
			return Waiting, nil
		}
		// The reclaim below, or the claim before it, may still be on its way.
		// A party that stopped before saving its claim has no call written
		// down, and nothing to wait for.
		if st.Call != (types.Hash{}) {
			done, _, err := p.Chain.Answered(st.Call)
			if err != nil || !done {
				return Waiting, err
			}
		}
		call, err := p.Chain.Reclaim(p.Wallet, st.Mine.ID)
		if err != nil {
			return Waiting, err
		}
		p.Log("takes its own entry back as well: %s", call)
		st.Call = call
		return p.to(StageSweeping)
	}
	return Waiting, fmt.Errorf("bob has no stage %q", st.Stage)
}

// watch reads every unlock of Bob's entry the contract has answered, accepted
// or not, and tries each as the completion of the pre-signature he made. The
// first that yields the scalar behind T is saved as the secret.
func (p *Party) watch() (bool, error) {
	st := p.st
	from := st.From
	if p.cursor > from {
		from = p.cursor
	}
	head, err := p.Chain.ContractHeight()
	if err != nil {
		return false, err
	}
	attempts, err := p.Chain.ScanAttempts(st.Mine.ID, from)
	if err != nil {
		return false, err
	}
	for _, a := range attempts {
		t, err := Extract(st.Mine.PreSig, a.Witness, st.Terms.T)
		if err != nil {
			continue
		}
		p.Log("read t from %s's unlock at contract height %d (accepted: %v)", a.From, a.Height, a.Accepted)
		st.Secret = t
		_, err = p.to(StageSecret)
		return err == nil, err
	}
	p.cursor = head
	return false, nil
}

// ---- an in-memory link ---------------------------------------------------------

// Pipe joins two parties in one process. Messages cross as JSON, so that what
// works over a Pipe works over any transport that carries bytes.
type Pipe struct {
	mu    sync.Mutex
	boxes map[string][][]byte
}

func NewPipe() *Pipe { return &Pipe{boxes: map[string][][]byte{}} }

// End is one party's end: role is "alice" or "bob".
func (p *Pipe) End(role string) Link { return &pipeEnd{pipe: p, role: role} }

type pipeEnd struct {
	pipe *Pipe
	role string
}

func (e *pipeEnd) other() string {
	if e.role == "alice" {
		return "bob"
	}
	return "alice"
}

func (e *pipeEnd) Send(m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	e.pipe.mu.Lock()
	defer e.pipe.mu.Unlock()
	e.pipe.boxes[e.other()] = append(e.pipe.boxes[e.other()], body)
	return nil
}

// Recv returns the first message of a kind to have arrived, and leaves it.
func (e *pipeEnd) Recv(kind string) *Message {
	e.pipe.mu.Lock()
	defer e.pipe.mu.Unlock()
	for _, body := range e.pipe.boxes[e.role] {
		var m Message
		if err := json.Unmarshal(body, &m); err == nil && m.Kind == kind {
			return &m
		}
	}
	return nil
}
