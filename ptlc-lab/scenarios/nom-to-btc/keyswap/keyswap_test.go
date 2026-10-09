package keyswap

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	znom "github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	rpcserver "github.com/zenon-network/go-zenon/rpc/server"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/wallet"
	"github.com/zenon-network/go-zenon/zenon/mock"

	"zenon-ptlc/scenarios/nom-to-btc/nom"
)

// These tests run both parties against go-zenon's mock chain: the contract is
// the real one, the node's RPC is the real one served in process, and the
// client is the one the devnet runs use. Only time is the test's: a momentum
// is made when neither party has anything left to do.

const zexp = g.Zexp

var (
	hundredQsr = big.NewInt(100 * zexp)
	tenZnn     = big.NewInt(10 * zexp)
	theDeal    = Deal{GiveToken: types.QsrTokenStandard, GiveAmount: hundredQsr, GetToken: types.ZnnTokenStandard, GetAmount: tenZnn, AliceFor: 600, Gap: 300}
	bobWants   = Wanted{GiveToken: types.ZnnTokenStandard, GiveAmount: tenZnn, GetToken: types.QsrTokenStandard, GetAmount: hundredQsr}
)

// Genesis balances of the mock chain's first two users, in whole coins.
const (
	aliceZnn, aliceQsr = 12000, 120000
	bobZnn, bobQsr     = 8000, 80000
)

type lab struct {
	t      *testing.T
	z      mock.MockZenon
	cl     *nom.Client
	pipe   *Pipe
	dir    string
	alice  *Party
	bob    *Party
	setup  map[string]func(*Party)
	gone   map[string]bool // a party that has walked away for good
	down   int             // momentums a stopped party stays down before it starts again
	chains map[string]Chain
	links  map[string]Link
}

// activatePtlc is go-zenon's own test helper (vm/embedded/tests/ptlc_test.go):
// the spork is created and activated on the mock chain, and its id put where
// the contract looks for it.
func activatePtlc(t *testing.T, z mock.MockZenon) {
	t.Helper()
	oldID := types.PtlcSpork.SporkId
	oldImplemented := types.ImplementedSporksMap[oldID]
	var activated types.Hash
	t.Cleanup(func() {
		if oldImplemented {
			types.ImplementedSporksMap[oldID] = true
		} else {
			delete(types.ImplementedSporksMap, oldID)
		}
		if activated != oldID {
			delete(types.ImplementedSporksMap, activated)
		}
		types.PtlcSpork.SporkId = oldID
	})
	z.InsertSendBlock(&znom.AccountBlock{
		Address: g.Spork.Address, ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName, "spork-ptlc", "activate spork for ptlc"),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	list, _ := embedded.NewSporkApi(z).GetAll(0, 10)
	id := list.List[0].Id
	z.InsertSendBlock(&znom.AccountBlock{
		Address: g.Spork.Address, ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName, id),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.PtlcSpork.SporkId = id
	types.ImplementedSporksMap[id] = true
	activated = id
	z.InsertMomentumsTo(20)
}

func newLab(t *testing.T) *lab {
	t.Helper()
	z := mock.NewMockZenon(t)
	t.Cleanup(z.StopPanic)
	activatePtlc(t, z)

	server := rpcserver.NewServer()
	for _, a := range rpc.GetApis(z, nil, "ledger", "embedded") {
		if err := server.RegisterName(a.Namespace, a.Service); err != nil {
			t.Fatal(err)
		}
	}
	client := rpcserver.DialInProc(server)
	t.Cleanup(client.Close)

	l := &lab{t: t, z: z, cl: nom.Wrap(client, t.Logf), pipe: NewPipe(), dir: t.TempDir(),
		setup: map[string]func(*Party){}, gone: map[string]bool{}, chains: map[string]Chain{}, links: map[string]Link{}}
	l.chains["alice"], l.chains["bob"] = l.cl, l.cl
	l.links["alice"], l.links["bob"] = l.pipe.End("alice"), l.pipe.End("bob")
	return l
}

// start makes a party over its directory: a new swap the first time, and the
// same swap carried on every time after. That is all a restart is.
func (l *lab) start(role string) *Party {
	l.t.Helper()
	store, err := OpenStore(filepath.Join(l.dir, role))
	if err != nil {
		l.t.Fatal(err)
	}
	var p *Party
	if role == "alice" {
		p, err = NewAlice(store, g.User1, l.chains[role], l.links[role], theDeal)
	} else {
		p, err = NewBob(store, g.User2, l.chains[role], l.links[role], bobWants)
	}
	if err != nil {
		l.t.Fatal(err)
	}
	p.Log = func(f string, a ...interface{}) { l.t.Logf(role+" "+f, a...) }
	if l.setup[role] != nil {
		l.setup[role](p)
	}
	if role == "alice" {
		l.alice = p
	} else {
		l.bob = p
	}
	return p
}

func (l *lab) now() int64 {
	now, err := l.cl.Now()
	if err != nil {
		l.t.Fatal(err)
	}
	return now
}

// run steps both parties until both are done, or gone. each is called before
// every step and may hold a party back (return false for its role), restart
// it, or mark it gone.
func (l *lab) run(each func(role string, p *Party) bool) {
	l.t.Helper()
	if l.alice == nil {
		l.start("alice")
	}
	if l.bob == nil {
		l.start("bob")
	}
	for round := 0; round < 3000; round++ {
		moved, done := false, len(l.gone)
		for _, role := range []string{"alice", "bob"} {
			p := l.alice
			if role == "bob" {
				p = l.bob
			}
			if l.gone[role] || (each != nil && !each(role, p)) {
				continue
			}
			// each may have restarted the party
			if role == "alice" {
				p = l.alice
			} else {
				p = l.bob
			}
			status, err := p.Step()
			switch {
			case errors.Is(err, ErrCrash):
				for i := 0; i < l.down; i++ {
					l.z.InsertNewMomentum()
				}
				l.start(role)
				moved = true
			case err != nil:
				l.t.Fatalf("%s at %q: %v", role, p.State().Stage, err)
			case status == Progress:
				moved = true
			case status == Done:
				done++
			}
		}
		if done == 2 {
			return
		}
		if !moved {
			l.z.InsertNewMomentum()
		}
	}
	l.t.Fatalf("the swap did not end: alice at %q, bob at %q", l.alice.State().Stage, l.bob.State().Stage)
}

func (l *lab) receiveAll() {
	l.t.Helper()
	// A party is done when the contract has answered its last call. What the
	// contract paid out in that answer is receivable once it is in a momentum.
	l.z.InsertNewMomentum()
	l.z.InsertNewMomentum()
	ledger := api.NewLedgerApi(l.z)
	for _, user := range []*wallet.KeyPair{g.User1, g.User2} {
		unreceived, err := ledger.GetUnreceivedBlocksByAddress(user.Address, 0, 50)
		if err != nil {
			l.t.Fatal(err)
		}
		for _, block := range unreceived.List {
			l.z.InsertReceiveBlock(block.AccountBlock.Header(), nil, nil, mock.SkipVmChanges)
		}
	}
}

// balances holds the chain to what each party ends with, in whole coins, and
// to a contract with nothing left in it: a second Create made by mistake would
// still be sitting there.
func (l *lab) balances(aZnn, aQsr, bZnn, bQsr int64) {
	l.t.Helper()
	l.receiveAll()
	l.z.ExpectBalance(g.User1.Address, types.ZnnTokenStandard, aZnn*zexp)
	l.z.ExpectBalance(g.User1.Address, types.QsrTokenStandard, aQsr*zexp)
	l.z.ExpectBalance(g.User2.Address, types.ZnnTokenStandard, bZnn*zexp)
	l.z.ExpectBalance(g.User2.Address, types.QsrTokenStandard, bQsr*zexp)
	l.z.ExpectBalance(types.PtlcContract, types.ZnnTokenStandard, 0)
	l.z.ExpectBalance(types.PtlcContract, types.QsrTokenStandard, 0)
}

func (l *lab) outcomes(alice, bob string) {
	l.t.Helper()
	a, b := l.alice.State(), l.bob.State()
	if a.Outcome != alice || (b.Outcome != bob && !l.gone["bob"]) {
		l.t.Fatalf("alice ended %q (%s) and bob %q (%s); wanted %q and %q", a.Outcome, a.Reason, b.Outcome, b.Reason, alice, bob)
	}
}

func (l *lab) swapped() {
	l.t.Helper()
	l.outcomes(OutcomeSwapped, OutcomeSwapped)
	l.balances(aliceZnn+10, aliceQsr-100, bobZnn-10, bobQsr+100)
}

// onChain is every byte the swap put on the chain: the data of every block of
// both parties and of the contract.
func (l *lab) onChain() []byte {
	l.t.Helper()
	ledger := api.NewLedgerApi(l.z)
	var all []byte
	for _, address := range []types.Address{g.User1.Address, g.User2.Address, types.PtlcContract} {
		list, err := ledger.GetAccountBlocksByHeight(address, 1, 500)
		if err != nil {
			l.t.Fatal(err)
		}
		for _, b := range list.List {
			all = append(all, b.Data...)
			all = append(all, 0xff)
		}
	}
	return all
}

func TestSwap(t *testing.T) {
	l := newLab(t)
	l.run(nil)
	l.swapped()

	// What the swap is for: neither the secret nor its point is on the chain,
	// and the two entries share no key.
	a, b := l.alice.State(), l.bob.State()
	if !bytes.Equal(a.Secret, b.Secret) {
		t.Fatal("Bob did not end with Alice's secret")
	}
	chain := l.onChain()
	if bytes.Contains(chain, a.Secret) {
		t.Fatal("the secret is on the chain")
	}
	if bytes.Contains(chain, a.Terms.T[1:]) {
		t.Fatal("the secret's point is on the chain")
	}
	if !bytes.Contains(chain, a.Terms.AliceLeg.Key) || !bytes.Contains(chain, a.Terms.BobLeg.Key) {
		t.Fatal("the lock keys are not on the chain, so this test is not reading it")
	}
	if bytes.Equal(a.Terms.AliceLeg.Key, a.Terms.BobLeg.Key) {
		t.Fatal("both entries are locked to one key")
	}
}

// A party that stops and starts again at any stage carries on from its
// directory, and the swap ends as if it had not stopped.
func TestSwapSurvivesARestartAtEveryStage(t *testing.T) {
	stages := map[string][]string{
		"alice": {StageOffered, StageAccepted, StageLocking, StageLocked, StagePresigned, StageChecking, StageRevealing, StageClaiming},
		"bob":   {StageAccepted, StageChecking, StageLocking, StageLocked, StagePresigned, StageSecret, StageClaiming, StageSweeping},
	}
	for role, list := range stages {
		for _, stage := range list {
			role, stage := role, stage
			t.Run(role+" at "+stage, func(t *testing.T) {
				l := newLab(t)
				restarted := false
				l.run(func(r string, p *Party) bool {
					if r == role && !restarted && p.State().Stage == stage {
						restarted = true
						l.start(role)
					}
					return true
				})
				if !restarted {
					t.Fatalf("%s never reached %q", role, stage)
				}
				l.swapped()
			})
		}
	}
}

// The worst moment to stop: a block is published and the party has not yet
// written down that it sent it. Started again, it must find the block and not
// send a second, which for a Create would lock the funds twice. It is tried
// with the party back at once, before the contract has answered the block, and
// back three momentums later, after it has: the two find different things on
// the chain.
func TestSwapSurvivesAStopBeforeSaving(t *testing.T) {
	type stop struct {
		role, point string
		down        int
	}
	var cases []stop
	for _, down := range []int{0, 3} {
		for _, role := range []string{"alice", "bob"} {
			for _, point := range []string{"created", "claimed"} {
				cases = append(cases, stop{role, point, down})
			}
		}
	}
	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s after it %s, down %d momentums", c.role, c.point, c.down), func(t *testing.T) {
			l := newLab(t)
			l.down = c.down
			fired := false
			l.setup[c.role] = func(p *Party) {
				p.Crash = func(point string) bool {
					if point != c.point || fired {
						return false
					}
					fired = true
					return true
				}
			}
			l.run(nil)
			if !fired {
				t.Fatalf("%s never reached the point %q", c.role, c.point)
			}
			l.swapped()
		})
	}
}

// Both parties stop at once and the link forgets everything in flight, which
// is what one process holding both ends does when it dies. Each side sends its
// last message again when it starts.
func TestSwapSurvivesLosingTheLink(t *testing.T) {
	for _, stage := range []string{StageOffered, StagePresigned} {
		stage := stage
		t.Run("alice at "+stage, func(t *testing.T) {
			l := newLab(t)
			lost := false
			l.run(func(r string, p *Party) bool {
				if r == "alice" && !lost && p.State().Stage == stage {
					lost = true
					l.pipe = NewPipe()
					l.links["alice"], l.links["bob"] = l.pipe.End("alice"), l.pipe.End("bob")
					l.start("alice")
					l.start("bob")
				}
				return true
			})
			if !lost {
				t.Fatalf("alice never reached %q", stage)
			}
			l.swapped()
		})
	}
}

// Both lock, and Alice is gone until after Bob has taken his funds back. His
// entry is no longer there to check, and she takes hers back.
func TestAliceNeverClaims(t *testing.T) {
	l := newLab(t)
	l.run(func(r string, p *Party) bool {
		if r == "alice" && p.State().Stage == StageChecking {
			return l.bob.State().Stage == StageDone
		}
		return true
	})
	l.outcomes(OutcomeRefunded, OutcomeRefunded)
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
	if bytes.Contains(l.onChain(), l.alice.State().Secret) {
		t.Fatal("the secret is on the chain")
	}
}

// Bob accepts and then is never heard from again. Alice has locked and sent a
// pre-signature that is useless without t; she waits out her entry and takes
// it back.
func TestBobNeverLocks(t *testing.T) {
	l := newLab(t)
	l.run(func(r string, p *Party) bool {
		// Alice has his answer once she is past her offer; from there he is gone.
		if r == "bob" && p.State().Stage == StageAccepted && l.alice.State().Stage != StageOffered {
			l.gone["bob"] = true
		}
		return !l.gone[r]
	})
	l.outcomes(OutcomeRefunded, "")
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
}

// Both lock, and Alice goes quiet with Bob's pre-signature checked and in
// hand. She returns half a minute before his expiry, which is inside the
// margin: a claim sent now could be answered too late. She keeps her secret,
// and each takes its own funds back.
func TestAliceComesBackTooLateToClaim(t *testing.T) {
	l := newLab(t)
	l.run(func(r string, p *Party) bool {
		if r == "alice" && p.State().Stage == StageRevealing {
			return l.now() >= p.State().Terms.BobLeg.Expiry-30
		}
		return true
	})
	l.outcomes(OutcomeRefunded, OutcomeRefunded)
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
	if bytes.Contains(l.onChain(), l.alice.State().Secret) {
		t.Fatal("the secret is on the chain")
	}
	if !strings.Contains(l.alice.State().Reason, "stays unrevealed") {
		t.Fatalf("alice gave up for another reason: %s", l.alice.State().Reason)
	}
}

// The mistake the margin exists for. Alice claims with seconds left; her claim
// is answered after Bob's expiry and refused, with her completed signature on
// the chain. Bob reads t from the refused call, takes her funds, and takes his
// own back.
func TestLateClaimCostsAlice(t *testing.T) {
	l := newLab(t)
	l.setup["alice"] = func(p *Party) { p.RevealLate = true }
	l.run(func(r string, p *Party) bool {
		if r == "alice" && p.State().Stage == StageRevealing {
			return l.now() >= p.State().Terms.BobLeg.Expiry-10
		}
		return true
	})
	l.outcomes(OutcomeLost, OutcomeBoth)
	l.balances(aliceZnn, aliceQsr-100, bobZnn, bobQsr+100)
}

// The same, with Bob stopping dead the moment his claim is published and
// before he has saved it, and staying down while it is answered. Started again
// he has no call written down, finds her entry gone by his own hand, and still
// takes his own back.
func TestLateClaimCostsAliceThoughBobStops(t *testing.T) {
	l := newLab(t)
	l.down = 3
	l.setup["alice"] = func(p *Party) { p.RevealLate = true }
	fired := false
	l.setup["bob"] = func(p *Party) {
		p.Crash = func(point string) bool {
			if point != "claimed" || fired {
				return false
			}
			fired = true
			return true
		}
	}
	l.run(func(r string, p *Party) bool {
		if r == "alice" && p.State().Stage == StageRevealing {
			return l.now() >= p.State().Terms.BobLeg.Expiry-10
		}
		return true
	})
	if !fired {
		t.Fatal("bob never claimed")
	}
	l.outcomes(OutcomeLost, OutcomeBoth)
	l.balances(aliceZnn, aliceQsr-100, bobZnn, bobQsr+100)
}

// tamper is a link that alters one kind of message on its way out.
type tamper struct {
	Link
	kind string
	with func(*Message)
}

func (t tamper) Send(m Message) error {
	if m.Kind == t.kind {
		copied := m
		if m.Locked != nil {
			locked := *m.Locked
			locked.PreSig = append([]byte{}, locked.PreSig...)
			copied.Locked = &locked
		}
		if m.Offer != nil {
			offer := *m.Offer
			copied.Offer = &offer
		}
		t.with(&copied)
		return t.Link.Send(copied)
	}
	return t.Link.Send(m)
}

// Alice sends a pre-signature that does not verify. Bob locks nothing, and
// Alice gets her own funds back at her expiry.
func TestBobRefusesABadPreSignature(t *testing.T) {
	l := newLab(t)
	l.links["alice"] = tamper{l.links["alice"], KindAliceLocked, func(m *Message) { m.Locked.PreSig[40] ^= 1 }}
	l.run(nil)
	l.outcomes(OutcomeRefunded, OutcomeNothing)
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
	if !strings.Contains(l.bob.State().Reason, "pre-signature") {
		t.Fatalf("bob refused for another reason: %s", l.bob.State().Reason)
	}
}

// Bob sends a pre-signature made on a point of his own. Completing it would
// not give Alice a valid signature, and she checks before she tries: her
// secret stays unrevealed and both take their funds back.
func TestAliceRefusesABadPreSignature(t *testing.T) {
	l := newLab(t)
	l.links["bob"] = tamper{l.links["bob"], KindBobLocked, func(m *Message) { m.Locked.PreSig[40] ^= 1 }}
	l.run(nil)
	l.outcomes(OutcomeRefunded, OutcomeRefunded)
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
	if bytes.Contains(l.onChain(), l.alice.State().Secret) {
		t.Fatal("the secret is on the chain")
	}
}

// openDestination is a chain through which every Create names nobody: what a
// dishonest Alice would publish, so that her key could take the entry back.
type openDestination struct{ Chain }

func (c openDestination) Create(key *wallet.KeyPair, pointType uint8, lock []byte, _ types.Address, expiry int64, token types.ZenonTokenStandard, amount *big.Int) (types.Hash, error) {
	return c.Chain.Create(key, pointType, lock, types.ZeroAddress, expiry, token, amount)
}

// The contract takes a key lock that names no destination. Bob does not: the
// lock's key is Alice's, and she could sign such an entry back to herself after
// claiming his (go-zenon's TestPtlc_keySwap_openDestinationLosesBob).
func TestBobRefusesAnEntryThatNamesNobody(t *testing.T) {
	l := newLab(t)
	l.chains["alice"] = openDestination{l.cl}
	l.run(nil)
	l.outcomes(OutcomeRefunded, OutcomeNothing)
	l.balances(aliceZnn, aliceQsr, bobZnn, bobQsr)
	if !strings.Contains(l.bob.State().Reason, "names no destination") {
		t.Fatalf("bob refused for another reason: %s", l.bob.State().Reason)
	}
}

// Bob holds Alice to the trade he came for and to times he can live with.
func TestCheckOffer(t *testing.T) {
	_, point, _ := NewSecret()
	key, _ := NewKey()
	good := func() *Offer {
		return &Offer{T: point, AliceKey: XOnly(key), Alice: g.User1.Address,
			AliceToken: types.QsrTokenStandard, AliceAmount: hundredQsr, AliceExpiry: 1600,
			BobToken: types.ZnnTokenStandard, BobAmount: tenZnn, BobExpiry: 1300}
	}
	const now = 1000
	if err := CheckOffer(good(), bobWants, now, DevnetPolicy); err != nil {
		t.Fatalf("a good offer was refused: %v", err)
	}
	for name, spoil := range map[string]func(*Offer){
		"T is not a point":             func(o *Offer) { o.T = append([]byte{0x02}, make([]byte, 32)...) },
		"Alice's key is not a key":     func(o *Offer) { o.AliceKey = make([]byte, 32) },
		"Alice has no address":         func(o *Offer) { o.Alice = types.ZeroAddress },
		"Alice is a contract":          func(o *Offer) { o.Alice = types.PtlcContract },
		"she offers less":              func(o *Offer) { o.AliceAmount = big.NewInt(99 * zexp) },
		"she offers another token":     func(o *Offer) { o.AliceToken = types.ZnnTokenStandard },
		"she asks more":                func(o *Offer) { o.BobAmount = big.NewInt(11 * zexp) },
		"her entry ends first":         func(o *Offer) { o.AliceExpiry, o.BobExpiry = o.BobExpiry, o.AliceExpiry },
		"her entry barely outlasts":    func(o *Offer) { o.AliceExpiry = o.BobExpiry + DevnetPolicy.MinGap() - 1 },
		"his entry would end too soon": func(o *Offer) { o.BobExpiry = now + DevnetPolicy.aliceLockBy() - 1; o.AliceExpiry = o.BobExpiry + 300 },
		"no amount":                    func(o *Offer) { o.AliceAmount = nil },
	} {
		o := good()
		spoil(o)
		if err := CheckOffer(o, bobWants, now, DevnetPolicy); err == nil {
			t.Errorf("%s: the offer was accepted", name)
		}
	}
}

func TestCheckEntry(t *testing.T) {
	key, _ := NewKey()
	leg := Leg{Key: XOnly(key), Destination: g.User2.Address, Token: types.QsrTokenStandard, Amount: hundredQsr, Expiry: 1600}
	good := func() *definition.PtlcInfo {
		return &definition.PtlcInfo{TimeLocked: g.User1.Address, TokenStandard: leg.Token, Amount: new(big.Int).Set(leg.Amount),
			ExpirationTime: leg.Expiry, PointType: definition.PointTypeBIP340, PointLock: leg.Key, Destination: leg.Destination}
	}
	if err := CheckEntry(good(), leg); err != nil {
		t.Fatalf("a good entry was refused: %v", err)
	}
	if err := CheckEntry(nil, leg); err == nil {
		t.Error("a missing entry was accepted")
	}
	other, _ := NewKey()
	for name, spoil := range map[string]func(*definition.PtlcInfo){
		"names nobody":      func(i *definition.PtlcInfo) { i.Destination = types.ZeroAddress },
		"pays someone else": func(i *definition.PtlcInfo) { i.Destination = g.User3.Address },
		"another key":       func(i *definition.PtlcInfo) { i.PointLock = XOnly(other) },
		"an Ed25519 lock":   func(i *definition.PtlcInfo) { i.PointType = definition.PointTypeED25519 },
		"a point lock":      func(i *definition.PtlcInfo) { i.PointType = definition.PointTypeSecp256k1Point },
		"less in it":        func(i *definition.PtlcInfo) { i.Amount = big.NewInt(1) },
		"another token":     func(i *definition.PtlcInfo) { i.TokenStandard = types.ZnnTokenStandard },
		"ends sooner":       func(i *definition.PtlcInfo) { i.ExpirationTime-- },
		"ends later":        func(i *definition.PtlcInfo) { i.ExpirationTime++ },
	} {
		info := good()
		spoil(info)
		if err := CheckEntry(info, leg); err == nil {
			t.Errorf("%s: the entry was accepted", name)
		}
	}
}

// The pre-signature is bound to one entry, one address and one T; completing
// it gives a signature the contract's verifier takes, and that signature gives
// the secret back. All four combinations of key and nonce parity turn up in a
// few hundred random runs, and each must work.
func TestPreSignatureRoundTrip(t *testing.T) {
	const chain = 69
	id := types.HexToHashPanic("4bd1b48fe7253f6f30d9bdc6e0ef3ec28e9d2dbcc0ae60e4abf7cb3d4f8b1bd2")
	seen := map[[2]bool]bool{}
	for i := 0; i < 300; i++ {
		secret, point, _ := NewSecret()
		key, _ := NewKey()
		leg := Leg{Key: XOnly(key), Destination: g.User2.Address}
		presig, err := PreSign(key, chain, id, leg, point)
		if err != nil {
			t.Fatal(err)
		}
		seen[[2]bool{key.PubKey().SerializeCompressed()[0] == 0x03, presig[0] == 0x03}] = true
		if err := VerifyPreSig(presig, chain, id, leg, point); err != nil {
			t.Fatal(err)
		}

		// bound to the entry, the address, the chain and T
		otherLeg := leg
		otherLeg.Destination = g.User3.Address
		_, otherPoint, _ := NewSecret()
		if VerifyPreSig(presig, chain, types.Hash{1}, leg, point) == nil ||
			VerifyPreSig(presig, chain, id, otherLeg, point) == nil ||
			VerifyPreSig(presig, chain+1, id, leg, point) == nil ||
			VerifyPreSig(presig, chain, id, leg, otherPoint) == nil {
			t.Fatal("a pre-signature verified for something it was not made for")
		}

		sig, err := Complete(presig, secret, chain, id, leg)
		if err != nil {
			t.Fatal(err)
		}
		wrong, _, _ := NewSecret()
		if _, err := Complete(presig, wrong, chain, id, leg); err == nil {
			t.Fatal("a wrong secret completed the pre-signature")
		}
		got, err := Extract(presig, sig, point)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatalf("the completed signature did not give the secret back: %v", err)
		}

		// An ordinary signature by the same key over the same message, which
		// is what the key's owner could publish instead, gives nothing.
		plain, err := schnorr.Sign(key, UnlockMessage(chain, id, leg.Destination))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Extract(presig, plain.Serialize(), point); err == nil {
			t.Fatal("an unrelated signature yielded a secret")
		}
	}
	if len(seen) != 4 {
		t.Fatalf("only %d of the 4 parity cases came up", len(seen))
	}
}

func TestStoreKeepsTheOldStateOrTheNew(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := store.Load(); err != nil || st != nil {
		t.Fatalf("an empty directory loaded as %v, %v", st, err)
	}
	first := &State{Role: "bob", Stage: StageChecking, Account: g.User2.Address, Key: []byte{1, 2, 3},
		Theirs: Locked{ID: types.Hash{7}, PreSig: bytes.Repeat([]byte{9}, 65)},
		Offer:  &Offer{AliceAmount: hundredQsr, BobAmount: tenZnn, AliceToken: types.QsrTokenStandard, BobToken: types.ZnnTokenStandard}}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	// A write that died half way leaves its temporary file and nothing else.
	if err := os.WriteFile(filepath.Join(dir, "swap.json.tmp"), []byte(`{"role":"bob","stage":"lock`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != StageChecking || !bytes.Equal(got.Theirs.PreSig, first.Theirs.PreSig) || got.Theirs.ID != first.Theirs.ID ||
		got.Offer.AliceAmount.Cmp(hundredQsr) != 0 || got.Offer.BobToken != types.ZnnTokenStandard || got.Account != g.User2.Address {
		t.Fatalf("the state did not come back as saved: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "swap.json"), []byte(`{"role":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a state that does not parse was loaded")
	}
}

// A directory is one party's. Bob started over Alice's would sign her swap's
// calls with his account.
func TestAStoreBelongsToOneParty(t *testing.T) {
	l := newLab(t)
	l.start("alice")
	if _, err := l.alice.Step(); err != nil {
		t.Fatal(err)
	}
	store, _ := OpenStore(filepath.Join(l.dir, "alice"))
	if _, err := NewBob(store, g.User2, l.cl, l.links["bob"], bobWants); err == nil {
		t.Fatal("Bob started over Alice's state")
	}
	if _, err := NewAlice(store, g.User3, l.cl, l.links["alice"], theDeal); err == nil {
		t.Fatal("another account started over Alice's state")
	}
}
