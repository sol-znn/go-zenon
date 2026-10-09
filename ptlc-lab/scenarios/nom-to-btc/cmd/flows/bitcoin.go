package main

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// The two Bitcoin scenarios are the nom-to-btc program, run as it is. It
// already plays both parties, builds the Taproot output and the adaptor
// signature, and has been run end to end; writing the swap a second time here
// would be a second thing to keep right. This reads its log and reports the
// steps the catalogue names. The log lines matched are that program's own
// (../../main.go), and a step whose line never comes is a failed run.

type logRule struct {
	role     string // "alice" or "bob", as the program prefixes its lines
	contains string
	step     string
	state    string // active or done
	answer   string
	detail   string
}

var swapRules = map[string][]logRule{
	"happy": {
		{"alice", "funds ", "btc-lock", "active", "", ""},
		{"alice", "funded ", "btc-lock", "done", "", "In a Taproot output only the two of them can spend"},
		{"alice", "pre-signs", "presign", "active", "", ""},
		{"bob", "verified the pre-signature", "presign", "done", "", "Bob checked it: the secret is the one missing piece"},
		{"bob", "locks ", "znn-lock", "active", "", ""},
		{"bob", "is live", "znn-lock", "done", accepted, "The contract accepted"},
		{"alice", "reveals t", "reveal", "active", "", ""},
		{"alice", "received ", "reveal", "done", accepted, "The contract accepted"},
		{"alice", "received ", "znn-paid", "done", "", "10 ZNN paid to Alice"},
		{"bob", "read t from the chain", "read", "done", "", "It is in Alice's unlock, public to everyone now"},
		{"bob", "claimed ", "btc-claim", "done", "", "An ordinary-looking Bitcoin payment; nothing links it to the ZNN"},
	},
	"refund": {
		{"alice", "funds ", "btc-lock", "active", "", ""},
		{"alice", "funded ", "btc-lock", "done", "", "In a Taproot output only the two of them can spend"},
		{"alice", "pre-signs", "presign", "active", "", ""},
		{"bob", "verified the pre-signature", "presign", "done", "", "Bob checked it: the secret is the one missing piece"},
		{"bob", "locks ", "znn-lock", "active", "", ""},
		{"bob", "is live", "znn-lock", "done", accepted, "The contract accepted"},
		{"alice", "never reveals", "stall", "done", "", "Her secret stays secret, so Bob cannot finish the bitcoin signature"},
		{"alice", "waits for height", "btc-refund", "active", "", "Waiting for Bitcoin to reach the refund height"},
		{"alice", "refunded in", "btc-refund", "done", "", "Her bitcoin is back, by the refund path she kept for herself"},
		{"bob", "no reveal", "znn-reclaim", "active", "", "Waiting for the deadline on this chain"},
		{"bob", "reclaimed the ZNN", "znn-reclaim", "done", accepted, "The contract accepted"},
	},
	"bad-terms": {
		{"alice", "proposes:", "terms", "done", "", "Her refund would open two blocks from now; his ZNN would be open to her for four minutes"},
		{"bob", "refuses the terms", "refuse", "done", "", "He gave her nothing to sign against and locked nothing"},
	},
}

func runBitcoinSwap(path string) func(*runner) (string, error) {
	return func(r *runner) (string, error) {
		args := []string{"-nom", r.nomURL, "-btc", r.btcHost, "-scenario", path}
		if path == "refund" {
			// The shortest wait the swap's own safety margins allow on this
			// devnet; the defaults are sized for reading along.
			args = append(args, "-expiry", "150", "-locktime", "10")
		}
		cmd := exec.Command(r.swapPath, args...)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("could not start %s: %w", r.swapPath, err)
		}
		finished := false
		lines := bufio.NewScanner(pipe)
		lines.Buffer(make([]byte, 64*1024), 1024*1024)
		for lines.Scan() {
			line := lines.Text()
			say("%s", line)
			if strings.Contains(line, "done: "+path) {
				finished = true
			}
			for _, rule := range swapRules[path] {
				if !strings.Contains(line, " "+rule.role+" ") || !strings.Contains(line, rule.contains) {
					continue
				}
				if rule.state == "done" {
					r.mu.Lock()
					r.settled[rule.step] = true
					r.mu.Unlock()
				}
				emit(event{Step: rule.step, State: rule.state, Answer: rule.answer, Detail: rule.detail})
			}
		}
		if err := cmd.Wait(); err != nil || !finished {
			return "", errors.New("the swap program stopped before the swap was over; its last lines are in the log")
		}
		switch path {
		case "refund":
			return "Alice has her bitcoin back and Bob has his ZNN back", nil
		case "bad-terms":
			return "Bob refused terms under which Alice could have taken the bitcoin back and the ZNN too; nothing was locked", nil
		}
		return "Alice got 10 ZNN and Bob got the bitcoin, with nobody in between", nil
	}
}
