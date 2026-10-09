// keyswap runs the key swap of zenon-ptlc/SPEC.md §5 on the devnet: Alice
// trades QSR for Bob's ZNN on two BIP340 key locks, with a pre-signature each
// way and nothing of the secret on the chain.
//
//	keyswap                                  # the swap, start to finish
//	keyswap -dir swap1 -stop bob:locked      # stop dead once Bob has locked
//	keyswap -dir swap1                       # carry the same swap on
//	keyswap -dir swap2 -kill bob:created     # die between publishing Bob's lock and saving it
//
// Both parties run in this process, each over its own directory and its own
// connection to the node, and talk through keyswap.Pipe. A party is
// keyswap.Party and knows nothing of the other but what the link carries, so
// two processes joined by a real transport are the same code with another
// Link; that transport is not built yet (GAPS G-05).
//
// Everything a party must not lose is in its directory (keyswap.State). Run
// again over the same -dir and each side carries on from what it saved,
// sending its last message again, since the pipe forgot it.
//
// The accounts are the devnet's: index 1 of its mnemonic is Alice, index 3 Bob.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/wallet"

	"zenon-ptlc/scenarios/nom-to-btc/keyswap"
	"zenon-ptlc/scenarios/nom-to-btc/nom"
)

func main() {
	nomURL := flag.String("nom", "http://localhost:35997", "znnd HTTP RPC of the devnet")
	dir := flag.String("dir", "", "where the two parties keep their state; a new temporary directory if empty")
	qsr := flag.Int64("qsr", 10, "QSR Alice gives")
	znn := flag.Int64("znn", 1, "ZNN Bob gives")
	aliceFor := flag.Int64("alice-for", 600, "how long Alice's entry lasts, in seconds from her offer")
	gap := flag.Int64("gap", 300, "how much sooner Bob's entry ends")
	stop := flag.String("stop", "", "role:stage, to stop dead once that party has saved that stage (alice:presigned, bob:locked, ...)")
	kill := flag.String("kill", "", "role:point, to die between an action and saving it: created, claimed or reclaimed")
	flag.Parse()

	if *dir == "" {
		made, err := os.MkdirTemp("", "keyswap-")
		must(err)
		*dir = made
	}
	log.Printf("state in %s", *dir)
	stopRole, stopStage, _ := strings.Cut(*stop, ":")
	killRole, killPoint, _ := strings.Cut(*kill, ":")

	ctx := context.Background()
	pipe := keyswap.NewPipe()
	deal := keyswap.Deal{
		GiveToken: types.QsrTokenStandard, GiveAmount: big.NewInt(*qsr * nom.Zexp),
		GetToken: types.ZnnTokenStandard, GetAmount: big.NewInt(*znn * nom.Zexp),
		AliceFor: *aliceFor, Gap: *gap,
	}
	want := keyswap.Wanted{GiveToken: deal.GetToken, GiveAmount: deal.GetAmount, GetToken: deal.GiveToken, GetAmount: deal.GiveAmount}

	type side struct {
		role  string
		key   *wallet.KeyPair
		cl    *nom.Client
		party *keyswap.Party
	}
	sides := []*side{{role: "alice"}, {role: "bob"}}
	for i, index := range []uint32{1, 3} {
		s := sides[i]
		logf := func(f string, a ...interface{}) { log.Printf("%-5s "+f, append([]interface{}{s.role}, a...)...) }
		var err error
		s.key, err = nom.DevKey(index)
		must(err)
		s.cl, err = nom.Dial(ctx, *nomURL, logf)
		must(err)
		defer s.cl.Close()
		store, err := keyswap.OpenStore(filepath.Join(*dir, s.role))
		must(err)
		if s.role == "alice" {
			s.party, err = keyswap.NewAlice(store, s.key, s.cl, pipe.End(s.role), deal)
		} else {
			s.party, err = keyswap.NewBob(store, s.key, s.cl, pipe.End(s.role), want)
		}
		must(err)
		s.party.Log = logf
		if s.role == killRole {
			// The block is on its way to the node and nothing of it is on
			// disk: the moment a party can least afford to stop.
			s.party.Crash = func(point string) bool {
				if point == killPoint {
					logf("dies here, as asked, having %s and saved nothing; run again with -dir %s", point, *dir)
					os.Exit(3)
				}
				return false
			}
		}
		if stage := s.party.State().Stage; stage != "" {
			logf("carries on from %q", stage)
		}
	}

	// A party that only ever locked would spend its account down while its
	// payouts sat unreceived.
	for _, s := range sides {
		if _, err := s.cl.ReceiveAll(s.key); err != nil {
			log.Fatalf("%s: receiving what is waiting: %v", s.role, err)
		}
	}

	var wg sync.WaitGroup
	for _, s := range sides {
		wg.Add(1)
		go func(s *side) {
			defer wg.Done()
			last := s.party.State().Stage
			for {
				status, err := s.party.Step()
				if err != nil {
					log.Printf("%-5s will try again: %v", s.role, err)
				}
				if stage := s.party.State().Stage; stage != last {
					last = stage
					log.Printf("%-5s is at %q", s.role, stage)
					if s.role == stopRole && stage == stopStage {
						log.Printf("%-5s stops here, as asked; run again with -dir %s to carry on", s.role, *dir)
						os.Exit(3)
					}
				}
				if status == keyswap.Done {
					return
				}
				if status != keyswap.Progress || err != nil {
					time.Sleep(2 * time.Second)
				}
			}
		}(s)
	}
	wg.Wait()

	for _, s := range sides {
		st := s.party.State()
		reason := ""
		if st.Reason != "" {
			reason = " (" + st.Reason + ")"
		}
		log.Printf("%-5s ended: %s%s", s.role, st.Outcome, reason)
	}
	a, b := sides[0].party.State(), sides[1].party.State()
	if a.Outcome != keyswap.OutcomeSwapped || b.Outcome != keyswap.OutcomeSwapped {
		os.Exit(1)
	}
	fmt.Printf("done: swapped. Alice's entry %s, Bob's %s; on the chain, two signatures by two keys nobody has seen before\n", a.Mine.ID, b.Mine.ID)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
