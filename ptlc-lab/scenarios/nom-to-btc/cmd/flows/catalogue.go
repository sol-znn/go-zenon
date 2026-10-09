package main

// The scenarios, as data: what each one shows and the steps it will take, in
// the order it takes them. The page draws a scenario from this before it runs,
// so the steps here are a promise the run has to keep: a step the run never
// reports is a failed run (runner.finish), and a call whose answer differs from
// Expect is a failed step.
//
// The words are for someone who has not read the contract. A lock's "point" is
// "a lock", its witness "the secret" or "a signature", a momentum "the chain".

const (
	alice    = "alice"
	bob      = "bob"
	carol    = "carol"
	contract = "contract"
	bitcoin  = "bitcoin"
)

// What a step is, which decides how it is drawn.
const (
	kindLocal   = "local"   // something a party does alone: make a secret, sign
	kindMessage = "message" // something one party hands another, off the chain
	kindCall    = "call"    // a call to the contract, answered a moment later
	kindPay     = "pay"     // funds the contract sent while answering
	kindWait    = "wait"    // a deadline on the chain's clock
	kindRead    = "read"    // a party reading the chain
	kindBitcoin = "bitcoin" // a Bitcoin transaction
)

// What a call is expected to meet.
const (
	accepted = "accepted" // the contract does it
	refused  = "refused"  // the contract answers no, and moves nothing
	rejected = "rejected" // the network will not take the block at all
)

type step struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Label  string `json:"label"`
	Expect string `json:"expect,omitempty"`

	// Because is what the node has to say when it turns a block away, for a
	// step that expects that. Any error at all used to count, and a node that
	// was down, or a race for the account's next height, passed for the rule
	// the step is there to show.
	Because string `json:"-"`
}

type scenario struct {
	ID      string   `json:"id"`
	Group   string   `json:"group"`
	Title   string   `json:"title"`
	Shows   string   `json:"shows"`
	Parties []string `json:"parties"`
	Seconds int      `json:"seconds"` // about how long it takes, for the page to say
	Steps   []step   `json:"steps"`

	run func(*runner) (string, error)
}

type group struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Lede  string `json:"lede"`
}

var groups = []group{
	{"locks", "Three ways to lock funds", "Funds go into the contract and come out for whoever can open the lock. There are three kinds of lock."},
	{"safety", "What the contract refuses", "A lock is only useful if nobody else can open it. Each of these tries something that must not work, and passes when it does not."},
	{"time", "Deadlines", "Every lock has a deadline. Before it, only the right secret or signature opens the lock. After it, only the owner can take the funds back."},
	{"swaps", "Swaps without a middleman", "Two people each lock funds for the other. Opening one lock is what makes the other one openable, so either both get paid or both get their funds back. That holds only in one order: whoever holds the secret locks first, and their lock lasts longer. The last swap here does it without the secret ever being shown."},
	{"mistakes", "Mistakes the contract cannot stop", "The contract does exactly what each call asks. These are ways to lose funds anyway, by asking in the wrong order, too late, or for a lock the contract does not look at closely enough. The lab's own clients refuse each of them."},
	{"bitcoin", "Bitcoin", "The same lock, with real Bitcoin transactions on the other side. This is the swap the zBTC proof of concept is built on."},
}

func call(id, from, label, expect string) step {
	return step{ID: id, Kind: kindCall, From: from, To: contract, Label: label, Expect: expect}
}

// turnedAway is a call the node must refuse to take at all, for the reason
// given: the error text it has to contain.
func turnedAway(id, from, label, because string) step {
	return step{ID: id, Kind: kindCall, From: from, To: contract, Label: label, Expect: rejected, Because: because}
}
func pay(id, to, label string) step {
	return step{ID: id, Kind: kindPay, From: contract, To: to, Label: label}
}
func local(id, who, label string) step { return step{ID: id, Kind: kindLocal, From: who, Label: label} }
func message(id, from, to, label string) step {
	return step{ID: id, Kind: kindMessage, From: from, To: to, Label: label}
}
func wait(id, label string) step { return step{ID: id, Kind: kindWait, From: contract, Label: label} }
func read(id, who, label string) step {
	return step{ID: id, Kind: kindRead, From: contract, To: who, Label: label}
}
func onBitcoin(id, from, label string) step {
	return step{ID: id, Kind: kindBitcoin, From: from, To: bitcoin, Label: label}
}

var scenarios = []*scenario{
	// ---- locks ---------------------------------------------------------------
	{
		ID: "secret", Group: "locks", Title: "Unlock with a secret",
		Shows:   "Alice locks 1 ZNN for Bob. Bob opens the lock with a secret number and is paid.",
		Parties: []string{alice, bob, contract}, Seconds: 60,
		Steps: []step{
			local("secret", alice, "Alice makes a secret and the lock that fits it"),
			call("lock", alice, "Alice locks 1 ZNN for Bob", accepted),
			message("tell", alice, bob, "Alice gives Bob the secret"),
			call("unlock", bob, "Bob unlocks with the secret", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runSecret,
	},
	{
		ID: "wallet-key", Group: "locks", Title: "Unlock with a Zenon wallet signature",
		Shows:   "Alice locks 1 ZNN to Bob's own wallet key. Bob signs with it and is paid. No secret ever appears on the chain.",
		Parties: []string{alice, bob, contract}, Seconds: 60,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN to Bob's wallet key", accepted),
			local("sign", bob, "Bob signs \"pay Bob\" with his wallet key"),
			call("unlock", bob, "Bob unlocks with the signature", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runWalletKey,
	},
	{
		ID: "bitcoin-key", Group: "locks", Title: "Unlock with a Bitcoin-style signature",
		Shows:   "The lock is a Bitcoin Taproot key. A signature Bitcoin itself would accept opens it, which is what lets a lock here be tied to one on Bitcoin.",
		Parties: []string{alice, bob, contract}, Seconds: 60,
		Steps: []step{
			local("key", bob, "Bob makes a Bitcoin-style key"),
			call("lock", alice, "Alice locks 1 ZNN to that key", accepted),
			local("sign", bob, "Bob signs \"pay Bob\" with the key"),
			call("unlock", bob, "Bob unlocks with the signature", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runBitcoinKey,
	},
	{
		ID: "relay", Group: "locks", Title: "Someone else delivers the unlock",
		Shows:   "Bob signs but does not send anything himself. Carol delivers his signature. The 1 ZNN goes to Bob, because that is who the signature names.",
		Parties: []string{alice, bob, carol, contract}, Seconds: 60,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN to Bob's key", accepted),
			local("sign", bob, "Bob signs \"pay Bob\""),
			message("hand", bob, carol, "Bob hands Carol the signature"),
			call("deliver", carol, "Carol delivers it", accepted),
			pay("paid", bob, "Bob receives 1 ZNN, Carol nothing"),
		},
		run: runRelay,
	},

	// ---- safety --------------------------------------------------------------
	{
		ID: "wrong-secret", Group: "safety", Title: "A wrong secret is refused",
		Shows:   "Bob tries a wrong secret and gets nothing. The lock stays shut and still opens with the right one.",
		Parties: []string{alice, bob, contract}, Seconds: 90,
		Steps: []step{
			local("secret", alice, "Alice makes a secret and its lock"),
			call("lock", alice, "Alice locks 1 ZNN for Bob", accepted),
			call("wrong", bob, "Bob tries a wrong secret", refused),
			call("right", bob, "Bob tries the right one", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runWrongSecret,
	},
	{
		ID: "stolen-signature", Group: "safety", Title: "A copied signature pays only its owner",
		Shows:   "Carol copies Bob's signature and asks to be paid herself. It is refused: the signature says who gets the funds.",
		Parties: []string{alice, bob, carol, contract}, Seconds: 90,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN to Bob's key", accepted),
			local("sign", bob, "Bob signs \"pay Bob\""),
			call("steal", carol, "Carol sends it as \"pay Carol\"", refused),
			call("unlock", bob, "Bob unlocks with his signature", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runStolenSignature,
	},
	{
		ID: "front-run", Group: "safety", Title: "A stolen secret is useless",
		Shows:   "A secret becomes public the moment it is used. So a secret lock must name who gets paid, and it pays nobody else, even someone who knows the secret.",
		Parties: []string{alice, bob, carol, contract}, Seconds: 90,
		Steps: []step{
			turnedAway("open", alice, "Alice tries a secret lock that names nobody", "invalid destination"),
			call("lock", alice, "Alice locks 1 ZNN for Bob, by name", accepted),
			call("snoop", carol, "Carol knows the secret and tries to take it", refused),
			call("unlock", bob, "Bob unlocks with the secret", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runFrontRun,
	},
	{
		ID: "once", Group: "safety", Title: "A lock pays once",
		Shows:   "After Bob is paid, the same secret sent again does nothing. The entry is gone.",
		Parties: []string{alice, bob, contract}, Seconds: 90,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN for Bob", accepted),
			call("unlock", bob, "Bob unlocks and is paid", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
			call("again", bob, "Bob sends the same unlock again", refused),
		},
		run: runOnce,
	},
	{
		ID: "bad-locks", Group: "safety", Title: "Broken locks are turned away",
		Shows:   "A lock nobody could ever open, a kind of lock the contract does not have, and a lock with nothing in it are not taken, so nothing leaves the account. A lock whose deadline has already passed is refused and refunded.",
		Parties: []string{alice, contract}, Seconds: 45,
		Steps: []step{
			turnedAway("impossible", alice, "Alice tries a lock to a key that cannot exist", "invalid point lock"),
			turnedAway("no-type", alice, "Alice tries a kind of lock that does not exist", "invalid point type"),
			turnedAway("empty", alice, "Alice tries a lock with nothing in it", "invalid token or amount"),
			call("late", alice, "Alice tries a lock whose deadline has passed", refused),
			pay("refund", alice, "The 1 ZNN comes straight back"),
		},
		run: runBadLocks,
	},
	{
		ID: "signed-elsewhere", Group: "safety", Title: "A signature made for somewhere else is refused",
		Shows:   "Bob signs \"pay Bob\" for another Zenon network, and for another contract on this one. Both signatures are real and both are refused, because what Bob signs names the network and the contract. The one he makes for this lock pays him.",
		Parties: []string{alice, bob, contract}, Seconds: 110,
		Steps: []step{
			local("key", bob, "Bob makes a Bitcoin-style key"),
			call("lock", alice, "Alice locks 1 ZNN to that key", accepted),
			call("other-chain", bob, "Bob unlocks with a signature made for another network", refused),
			call("other-contract", bob, "Bob unlocks with a signature made for another contract", refused),
			call("unlock", bob, "Bob unlocks with the one made for this lock", accepted),
			pay("paid", bob, "Bob receives 1 ZNN"),
		},
		run: runSignedElsewhere,
	},

	// ---- time ----------------------------------------------------------------
	{
		ID: "deadline", Group: "time", Title: "After the deadline, the owner takes it back",
		Shows:   "Before the deadline Alice cannot take her funds back. After it Bob can no longer unlock, a stranger cannot claim, and Alice can.",
		Parties: []string{alice, bob, carol, contract}, Seconds: 180,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN for Bob, for 100 seconds", accepted),
			call("early", alice, "Alice asks for it back early", refused),
			wait("wait", "The deadline passes"),
			call("toolate", bob, "Bob unlocks with the right secret, too late", refused),
			call("stranger", carol, "Carol asks for Alice's funds", refused),
			call("reclaim", alice, "Alice asks for it back", accepted),
			pay("back", alice, "Alice receives her 1 ZNN"),
		},
		run: runDeadline,
	},

	// ---- swaps ---------------------------------------------------------------
	{
		ID: "swap", Group: "swaps", Title: "Swap ZNN for QSR",
		Shows:   "Alice and Bob trade 10 QSR for 1 ZNN. Alice holds the secret, so she locks first and her lock lasts five minutes longer. Her taking the ZNN makes the secret public, and that is what lets Bob take the QSR. The two locks look unrelated on the chain.",
		Parties: []string{alice, bob, contract}, Seconds: 150,
		Steps: []step{
			message("agree", alice, bob, "Alice sends Bob a lock only she can open"),
			call("alice-lock", alice, "Alice locks 10 QSR for Bob, for 10 minutes", accepted),
			read("bob-check", bob, "Bob checks Alice's lock on the chain"),
			call("bob-lock", bob, "Bob locks 1 ZNN for Alice, ending 5 minutes before hers", accepted),
			call("alice-unlock", alice, "Alice takes the ZNN, which reveals her secret", accepted),
			pay("alice-paid", alice, "Alice receives 1 ZNN"),
			read("bob-read", bob, "Bob reads the secret from the chain"),
			call("bob-unlock", bob, "Bob takes the QSR", accepted),
			pay("bob-paid", bob, "Bob receives 10 QSR"),
		},
		run: runSwap,
	},
	{
		ID: "swap-abandoned", Group: "swaps", Title: "A swap that stalls is undone",
		Shows:   "Both lock, then Alice walks away. Nobody loses anything: after the deadlines each takes their own funds back, Bob first, because his lock was the shorter one.",
		Parties: []string{alice, bob, contract}, Seconds: 240,
		Steps: []step{
			message("agree", alice, bob, "Alice sends Bob a lock only she can open"),
			call("alice-lock", alice, "Alice locks 10 QSR for Bob", accepted),
			call("bob-lock", bob, "Bob locks 1 ZNN for Alice, with the earlier deadline", accepted),
			local("stall", alice, "Alice walks away and never unlocks"),
			wait("wait-bob", "Bob's deadline passes"),
			call("bob-reclaim", bob, "Bob takes his ZNN back", accepted),
			pay("bob-back", bob, "Bob receives his 1 ZNN"),
			wait("wait-alice", "Alice's deadline passes"),
			call("alice-reclaim", alice, "Alice takes her QSR back", accepted),
			pay("alice-back", alice, "Alice receives her 10 QSR"),
		},
		run: runSwapAbandoned,
	},
	{
		ID: "swap-key", Group: "swaps", Title: "Swap with no secret on the chain",
		Shows:   "The same trade on another kind of lock. Each locks to a fresh key of their own and names the other as the only one who can be paid. Each gives the other a signature with Alice's secret left out. Alice adds the secret to take the ZNN, and her finished signature is what tells Bob the secret. The chain shows two ordinary signatures and never the secret.",
		Parties: []string{alice, bob, contract}, Seconds: 150,
		Steps: []step{
			message("agree", alice, bob, "Alice and Bob each make a fresh key and agree the trade"),
			call("alice-lock", alice, "Alice locks 10 QSR to her key, payable only to Bob, for 10 minutes", accepted),
			message("alice-presign", alice, bob, "Alice gives Bob a half-finished signature for her lock"),
			read("bob-check", bob, "Bob checks her lock and that signature"),
			call("bob-lock", bob, "Bob locks 1 ZNN to his key, payable only to Alice, ending 5 minutes sooner", accepted),
			message("bob-presign", bob, alice, "Bob gives Alice a half-finished signature for his lock"),
			read("alice-check", alice, "Alice checks his lock and that signature"),
			call("alice-unlock", alice, "Alice finishes it with her secret and takes the ZNN", accepted),
			pay("alice-paid", alice, "Alice receives 1 ZNN"),
			read("bob-read", bob, "Bob works the secret out from her finished signature"),
			call("bob-unlock", bob, "Bob finishes hers with it and takes the QSR", accepted),
			pay("bob-paid", bob, "Bob receives 10 QSR"),
			read("hidden", bob, "The secret is nowhere on the chain"),
		},
		run: runKeySwap,
	},

	// ---- mistakes ------------------------------------------------------------
	{
		ID: "wrong-order", Group: "mistakes", Title: "Locking first without the secret",
		Shows:   "Bob locks first, though it is Alice who holds the secret. She takes his ZNN at once and never locks anything. Bob reads her secret and has nothing to open with it. Every call was valid; the order was the mistake, and the swap above does not make it.",
		Parties: []string{alice, bob, contract}, Seconds: 75,
		Steps: []step{
			message("agree", alice, bob, "Alice sends Bob a lock only she can open"),
			call("bob-lock", bob, "Bob locks 1 ZNN for Alice, first", accepted),
			call("alice-take", alice, "Alice takes the ZNN at once", accepted),
			pay("alice-paid", alice, "Alice receives 1 ZNN"),
			local("walk", alice, "Alice never locks her QSR"),
			read("bob-read", bob, "Bob reads the secret, with nothing to open"),
		},
		run: runWrongOrder,
	},
	{
		ID: "late-reveal", Group: "mistakes", Title: "Showing the secret too late",
		Shows:   "The swap in the right order, but Alice waits until Bob's deadline has passed before taking his ZNN. The contract refuses her, and her secret is public all the same: Bob takes his ZNN back and her QSR too. The lab's clients will not unlock with under a minute left.",
		Parties: []string{alice, bob, contract}, Seconds: 230,
		Steps: []step{
			message("agree", alice, bob, "Alice sends Bob a lock only she can open"),
			call("alice-lock", alice, "Alice locks 10 QSR for Bob", accepted),
			call("bob-lock", bob, "Bob locks 1 ZNN for Alice, with the earlier deadline", accepted),
			wait("wait", "Bob's deadline passes"),
			call("alice-late", alice, "Alice tries to take the ZNN, too late", refused),
			read("bob-read", bob, "Bob reads her secret from the refused call"),
			call("bob-reclaim", bob, "Bob takes his ZNN back", accepted),
			pay("bob-back", bob, "Bob receives his 1 ZNN"),
			call("bob-unlock", bob, "Bob takes Alice's QSR with her secret", accepted),
			pay("bob-paid", bob, "Bob receives 10 QSR"),
		},
		run: runLateReveal,
	},
	{
		ID: "swap-key-open", Group: "mistakes", Title: "A key lock that does not say who it pays",
		Shows:   "The swap with no secret on the chain, but Alice's lock names nobody and Bob does not check. The lock's key is hers, so she signs it back to herself, and then takes his ZNN as agreed. Bob works out the secret and finishes a signature for a lock that is gone. The lab's client refuses to lock against a key lock that does not name it.",
		Parties: []string{alice, bob, contract}, Seconds: 170,
		Steps: []step{
			message("agree", alice, bob, "Alice and Bob each make a fresh key and agree the trade"),
			call("alice-lock", alice, "Alice locks 10 QSR to her key, naming nobody", accepted),
			message("alice-presign", alice, bob, "Alice gives Bob a half-finished signature that would pay him"),
			local("bob-skips", bob, "Bob does not check who her lock pays"),
			call("bob-lock", bob, "Bob locks 1 ZNN to his key, payable only to Alice", accepted),
			message("bob-presign", bob, alice, "Bob gives Alice a half-finished signature for his lock"),
			call("alice-back", alice, "Alice signs her own lock back to herself", accepted),
			pay("alice-back-paid", alice, "Alice receives her 10 QSR back"),
			call("alice-unlock", alice, "Alice finishes Bob's signature and takes the ZNN", accepted),
			pay("alice-paid", alice, "Alice receives 1 ZNN"),
			read("bob-read", bob, "Bob works the secret out from her finished signature"),
			call("bob-unlock", bob, "Bob finishes the signature that would have paid him", refused),
		},
		run: runKeySwapOpen,
	},
	{
		ID: "weak-key", Group: "mistakes", Title: "A lock to a key with no secret behind it",
		Shows:   "Alice locks 1 ZNN to a Zenon-style key that is a special point of the curve, one for which a signature anybody can write down checks out. Carol writes one and is paid. The contract checks only the length of this kind of key; the lab's console and CLI refuse it before it is sent.",
		Parties: []string{alice, carol, contract}, Seconds: 60,
		Steps: []step{
			call("lock", alice, "Alice locks 1 ZNN to a key with no secret behind it", accepted),
			local("forge", carol, "Carol writes a signature without any key"),
			call("take", carol, "Carol sends it as \"pay Carol\"", accepted),
			pay("carol-paid", carol, "Carol receives 1 ZNN"),
		},
		run: runWeakKey,
	},

	// ---- bitcoin -------------------------------------------------------------
	{
		ID: "swap-btc", Group: "bitcoin", Title: "Swap bitcoin for ZNN",
		Shows:   "Alice sells 100,000 sats for 10 ZNN. When she takes the ZNN her secret becomes public, and Bob uses it to finish the signature that pays him the bitcoin. On Bitcoin it looks like any other payment.",
		Parties: []string{alice, bob, contract, bitcoin}, Seconds: 90,
		Steps: []step{
			onBitcoin("btc-lock", alice, "Alice locks 100,000 sats on Bitcoin"),
			message("presign", alice, bob, "Alice gives Bob a half-finished signature for the bitcoin"),
			call("znn-lock", bob, "Bob locks 10 ZNN for Alice", accepted),
			call("reveal", alice, "Alice takes the ZNN, which reveals her secret", accepted),
			pay("znn-paid", alice, "Alice receives 10 ZNN"),
			read("read", bob, "Bob reads the secret from the chain"),
			onBitcoin("btc-claim", bob, "Bob finishes the signature and takes the bitcoin"),
		},
		run: runBitcoinSwap("happy"),
	},
	{
		ID: "swap-btc-refund", Group: "bitcoin", Title: "A bitcoin swap that stalls is refunded",
		Shows:   "Both sides lock, then Alice never takes the ZNN. After the deadlines Alice takes her bitcoin back on Bitcoin and Bob takes his ZNN back here.",
		Parties: []string{alice, bob, contract, bitcoin}, Seconds: 240,
		Steps: []step{
			onBitcoin("btc-lock", alice, "Alice locks 100,000 sats on Bitcoin"),
			message("presign", alice, bob, "Alice gives Bob a half-finished signature for the bitcoin"),
			call("znn-lock", bob, "Bob locks 10 ZNN for Alice", accepted),
			local("stall", alice, "Alice walks away and never unlocks"),
			onBitcoin("btc-refund", alice, "Alice takes her bitcoin back after the deadline"),
			call("znn-reclaim", bob, "Bob takes his ZNN back after the deadline", accepted),
		},
		run: runBitcoinSwap("refund"),
	},
	{
		ID: "swap-btc-bad-terms", Group: "bitcoin", Title: "Bob refuses a bitcoin refund that opens too soon",
		Shows:   "Alice proposes a swap whose bitcoin refund opens two blocks from now, while Bob's ZNN would stay open to her for four minutes. She could take the bitcoin back and the ZNN as well, so Bob says no before anything is locked.",
		Parties: []string{alice, bob, contract, bitcoin}, Seconds: 20,
		Steps: []step{
			message("terms", alice, bob, "Alice proposes a refund two blocks out"),
			local("refuse", bob, "Bob refuses, before anything is locked"),
		},
		run: runBitcoinSwap("bad-terms"),
	},
}

func find(id string) *scenario {
	for _, s := range scenarios {
		if s.ID == id {
			return s
		}
	}
	return nil
}
