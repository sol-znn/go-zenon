// labd is the lab console's back end. The page does everything it can in the
// browser: it reads the chain, encodes contract calls, and signs through the
// Syrius extension or with a devnet key of its own. What a browser cannot do
// is run a program, and two of the ways to drive the ptlc contract are
// programs: the Dart CLI and the Go scenarios. labd runs those and streams
// what they print.
//
// It also forwards JSON-RPC to the node and a short list of read-only calls to
// bitcoind, so that the page has one origin and no cross-origin rules to meet.
//
// It serves a second page as well, /scenarios/, which signs nothing and asks
// for nothing but a name: each of its buttons starts one scenario of the flows
// program (scenarios/nom-to-btc/cmd/flows), which signs every block itself
// with the devnet's keys and prints each step as it happens. labd runs it like
// any other program and the page draws what it prints.
//
// It executes binaries on request and signs with keys that are public, so it
// is a tool for a devnet and nothing else. It listens on loopback unless told
// otherwise and refuses a request another site's page sent.
//
// It can also be published under one name (LAB_PUBLIC_URL, behind the gateway
// in ../../gateway), and then a stranger has everything the page has. The
// chain is not what needs guarding there: the mnemonic is in this repository
// and the node's RPC is published beside the page, so anyone can already sign
// as Alice. What labd guards is the machine. A command cannot write a file, a
// handful of programs run at once and none for ever, and bitcoind is asked for
// a bounded number of blocks. The same rules hold on loopback; there is one
// labd, not a careful one and a trusting one.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	Listen       string
	WebRoot      string
	ZnndHTTP     string
	ZnndWS       string // what znn-cli dials, from where labd runs
	ZnndWSPublic string // what a browser wallet dials, from the host
	ZnndHTTPHost string // the node's HTTP RPC, from the host

	// Where the lab is published, if it is, and the node's two addresses
	// under that name. A request that names this host is told these instead
	// of the loopback ones, which mean nothing on a visitor's machine.
	PublicHost string
	PublicWS   string
	PublicRPC  string

	BtcRPC       string
	BtcUser      string
	BtcPass      string
	CLI          string
	Keystore     string
	KeystorePass string
	ChainID      string
	NomToBtc     string
	NomToNom     string
	Flows        string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() config {
	c := loadEnv()
	if raw := os.Getenv("LAB_PUBLIC_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
			log.Fatalf("LAB_PUBLIC_URL %q is not an http or https address", raw)
		}
		// The gateway sends a WebSocket upgrade on any path to the node's WS
		// port and /rpc to its HTTP port. The wallet's node has no path on
		// purpose: Syrius tells a page its node's scheme, host and port and
		// nothing after them, and the page dials what it is told.
		c.PublicHost = u.Hostname()
		c.PublicWS = map[string]string{"https": "wss", "http": "ws"}[u.Scheme] + "://" + u.Host
		c.PublicRPC = u.Scheme + "://" + u.Host + "/rpc"
	}
	return c
}

func loadEnv() config {
	return config{
		Listen:       env("LAB_LISTEN", "127.0.0.1:8470"),
		WebRoot:      os.Getenv("LAB_WEB_ROOT"),
		ZnndHTTP:     env("LAB_ZNND_HTTP", "http://127.0.0.1:35997"),
		ZnndWS:       env("LAB_ZNND_WS", "ws://127.0.0.1:35998"),
		ZnndWSPublic: env("LAB_ZNND_WS_PUBLIC", "ws://127.0.0.1:35998"),
		ZnndHTTPHost: env("LAB_ZNND_HTTP_PUBLIC", "http://127.0.0.1:35997"),
		BtcRPC:       env("LAB_BTC_RPC", "127.0.0.1:18443"),
		BtcUser:      env("LAB_BTC_USER", "ptlc"),
		BtcPass:      env("LAB_BTC_PASS", "ptlc"),
		CLI:          env("LAB_CLI", "znn-cli"),
		Keystore:     env("PTLC_KEYSTORE", "ptlc-devnet"),
		KeystorePass: env("PTLC_KEYSTORE_PASS", "ptlc"),
		ChainID:      env("LAB_CHAIN_ID", "69"),
		NomToBtc:     env("LAB_NOM_TO_BTC", "nom-to-btc"),
		NomToNom:     env("LAB_NOM_TO_NOM", "/opt/scenarios/nom-to-nom.sh"),
		Flows:        env("LAB_FLOWS", "flows"),
	}
}

// ---- runs --------------------------------------------------------------------

type line struct {
	N      int    `json:"n"`
	At     int64  `json:"at"` // unix milliseconds
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type run struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Flow    string   `json:"flow,omitempty"` // which scenario, for a run of kind flow
	Title   string   `json:"title"`
	Command []string `json:"command"`
	Started int64    `json:"started"`
	Ended   int64    `json:"ended,omitempty"`
	Exit    *int     `json:"exit,omitempty"`
	Lines   []line   `json:"lines,omitempty"`

	mu     sync.Mutex
	wake   chan struct{}
	cancel context.CancelFunc
}

// maxLines is the most a run keeps of what it prints. A run lives in memory
// until forgotten, and a command that prints without end (a CLI listing with a
// large page, an autoreceive left running) would otherwise hold its twenty
// minutes of output in the container's gigabyte.
const maxLines = 20000

func (r *run) add(stream, text string) {
	r.mu.Lock()
	switch n := len(r.Lines); {
	case n < maxLines:
		r.Lines = append(r.Lines, line{N: n, At: time.Now().UnixMilli(), Stream: stream, Text: text})
	case n == maxLines:
		r.Lines = append(r.Lines, line{N: n, At: time.Now().UnixMilli(), Stream: "lab",
			Text: fmt.Sprintf("output cut at %d lines; the program goes on, and what it prints from here is dropped", maxLines)})
	default:
		r.mu.Unlock()
		return
	}
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

func (r *run) live() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Exit == nil
}

func (r *run) finish(code int) {
	r.mu.Lock()
	r.Ended = time.Now().UnixMilli()
	r.Exit = &code
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

// since returns the lines from n on, whether the run has ended, and a channel
// that closes when there is more to read.
func (r *run) since(n int) ([]line, *int, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []line
	if n < len(r.Lines) {
		out = append(out, r.Lines[n:]...)
	}
	return out, r.Exit, r.wake
}

func (r *run) summary() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]any{
		"id": r.ID, "kind": r.Kind, "title": r.Title, "command": r.Command,
		"started": r.Started, "lines": len(r.Lines),
	}
	if r.Flow != "" {
		m["flow"] = r.Flow
	}
	if r.Exit != nil {
		m["ended"] = r.Ended
		m["exit"] = *r.Exit
	}
	return m
}

type lab struct {
	cfg config

	// What the flows program can run: its own list, read once at start and
	// passed to the page as it came. flowTitles is the same list as labd
	// needs it, to refuse a name that is not on it.
	flows      json.RawMessage
	flowTitles map[string]string

	mu   sync.Mutex
	runs []*run
	seq  int
}

// loadFlows asks the flows program what it can run. Without it the lab is the
// lab it was; only the scenarios page has nothing to show, and says why.
func (l *lab) loadFlows() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, l.cfg.Flows, "-list").Output()
	if err != nil {
		return fmt.Errorf("%s -list: %w", l.cfg.Flows, err)
	}
	var catalogue struct {
		Scenarios []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(out, &catalogue); err != nil {
		return fmt.Errorf("%s -list did not print a catalogue: %w", l.cfg.Flows, err)
	}
	l.flowTitles = map[string]string{}
	for _, s := range catalogue.Scenarios {
		l.flowTitles[s.ID] = s.Title
	}
	l.flows = json.RawMessage(bytes.TrimSpace(out))
	return nil
}

func (l *lab) handleFlows(w http.ResponseWriter, r *http.Request) {
	if l.flows == nil {
		fail(w, http.StatusServiceUnavailable, "this lab was built without the flows program, so it has no scenarios to run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(l.flows)
}

// The dev accounts are two account chains, and a chain takes one block at a
// time. A scenario owns both for as long as it runs, so nothing else that
// signs with them may start while one does.
func (l *lab) scenarioRunning() *run {
	for _, r := range l.runs {
		if r.Kind != "cli" && r.live() {
			return r
		}
	}
	return nil
}

func (l *lab) cliRunning() int {
	n := 0
	for _, r := range l.runs {
		if r.Kind == "cli" && r.live() {
			n++
		}
	}
	return n
}

const keepRuns = 40

// forget drops the oldest finished runs. One that is still running stays
// however old it is: it is what scenarioRunning and cliRunning count, and
// what a DELETE has to find.
func (l *lab) forget() {
	for i := 0; len(l.runs) > keepRuns && i < len(l.runs); {
		if l.runs[i].live() {
			i++
			continue
		}
		l.runs = append(l.runs[:i], l.runs[i+1:]...)
	}
}

// A CLI command is a process, and ptlc.monitor and autoreceive are processes
// that wait. Without a count and a clock, whoever can reach the page can fill
// the container with them.
const (
	maxCLI        = 4
	cliLimit      = 20 * time.Minute
	scenarioLimit = 2 * time.Hour    // the refund path waits out an expiry of up to an hour
	flowLimit     = 15 * time.Minute // the longest flow waits out four minutes of deadlines
)

// A refusal that is the caller's to act on, with the status that says which.
type refusal struct {
	status int
	msg    string
}

func (e *refusal) Error() string { return e.msg }

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func (l *lab) start(kind, flow, title string, argv []string, extraEnv []string) (*run, error) {
	l.mu.Lock()
	if busy := l.scenarioRunning(); busy != nil {
		l.mu.Unlock()
		return nil, &refusal{http.StatusConflict, busy.Title + " is still running and holds both dev accounts; wait for it or stop it"}
	}
	limit := scenarioLimit
	if kind == "flow" {
		limit = flowLimit
	}
	if kind == "cli" {
		limit = cliLimit
		if l.cliRunning() >= maxCLI {
			l.mu.Unlock()
			return nil, &refusal{http.StatusTooManyRequests, fmt.Sprintf("%d commands are already running, which is as many as the lab runs at once; wait for one or stop one", maxCLI)}
		}
	}
	l.seq++
	// A CLI run is one visitor's: what they typed, and what it printed, which
	// for ptlc.keygen is a secret. Its id is not guessable and it is left out
	// of the list, so it reaches only the page that started it. A scenario run
	// is everyone's, and keeps a number.
	id := strconv.Itoa(l.seq)
	if kind == "cli" {
		id = "c" + randomHex(12)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	r := &run{
		ID: id, Kind: kind, Flow: flow, Title: title,
		Command: redact(argv, l.cfg.KeystorePass),
		Started: time.Now().UnixMilli(), wake: make(chan struct{}), cancel: cancel,
	}
	l.runs = append(l.runs, r)
	l.forget()
	l.mu.Unlock()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	ownGroup(cmd)
	cmd.Env = append(os.Environ(), extraEnv...)

	// Nothing here is interactive. A program that asks a question gets end of
	// file and has to decide without an answer.
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		r.add("lab", "could not start: "+err.Error())
		r.finish(-1)
		return r, nil
	}

	var readers sync.WaitGroup
	pump := func(stream string, src io.Reader) {
		defer readers.Done()
		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			r.add(stream, ansi.ReplaceAllString(strings.TrimRight(sc.Text(), "\r"), ""))
		}
	}
	readers.Add(2)
	go pump("out", stdout)
	go pump("err", stderr)
	go func() {
		readers.Wait()
		code := 0
		if err := cmd.Wait(); err != nil {
			code = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				r.add("lab", fmt.Sprintf("stopped after %s, the longest the lab lets this run", limit))
			case ctx.Err() != nil:
				r.add("lab", "stopped")
			}
		}
		cancel()
		r.finish(code)
	}()
	return r, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func redact(argv []string, secret string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if i > 0 && argv[i-1] == "-p" && a == secret {
			a = "***"
		}
		out[i] = a
	}
	return out
}

func (l *lab) find(id string) *run {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// ---- requests ----------------------------------------------------------------

type startRequest struct {
	Kind string `json:"kind"`

	// flow: the name of one scenario of the flows program, and nothing else.
	// Its amounts, keys and deadlines are the program's own.
	Flow string `json:"flow"`

	// cli
	Index int      `json:"index"`
	Args  []string `json:"args"`

	// nom-to-btc
	Scenario string `json:"scenario"`
	Sats     int64  `json:"sats"`
	Znn      int64  `json:"znn"`
	Expiry   int64  `json:"expiry"`
	Locktime int64  `json:"locktime"`

	// nom-to-nom
	ZnnAmount int64 `json:"znnAmount"`
	QsrAmount int64 `json:"qsrAmount"`
}

var cliCommand = regexp.MustCompile(`^[a-zA-Z]+(\.[a-zA-Z0-9]+)?$`)

// The wallet commands that only read. The others make or replace a keystore,
// which is the lab's to set, and wallet.export writes the mnemonic to whatever
// path it is given, as root in the lab's container.
var cliWalletReads = map[string]bool{"wallet.list": true, "wallet.deriveaddresses": true, "wallet.dumpmnemonic": true}

func (l *lab) command(req startRequest) (title string, argv, extraEnv []string, err error) {
	c := l.cfg
	switch req.Kind {
	case "cli":
		if len(req.Args) == 0 || !cliCommand.MatchString(req.Args[0]) {
			return "", nil, nil, errors.New("the first argument is a znn-cli command, such as ptlc.get")
		}
		if len(req.Args) > 16 {
			return "", nil, nil, errors.New("too many arguments")
		}
		if name := strings.ToLower(req.Args[0]); strings.HasPrefix(name, "wallet.") && !cliWalletReads[name] {
			return "", nil, nil, fmt.Errorf("%s writes a keystore or a file, and the lab's CLI is given its keystore; wallet.list, wallet.deriveAddresses and wallet.dumpMnemonic are the wallet commands it runs", req.Args[0])
		}
		for _, a := range req.Args {
			// The connection and the keystore are the lab's to set. An
			// argument that looks like an option would move one of them.
			if strings.HasPrefix(a, "-") || len(a) > 512 {
				return "", nil, nil, fmt.Errorf("argument %q is not accepted here", a)
			}
		}
		if req.Index < 0 || req.Index > 255 {
			return "", nil, nil, errors.New("the account index is between 0 and 255")
		}
		argv = append([]string{c.CLI, "-u", c.ZnndWS, "-c", c.ChainID, "-k", c.Keystore, "-p", c.KeystorePass,
			"-i", strconv.Itoa(req.Index)}, req.Args...)
		return "znn-cli " + strings.Join(req.Args, " "), argv, nil, nil

	case "nom-to-btc":
		if req.Scenario != "happy" && req.Scenario != "refund" {
			return "", nil, nil, errors.New("scenario is happy or refund")
		}
		argv = []string{c.NomToBtc, "-nom", c.ZnndHTTP, "-btc", c.BtcRPC, "-scenario", req.Scenario}
		for _, opt := range []struct {
			flag      string
			v, lo, hi int64
		}{
			{"-sats", req.Sats, 10_000, 100_000_000},
			{"-znn", req.Znn, 1, 1000},
			{"-expiry", req.Expiry, 120, 3600},
			{"-locktime", req.Locktime, 5, 500},
		} {
			if opt.v == 0 {
				continue
			}
			if opt.v < opt.lo || opt.v > opt.hi {
				return "", nil, nil, fmt.Errorf("%s is between %d and %d", opt.flag, opt.lo, opt.hi)
			}
			argv = append(argv, opt.flag, strconv.FormatInt(opt.v, 10))
		}
		return "NoM-to-BTC, " + req.Scenario + " path", argv, nil, nil

	case "nom-to-nom":
		extraEnv = []string{"CLI=" + c.CLI, "URL=" + c.ZnndWS, "CHAIN=" + c.ChainID,
			"KEYSTORE=" + c.Keystore, "PASS=" + c.KeystorePass}
		for name, v := range map[string]int64{"ZNN_AMOUNT": req.ZnnAmount, "QSR_AMOUNT": req.QsrAmount} {
			if v == 0 {
				continue
			}
			if v < 1 || v > 1000 {
				return "", nil, nil, fmt.Errorf("%s is between 1 and 1000", name)
			}
			extraEnv = append(extraEnv, name+"="+strconv.FormatInt(v, 10))
		}
		return "NoM-to-NoM through znn-cli", []string{"bash", c.NomToNom}, extraEnv, nil

	case "flow":
		// The name is looked up, never passed through: what reaches the
		// command line is one of the program's own scenario ids.
		title, ok := l.flowTitles[req.Flow]
		if !ok {
			return "", nil, nil, fmt.Errorf("%q is not a scenario; GET /api/flows lists them", req.Flow)
		}
		return title, []string{c.Flows, "-nom", c.ZnndHTTP, "-btc", c.BtcRPC, "-swap", c.NomToBtc, "-scenario", req.Flow}, nil, nil
	}
	return "", nil, nil, errors.New("kind is cli, flow, nom-to-nom or nom-to-btc")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (l *lab) handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		l.mu.Lock()
		list := make([]map[string]any, 0, len(l.runs))
		for i := len(l.runs) - 1; i >= 0; i-- {
			if l.runs[i].Kind == "cli" {
				continue
			}
			list = append(list, l.runs[i].summary())
		}
		busy := l.scenarioRunning()
		l.mu.Unlock()
		out := map[string]any{"runs": list}
		if busy != nil {
			out["busy"] = map[string]string{"id": busy.ID, "title": busy.Title}
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req startRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "the request is not JSON: "+err.Error())
			return
		}
		title, argv, extraEnv, err := l.command(req)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		flow := ""
		if req.Kind == "flow" {
			flow = req.Flow
		}
		run, err := l.start(req.Kind, flow, title, argv, extraEnv)
		if err != nil {
			status := http.StatusInternalServerError
			var refused *refusal
			if errors.As(err, &refused) {
				status = refused.status
			}
			fail(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, run.summary())
	default:
		fail(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (l *lab) handleRun(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	id, tail, _ := strings.Cut(rest, "/")
	run := l.find(id)
	if run == nil {
		fail(w, http.StatusNotFound, "no such run; labd keeps the last "+strconv.Itoa(keepRuns)+" and forgets them when it restarts")
		return
	}
	switch {
	case tail == "" && r.Method == http.MethodGet:
		lines, _, _ := run.since(0)
		out := run.summary()
		out["output"] = lines
		writeJSON(w, http.StatusOK, out)
	case tail == "" && r.Method == http.MethodDelete:
		run.cancel()
		writeJSON(w, http.StatusOK, run.summary())
	case tail == "stream" && r.Method == http.MethodGet:
		l.stream(w, r, run)
	default:
		fail(w, http.StatusMethodNotAllowed, "GET /api/runs/{id}, GET /api/runs/{id}/stream or DELETE /api/runs/{id}")
	}
}

// stream replays a run from ?from=N and then follows it, as server-sent
// events. EventSource reconnects by itself and sends the last id it saw, so a
// dropped connection resumes where it stopped instead of printing the run twice.
func (l *lab) stream(w http.ResponseWriter, r *http.Request, run *run) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "this connection cannot stream")
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		if n, err := strconv.Atoi(last); err == nil {
			from = n + 1
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		lines, exit, wake := run.since(from)
		for _, ln := range lines {
			body, _ := json.Marshal(ln)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ln.N, body)
			from = ln.N + 1
		}
		if exit != nil {
			fmt.Fprintf(w, "event: done\ndata: {\"exit\":%d}\n\n", *exit)
			flusher.Flush()
			return
		}
		flusher.Flush()
		select {
		case <-wake:
		case <-keepalive.C:
			fmt.Fprint(w, ": still running\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// The calls the page may make to bitcoind: reads, and mining on regtest, which
// is how a block gets made there at all.
var btcMethods = map[string]bool{
	"getblockchaininfo": true, "getblockcount": true, "getblockhash": true, "getblockheader": true,
	"getrawtransaction": true, "gettxout": true, "getmempoolinfo": true, "getrawmempool": true,
	"decoderawtransaction": true, "generatetoaddress": true,
}

func (l *lab) handleBtc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "the request is not JSON: "+err.Error())
		return
	}
	if !btcMethods[req.Method] {
		fail(w, http.StatusForbidden, req.Method+" is not one of the bitcoind calls the lab forwards")
		return
	}
	if req.Method == "generatetoaddress" {
		// Nothing on the page mines; nom-to-btc mines with its own credentials.
		// Under the public name anybody could, 110 blocks a call and with no
		// bound on the calls, and every block stays on the lab's disk. On
		// loopback it is the operator's.
		if l.cfg.PublicHost != "" && hostOnly(r.Host) == l.cfg.PublicHost {
			fail(w, http.StatusForbidden, "generatetoaddress is not offered under the public name; the lab mines what its swaps need by itself")
			return
		}
		// Blocks and an address, and no more blocks than a coinbase needs to
		// mature and a few over. Asked for a million, regtest mines a million.
		var blocks int64
		if len(req.Params) != 2 || json.Unmarshal(req.Params[0], &blocks) != nil || blocks < 1 || blocks > 110 {
			fail(w, http.StatusBadRequest, "generatetoaddress takes [blocks, address] here, with 1 to 110 blocks")
			return
		}
	}
	if req.Params == nil {
		req.Params = []json.RawMessage{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "id": "lab", "method": req.Method, "params": req.Params})
	out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+l.cfg.BtcRPC+"/", bytes.NewReader(body))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.SetBasicAuth(l.cfg.BtcUser, l.cfg.BtcPass)
	out.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(out)
	if err != nil {
		fail(w, http.StatusBadGateway, "bitcoind did not answer at "+l.cfg.BtcRPC+": "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8*1024*1024))
}

func found(name string) bool {
	if filepath.IsAbs(name) {
		_, err := os.Stat(name)
		return err == nil
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func (l *lab) handleConfig(w http.ResponseWriter, r *http.Request) {
	c := l.cfg
	walletNode, nodeHTTP := c.ZnndWSPublic, c.ZnndHTTPHost
	public := c.PublicHost != "" && hostOnly(r.Host) == c.PublicHost
	if public {
		walletNode, nodeHTTP = c.PublicWS, c.PublicRPC
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chainId":      c.ChainID,
		"public":       public,
		"walletNode":   walletNode,
		"nodeHttp":     nodeHTTP,
		"cliNode":      c.ZnndWS,
		"keystore":     c.Keystore,
		"bitcoind":     c.BtcRPC,
		"cli":          found(c.CLI),
		"nomToBtc":     found(c.NomToBtc),
		"nomToNom":     found(c.NomToNom),
		"flows":        l.flows != nil,
		"goZenonBase":  os.Getenv("PTLC_GO_ZENON_BASE"),
		"dartSdkBase":  os.Getenv("PTLC_DART_SDK_BASE"),
		"dartCliBase":  os.Getenv("PTLC_DART_CLI_BASE"),
		"serverTimeMs": time.Now().UnixMilli(),
	})
}

func hostOnly(host string) string {
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// sameOrigin refuses a request a page on another origin sent. labd runs
// programs, and a browser will happily post to loopback on behalf of any site
// the person has open. A request with no Origin did not come from a page.
func sameOrigin(publicHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				fail(w, http.StatusForbidden, "this lab answers only its own page")
				return
			}
		}
		// The Host is checked too, so that a name pointed at loopback by
		// somebody else's DNS is not taken for this page.
		host := hostOnly(r.Host)
		if allowed := os.Getenv("LAB_HOSTS"); allowed != "*" {
			ok := host == "localhost" || host == "127.0.0.1" || host == "[::1]" || (publicHost != "" && host == publicHost)
			for _, h := range strings.Split(allowed, ",") {
				if h != "" && h == host {
					ok = true
				}
			}
			if !ok {
				fail(w, http.StatusForbidden, "open the lab as localhost or 127.0.0.1, or name this host in LAB_HOSTS or LAB_PUBLIC_URL")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (l *lab) static() http.Handler {
	root := l.cfg.WebRoot
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if root == "" {
			fail(w, http.StatusNotFound, "labd was started without LAB_WEB_ROOT; run the page with `npm run dev` in lab/web")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		path := filepath.Join(root, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			// A directory with a page of its own is that page: /scenarios/.
			// Asked for without the slash it is sent to the one with, so that
			// what the page names relative to itself resolves.
			if own := filepath.Join(path, "index.html"); path != filepath.Clean(root) && fileExists(own) {
				if !strings.HasSuffix(r.URL.Path, "/") {
					http.Redirect(w, r, r.URL.Path+"/", http.StatusPermanentRedirect)
					return
				}
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, own)
				return
			}
		}
		if err != nil || info.IsDir() {
			// The console is one page, routed by its fragment; anything
			// unknown is that page.
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func main() {
	cfg := loadConfig()
	l := &lab{cfg: cfg}
	if err := l.loadFlows(); err != nil {
		log.Printf("no scenarios page: %v", err)
	}

	node, err := url.Parse(cfg.ZnndHTTP)
	if err != nil {
		log.Fatalf("LAB_ZNND_HTTP: %v", err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(node)
			p.Out.URL.Path = "/"
			p.Out.Host = node.Host
			// The node answers a browser's Origin with CORS headers of its
			// own; this is one origin, so there is nothing for it to answer.
			p.Out.Header.Del("Origin")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			fail(w, http.StatusBadGateway, "the node did not answer at "+cfg.ZnndHTTP+": "+err.Error())
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/rpc", http.MaxBytesHandler(proxy, 1024*1024))
	mux.HandleFunc("/api/config", l.handleConfig)
	mux.HandleFunc("/api/flows", l.handleFlows)
	mux.HandleFunc("/api/runs", l.handleRuns)
	mux.HandleFunc("/api/runs/", l.handleRun)
	mux.HandleFunc("/api/btc", l.handleBtc)
	mux.Handle("/", l.static())

	srv := &http.Server{Addr: cfg.Listen, Handler: sameOrigin(cfg.PublicHost, mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("labd on http://%s  node %s  bitcoind %s  page %q  published as %q", cfg.Listen, cfg.ZnndHTTP, cfg.BtcRPC, cfg.WebRoot, cfg.PublicHost)
	log.Fatal(srv.ListenAndServe())
}
