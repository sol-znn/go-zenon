package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The rules that matter once the lab has a public name: what a request may
// make the container do, and what labd tells a page about where the node is.
// None of these starts a process or needs a node.

func testLab(t *testing.T) *lab {
	t.Helper()
	t.Setenv("LAB_PUBLIC_URL", "https://ptlc.zenon.foo")
	t.Setenv("LAB_HOSTS", "")
	return &lab{cfg: loadConfig()}
}

func TestCommandRefusesWhatWritesFiles(t *testing.T) {
	l := testLab(t)
	for _, args := range [][]string{
		{"wallet.export", "/srv/web/index.html"},
		{"wallet.createNew", "pass", "name"},
		{"wallet.createFromMnemonic", "words", "pass"},
		{"Wallet.Export", "/tmp/x"}, // the CLI's own matching is not this code's to rely on
		{"ptlc.get", "-u"},          // an option would move the node or the keystore
		{"../bin/sh"},
		{},
	} {
		if _, _, _, err := l.command(startRequest{Kind: "cli", Index: 1, Args: args}); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	for _, args := range [][]string{
		{"ptlc.keygen"},
		{"wallet.list"},
		{"wallet.deriveAddresses", "0", "4"},
		{"balance", "z1qq6eg8nhs4eqj0pcz4jy5v9jn7gx27c0l2w37t"},
	} {
		_, argv, _, err := l.command(startRequest{Kind: "cli", Index: 3, Args: args})
		if err != nil {
			t.Errorf("%q was refused: %v", args, err)
			continue
		}
		if got := strings.Join(argv[:11], " "); got != "znn-cli -u ws://127.0.0.1:35998 -c 69 -k ptlc-devnet -p ptlc -i 3" {
			t.Errorf("the node, chain and keystore are not the lab's own: %s", got)
		}
	}
}

func TestARunStillGoingIsNotForgotten(t *testing.T) {
	l := &lab{}
	zero := 0
	scenario := &run{ID: "scenario", Kind: "nom-to-btc", wake: make(chan struct{})}
	l.runs = append(l.runs, scenario)
	for i := 0; i < keepRuns+10; i++ {
		l.runs = append(l.runs, &run{ID: "done", Kind: "cli", Exit: &zero, wake: make(chan struct{})})
	}
	l.forget()
	if len(l.runs) != keepRuns {
		t.Fatalf("%d runs kept, want %d", len(l.runs), keepRuns)
	}
	if l.runs[0] != scenario || l.scenarioRunning() != scenario {
		t.Fatal("the scenario that is still running was dropped, and with it the hold on both dev accounts")
	}
}

func TestConfigNamesTheNodeByTheHostThatAsked(t *testing.T) {
	l := testLab(t)
	for host, want := range map[string][3]any{
		"ptlc.zenon.foo":      {true, "wss://ptlc.zenon.foo", "https://ptlc.zenon.foo/rpc"},
		"ptlc.zenon.foo:8472": {true, "wss://ptlc.zenon.foo", "https://ptlc.zenon.foo/rpc"},
		"localhost:8470":      {false, "ws://127.0.0.1:35998", "http://127.0.0.1:35997"},
		"127.0.0.1:8471":      {false, "ws://127.0.0.1:35998", "http://127.0.0.1:35997"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		l.handleConfig(rec, req)
		var got struct {
			Public     bool   `json:"public"`
			WalletNode string `json:"walletNode"`
			NodeHTTP   string `json:"nodeHttp"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Public != want[0] || got.WalletNode != want[1] || got.NodeHTTP != want[2] {
			t.Errorf("asked as %s: %+v, want %v", host, got, want)
		}
	}
}

func TestOnlyItsOwnPageUnderANameItWasGiven(t *testing.T) {
	l := testLab(t)
	h := sameOrigin(l.cfg.PublicHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, c := range []struct {
		host, origin string
		want         int
	}{
		{"ptlc.zenon.foo", "https://ptlc.zenon.foo", http.StatusNoContent},
		{"ptlc.zenon.foo", "", http.StatusNoContent},
		{"localhost:8470", "http://localhost:8470", http.StatusNoContent},
		{"ptlc.zenon.foo", "https://other.example", http.StatusForbidden},
		{"localhost:8470", "https://ptlc.zenon.foo", http.StatusForbidden},
		{"other.example", "", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/runs", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %q, Origin %q: %d, want %d", c.host, c.origin, rec.Code, c.want)
		}
	}
}

func TestBitcoindIsAskedForABoundedNumberOfBlocks(t *testing.T) {
	l := testLab(t)
	l.cfg.BtcRPC = "127.0.0.1:1" // nothing listens; a call that got this far answers 502
	for body, want := range map[string]int{
		`{"method":"generatetoaddress","params":[1000000,"bcrt1q"]}`:    http.StatusBadRequest,
		`{"method":"generatetoaddress","params":[1,"bcrt1q",99999999]}`: http.StatusBadRequest,
		`{"method":"generatetoaddress","params":["many","bcrt1q"]}`:     http.StatusBadRequest,
		`{"method":"generatetoaddress","params":[101,"bcrt1q"]}`:        http.StatusBadGateway,
		`{"method":"dumpprivkey","params":["x"]}`:                       http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		l.handleBtc(rec, httptest.NewRequest(http.MethodPost, "/api/btc", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
}

// Mining is the operator's: under the public name it is refused before the
// bounds are looked at, and on loopback the bounds still hold.
func TestThePublicNameCannotMine(t *testing.T) {
	l := testLab(t)
	l.cfg.BtcRPC = "127.0.0.1:1"
	for host, want := range map[string]int{"ptlc.zenon.foo": http.StatusForbidden, "127.0.0.1:8470": http.StatusBadGateway} {
		req := httptest.NewRequest(http.MethodPost, "/api/btc", strings.NewReader(`{"method":"generatetoaddress","params":[1,"bcrt1q"]}`))
		req.Host = host
		rec := httptest.NewRecorder()
		l.handleBtc(rec, req)
		if rec.Code != want {
			t.Errorf("mining asked for as %s: %d, want %d", host, rec.Code, want)
		}
	}
}

// A CLI run is the visitor's who started it: its id cannot be guessed from
// another's, and the list everyone reads leaves it out. A scenario run is
// listed, because the scenarios page's tally is everyone's.
func TestACliRunIsNotEveryones(t *testing.T) {
	l := testLab(t)
	cli1, err := l.start("cli", "", "znn-cli ptlc.keygen", []string{"no-such-program-here"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cli2, _ := l.start("cli", "", "znn-cli ptlc.keygen", []string{"no-such-program-here"}, nil)
	flow, err := l.start("flow", "secret", "Unlock with a secret", []string{"no-such-program-here"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cli1.ID) < 20 || cli1.ID == cli2.ID || !strings.HasPrefix(cli1.ID, "c") {
		t.Errorf("CLI run ids %q and %q are guessable", cli1.ID, cli2.ID)
	}
	rec := httptest.NewRecorder()
	l.handleRuns(rec, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	var out struct {
		Runs []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != 1 || out.Runs[0].ID != flow.ID {
		t.Errorf("the list is %+v; it should hold the scenario run and no CLI run", out.Runs)
	}
	if l.find(cli1.ID) != cli1 {
		t.Error("the CLI run cannot be found by its own id")
	}
}

// What a run prints is kept up to a bound, with a line that says it was cut.
func TestARunKeepsABoundedOutput(t *testing.T) {
	r := &run{wake: make(chan struct{})}
	for i := 0; i < maxLines+500; i++ {
		r.add("out", "x")
	}
	if len(r.Lines) != maxLines+1 || r.Lines[maxLines].Stream != "lab" || !strings.Contains(r.Lines[maxLines].Text, "cut") {
		t.Fatalf("%d lines kept, the last %+v", len(r.Lines), r.Lines[len(r.Lines)-1])
	}
}
