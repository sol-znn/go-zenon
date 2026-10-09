// nom-to-btc runs the NoM-to-BTC swap of zenon-ptlc/SPEC.md §4 end to end:
// Alice has regtest bitcoin and wants devnet ZNN, Bob the reverse. Both
// roles run in this process and talk over channels; every value that
// crosses between them is one a real client would send over a wire.
//
//	go run . -scenario happy      # Alice reveals, Bob claims the bitcoin
//	go run . -scenario refund     # Alice never reveals: Bob reclaims, Alice refunds
//	go run . -scenario bad-terms  # Alice's refund would open too soon: Bob refuses
//
// It needs the devnet on the go-zenon-ptlc branch (docker compose up in that
// worktree) and the regtest bitcoind in ./compose.yml.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/wallet"

	"zenon-ptlc/scenarios/nom-to-btc/adaptor"
	"zenon-ptlc/scenarios/nom-to-btc/btc"
	"zenon-ptlc/scenarios/nom-to-btc/nom"
)

// terms is what Alice proposes. E is a NoM timestamp, L a Bitcoin height.
type terms struct {
	T        []byte
	AliceX   [32]byte
	AliceNoM types.Address
	Sats     int64
	Znn      *big.Int
	E        int64
	L        uint32
}

type accept struct {
	BobX   [32]byte
	BobBTC btcutil.Address
	BobNoM types.Address
}

type funded struct {
	Outpoint wire.OutPoint
	Value    int64
}

// refusal is Bob's answer to terms he will not take, in place of an accept.
type refusal struct {
	Reason string
}

// claimTerms is what Bob asks Alice to pre-sign: the exact transaction.
type claimTerms struct {
	Dest btcutil.Address
	Fee  int64
}

var errRefused = errors.New("Bob refused the terms")

const (
	claimFee = int64(500)
	// Alice will not reveal with less than this left on the NoM expiry. Her
	// unlock is answered two momentums after it is sent, and one answered
	// after E is refused with t in the clear: Bob reclaims his ZNN and claims
	// the bitcoin with t as well. The CLI and the console keep the same margin.
	revealMargin = int64(60)

	// What Bob assumes of Bitcoin when he weighs L against E. His entry is open
	// to Alice until E, and her refund leaf opens at height L; if L could come
	// first, she could take the bitcoin back and still claim the ZNN. Blocks are
	// taken to come twice as fast as the ten-minute target, which is the side
	// that hurts him, and he wants claimBlocks more after E to get his claim
	// mined once he has seen t.
	fastBlockSeconds = int64(300)
	claimBlocks      = int64(3)
)

// checkTerms is Bob's test of Alice's proposal before he gives her anything.
// now is the NoM chain's clock and tip Bitcoin's height, both read by Bob.
func checkTerms(tm terms, now, tip int64) error {
	if tm.E < now+120 {
		return fmt.Errorf("the NoM expiry is %d s away; Alice needs at least 120 to claim", tm.E-now)
	}
	if _, err := btcec.ParsePubKey(tm.T); err != nil || len(tm.T) != 33 {
		return fmt.Errorf("T is not a compressed point")
	}
	blocks := int64(tm.L) - tip
	need := (tm.E-now+fastBlockSeconds-1)/fastBlockSeconds + claimBlocks
	if blocks < need {
		return fmt.Errorf("her refund opens %d blocks from now (height %d), and the ZNN stays open to her for %d s: at a block every %d s that is before his entry closes plus %d blocks to claim; he needs at least %d",
			blocks, tm.L, tm.E-now, fastBlockSeconds, claimBlocks, need)
	}
	return nil
}

func main() {
	nomURL := flag.String("nom", "http://localhost:35997", "znnd HTTP RPC of the devnet")
	btcHost := flag.String("btc", "127.0.0.1:18443", "bitcoind regtest RPC")
	scenario := flag.String("scenario", "happy", "happy, refund or bad-terms")
	sats := flag.Int64("sats", 100_000, "bitcoin Alice sells")
	znn := flag.Int64("znn", 10, "ZNN Bob sells")
	expiry := flag.Int64("expiry", 240, "NoM expiry, seconds after the frontier momentum")
	locktime := flag.Int64("locktime", 30, "Bitcoin refund height, blocks after the tip")
	// One is enough on regtest, where only this program mines. On a real chain
	// Bob locks ZNN against Alice's funding, and a funding transaction that a
	// reorganisation takes away leaves his ZNN open to her t with nothing on
	// Bitcoin for him: six is the usual figure.
	confirmations := flag.Int64("confirmations", 1, "blocks Bob waits for on Alice's funding before he locks")
	flag.Parse()
	if *scenario == "bad-terms" {
		// a refund two blocks out against an expiry minutes away
		*locktime = 2
	}

	ctx := context.Background()
	logf := func(role string) func(string, ...interface{}) {
		return func(f string, a ...interface{}) { log.Printf("%-5s "+f, append([]interface{}{role}, a...)...) }
	}

	rt, err := btc.Dial(*btcHost, "ptlc", "ptlc")
	must(err)
	prepareRegtest(rt)

	aliceNoM, err := nom.Dial(ctx, *nomURL, logf("alice"))
	must(err)
	bobNoM, err := nom.Dial(ctx, *nomURL, logf("bob"))
	must(err)

	aliceKey, err := nom.DevKey(1)
	must(err)
	bobKey, err := nom.DevKey(3)
	must(err)
	aliceBTC, err := btcec.NewPrivateKey()
	must(err)
	bobBTC, err := btcec.NewPrivateKey()
	must(err)

	toBob := make(chan interface{}, 8)
	toAlice := make(chan interface{}, 8)
	errs := make(chan error, 2)

	a := &alice{rt: rt, nom: aliceNoM, key: aliceKey, btcKey: aliceBTC, in: toAlice, out: toBob, log: logf("alice"), reveal: *scenario == "happy", conf: *confirmations}
	b := &bob{rt: rt, nom: bobNoM, key: bobKey, btcKey: bobBTC, in: toBob, out: toAlice, log: logf("bob"), conf: *confirmations}

	go func() { errs <- a.run(ctx, *sats, big.NewInt(*znn*nom.Zexp), *expiry, uint32(*locktime)) }()
	go func() { errs <- b.run(ctx) }()
	refused := 0
	for i := 0; i < 2; i++ {
		err := <-errs
		if errors.Is(err, errRefused) {
			refused++
			continue
		}
		if err != nil {
			log.Fatalf("swap failed: %v", err)
		}
	}
	switch {
	case *scenario == "bad-terms" && refused != 2:
		log.Fatalf("swap failed: Bob took terms he should have refused")
	case *scenario != "bad-terms" && refused != 0:
		log.Fatalf("swap failed: %v", errRefused)
	}
	log.Printf("done: %s", *scenario)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// prepareRegtest makes sure bitcoind has a wallet with spendable coins.
func prepareRegtest(rt *btc.Regtest) {
	if _, err := rt.CreateWallet("ptlc"); err != nil {
		if _, err2 := rt.LoadWallet("ptlc"); err2 != nil && !bytes.Contains([]byte(err2.Error()), []byte("already loaded")) {
			log.Fatalf("regtest wallet: %v / %v", err, err2)
		}
	}
	h, err := rt.Height()
	must(err)
	if h < 101 {
		addr, err := rt.GetNewAddress("miner")
		must(err)
		_, err = rt.Mine(101-h, addr)
		must(err)
	}
}

func mine(rt *btc.Regtest, n int64) {
	addr, err := rt.GetNewAddress("miner")
	must(err)
	_, err = rt.Mine(n, addr)
	must(err)
}

func xonly(k *btcec.PrivateKey) [32]byte {
	var x [32]byte
	copy(x[:], schnorr.SerializePubKey(k.PubKey()))
	return x
}

// ---- Alice ----------------------------------------------------------------

type alice struct {
	rt     *btc.Regtest
	nom    *nom.Client
	key    *wallet.KeyPair
	btcKey *btcec.PrivateKey
	in     <-chan interface{}
	out    chan<- interface{}
	log    func(string, ...interface{})
	reveal bool
	conf   int64
}

func (a *alice) run(ctx context.Context, sats int64, znn *big.Int, expiryAfter int64, locktimeAfter uint32) error {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	T, err := adaptor.PointOf(secret[:])
	if err != nil {
		return err
	}
	now, err := a.nom.Now()
	if err != nil {
		return err
	}
	tip, err := a.rt.Height()
	if err != nil {
		return err
	}
	tm := terms{T: T, AliceX: xonly(a.btcKey), AliceNoM: a.key.Address, Sats: sats, Znn: znn, E: now + expiryAfter, L: uint32(tip) + locktimeAfter}
	a.log("proposes: T=%x, %d sats for %s ZNN, E=%d (%ds out), L=%d (%d blocks out)", T, sats, znn, tm.E, expiryAfter, tm.L, locktimeAfter)
	a.out <- tm

	var acc accept
	switch m := (<-a.in).(type) {
	case accept:
		acc = m
	case refusal:
		a.log("Bob refused the terms: %s; nothing was locked", m.Reason)
		return errRefused
	}
	out, err := btc.NewSwapOutput(tm.AliceX, acc.BobX, tm.L, a.rt.Params)
	if err != nil {
		return err
	}
	a.log("funds %s (%d sats)", out.Address, sats)
	txid, err := a.rt.SendToAddress(out.Address, btcutil.Amount(sats))
	if err != nil {
		return fmt.Errorf("fund: %w", err)
	}
	mine(a.rt, a.conf)
	tx, err := a.rt.GetRawTransaction(txid)
	if err != nil {
		return err
	}
	op, value, err := btc.FindOutput(tx.MsgTx(), out.PkScript)
	if err != nil {
		return err
	}
	a.log("funded %s:%d, confirmed", op.Hash, op.Index)
	a.out <- funded{Outpoint: op, Value: value}

	ct := (<-a.in).(claimTerms)
	claim, err := btc.NewSpend(op, value, out.PkScript, ct.Dest, ct.Fee, 0, wire.MaxTxInSequenceNum)
	if err != nil {
		return err
	}
	h, err := claim.Sighash(out.ClaimLeaf)
	if err != nil {
		return err
	}
	var aux [32]byte
	if _, err := rand.Read(aux[:]); err != nil {
		return err
	}
	presig, err := adaptor.Sign(a.btcKey, h, T, aux)
	if err != nil {
		return err
	}
	a.log("pre-signs Bob's claim on T (sighash %x)", h[:8])
	a.out <- presig

	id := (<-a.in).(types.Hash)
	entry, err := a.nom.WaitEntry(id)
	if err != nil {
		return err
	}
	a.log("sees entry %s", nom.Summary(entry))
	switch {
	case !bytes.Equal(entry.PointLock, T):
		return fmt.Errorf("entry locks to %x, not T", entry.PointLock)
	case entry.Destination != a.key.Address:
		return fmt.Errorf("entry pays %s, not me", entry.Destination)
	case entry.ExpirationTime != tm.E:
		return fmt.Errorf("entry expires at %d, not %d", entry.ExpirationTime, tm.E)
	case entry.Amount.Cmp(znn) != 0 || entry.TokenStandard != types.ZnnTokenStandard:
		return fmt.Errorf("entry holds %s %s", entry.Amount, entry.TokenStandard)
	}

	if !a.reveal {
		a.log("refund scenario: never reveals t")
		return a.refund(ctx, out, op, value, tm.L)
	}

	now, err = a.nom.Now()
	if err != nil {
		return err
	}
	if now > tm.E-revealMargin {
		return fmt.Errorf("too close to expiry to reveal (now %d, E %d)", now, tm.E)
	}
	before, _ := a.nom.Balance(a.key.Address, types.ZnnTokenStandard)
	a.log("reveals t by unlocking %s", id)
	unlockHash, err := a.nom.Unlock(a.key, id, secret[:])
	if err != nil {
		return err
	}
	ok, err := a.nom.WaitContract(unlockHash)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the contract refused the unlock")
	}
	if _, err := a.nom.ReceiveAll(a.key); err != nil {
		return err
	}
	after, _ := a.nom.Balance(a.key.Address, types.ZnnTokenStandard)
	a.log("received %s ZNN (balance %s -> %s)", new(big.Int).Sub(after, before), before, after)
	return nil
}

func (a *alice) refund(ctx context.Context, out *btc.SwapOutput, op wire.OutPoint, value int64, L uint32) error {
	tip, err := a.rt.Height()
	if err != nil {
		return err
	}
	if uint32(tip) < L {
		a.log("waits for height %d (tip %d)", L, tip)
		mine(a.rt, int64(L)-tip)
	}
	dest, err := btc.KeyAddress(a.btcKey.PubKey(), a.rt.Params)
	if err != nil {
		return err
	}
	refund, err := btc.NewSpend(op, value, out.PkScript, dest, claimFee, L, wire.MaxTxInSequenceNum-1)
	if err != nil {
		return err
	}
	sig, err := refund.SignLeaf(out.RefundLeaf, a.btcKey)
	if err != nil {
		return err
	}
	if err := refund.SetRefundWitness(out, sig); err != nil {
		return err
	}
	txid, err := a.rt.Broadcast(refund.Tx)
	if err != nil {
		return fmt.Errorf("refund broadcast: %w", err)
	}
	mine(a.rt, 1)
	a.log("refunded in %s", txid)
	return nil
}

// ---- Bob ------------------------------------------------------------------

type bob struct {
	rt     *btc.Regtest
	nom    *nom.Client
	key    *wallet.KeyPair
	btcKey *btcec.PrivateKey
	in     <-chan interface{}
	out    chan<- interface{}
	log    func(string, ...interface{})
	conf   int64
}

func (b *bob) run(ctx context.Context) error {
	tm := (<-b.in).(terms)
	now, err := b.nom.Now()
	if err != nil {
		return err
	}
	tip, err := b.rt.Height()
	if err != nil {
		return err
	}
	if err := checkTerms(tm, now, tip); err != nil {
		b.log("refuses the terms before locking anything: %v", err)
		b.out <- refusal{Reason: err.Error()}
		return errRefused
	}
	dest, err := btc.KeyAddress(b.btcKey.PubKey(), b.rt.Params)
	if err != nil {
		return err
	}
	b.log("accepts: %d sats for %s ZNN, pays bitcoin to %s", tm.Sats, tm.Znn, dest)
	b.out <- accept{BobX: xonly(b.btcKey), BobBTC: dest, BobNoM: b.key.Address}

	out, err := btc.NewSwapOutput(tm.AliceX, xonly(b.btcKey), tm.L, b.rt.Params)
	if err != nil {
		return err
	}
	f := (<-b.in).(funded)
	txOut, err := b.rt.GetTxOut(&f.Outpoint.Hash, f.Outpoint.Index, false)
	if err != nil {
		return err
	}
	if txOut == nil || txOut.Confirmations < b.conf {
		return fmt.Errorf("funding has %d confirmations, fewer than the %d Bob waits for", confirmationsOf(txOut), b.conf)
	}
	pk, _ := hex.DecodeString(txOut.ScriptPubKey.Hex)
	if !bytes.Equal(pk, out.PkScript) || int64(txOut.Value*1e8+0.5) != tm.Sats {
		return fmt.Errorf("funding output does not match the terms")
	}
	b.log("checked the funding: %d sats at %s:%d", f.Value, f.Outpoint.Hash, f.Outpoint.Index)

	claim, err := btc.NewSpend(f.Outpoint, f.Value, out.PkScript, dest, claimFee, 0, wire.MaxTxInSequenceNum)
	if err != nil {
		return err
	}
	b.out <- claimTerms{Dest: dest, Fee: claimFee}
	presig := (<-b.in).([]byte)
	h, err := claim.Sighash(out.ClaimLeaf)
	if err != nil {
		return err
	}
	if !adaptor.Verify(tm.AliceX[:], h, tm.T, presig) {
		return fmt.Errorf("Alice's pre-signature does not verify")
	}
	b.log("verified the pre-signature: completes only with t")

	heightBefore, err := b.nom.ContractHeight()
	if err != nil {
		return err
	}
	b.log("locks %s ZNN to T for %s until %d", tm.Znn, tm.AliceNoM, tm.E)
	id, err := b.nom.CreatePoint(b.key, tm.T, tm.AliceNoM, tm.E, types.ZnnTokenStandard, tm.Znn)
	if err != nil {
		return err
	}
	if _, err := b.nom.WaitEntry(id); err != nil {
		return err
	}
	b.log("entry %s is live", id)
	b.out <- id

	// wait for t until E; after E the entry is his to reclaim
	deadline := time.Now().Add(time.Duration(tm.E-now+30) * time.Second)
	witness, err := b.nom.WaitWitness(ctx, id, heightBefore, deadline)
	if err != nil {
		b.log("no reveal: %v", err)
		return b.reclaim(id, tm.E)
	}
	T2, err := adaptor.PointOf(witness)
	if err != nil || !bytes.Equal(T2, tm.T) {
		return fmt.Errorf("witness is not the scalar behind T")
	}
	b.log("read t from the chain: %x", witness)

	aliceSig, err := adaptor.Complete(presig, witness)
	if err != nil {
		return err
	}
	bobSig, err := claim.SignLeaf(out.ClaimLeaf, b.btcKey)
	if err != nil {
		return err
	}
	if err := claim.SetClaimWitness(out, aliceSig, bobSig); err != nil {
		return err
	}
	if err := claim.Execute(int32(tm.L) - 1); err != nil {
		return fmt.Errorf("claim fails locally: %w", err)
	}
	txid, err := b.rt.Broadcast(claim.Tx)
	if err != nil {
		return fmt.Errorf("claim broadcast: %w", err)
	}
	mine(b.rt, 1)
	got, err := b.rt.GetTxOut(txid, 0, false)
	if err != nil || got == nil || got.Confirmations < 1 {
		return fmt.Errorf("claim not confirmed: %v", err)
	}
	b.log("claimed %d sats in %s; on Bitcoin only an ordinary signature appears", f.Value-claimFee, txid)
	return nil
}

func (b *bob) reclaim(id types.Hash, E int64) error {
	for {
		now, err := b.nom.Now()
		if err != nil {
			return err
		}
		if now >= E {
			break
		}
		time.Sleep(5 * time.Second)
	}
	h, err := b.nom.Reclaim(b.key, id)
	if err != nil {
		return err
	}
	ok, err := b.nom.WaitContract(h)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("reclaim refused")
	}
	if _, err := b.nom.ReceiveAll(b.key); err != nil {
		return err
	}
	b.log("reclaimed the ZNN after expiry")
	return nil
}

func confirmationsOf(out *btcjson.GetTxOutResult) int64 {
	if out == nil {
		return 0
	}
	return out.Confirmations
}

// keep the unused-import linter honest for types used only in some builds
var _ = chainhash.Hash{}
var _ = btcjson.TxRawResult{}
var _ = os.Exit
