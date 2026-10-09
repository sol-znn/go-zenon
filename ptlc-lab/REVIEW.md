# Adversarial review, 2026-10-09

**Written 2026-10-09 (UTC)** at the owner's request: "Adversarially review the ptlc
implementation for flaws, mistakes, vulnerabilities, missing features, etc. Then update
the ptlc lab and tooling accordingly."

What was read, as it stood that morning: the contract on `ptlc/release-0.0.9` at
`430fb6d` (`vm/embedded/definition/ptlc.go`, `implementation/ptlc.go`, its RPC and its
wiring), the Dart SDK and CLI branches, the two swap programs and the scenarios page's
runner in [`scenarios/nom-to-btc`](scenarios/nom-to-btc), the CLI swap script in
[`scenarios/nom-to-nom`](scenarios/nom-to-nom), `labd`, the console, the gateway, the
compose files and the end-to-end scripts. The findings a run could show were shown on
the devnet: R-01 to R-05 each have a scenario, a test or a transcript below. The rest
come from reading the code. Every fix was run after it was made.

Back to the [README](README.md). The decisions this produced are D-30 to D-34 in
[DECISIONS.md](DECISIONS.md), D-34 being the owner's call on the one finding in the
contract; the two gaps it leaves are G-17 and G-18 in [GAPS.md](GAPS.md), both
accepted.

---

## Findings

Severity: **critical** means a party loses funds by following the instructions this
repository shipped; **high**, a party loses funds to a counterparty or a stranger the
tooling did not guard against; **medium**, the contract, the lab or its tests can be
used or misled in a way they claim they cannot; **low**, defence in depth or a
statement that was wrong.

| # | Severity | Where | Finding | Done |
|---|---|---|---|---|
| R-01 | critical | SPEC §3, `scenarios/nom-to-nom/run.sh`, `TestPtlc_pointLock_tweakedSwap`, the scenarios page's two NoM swaps, the console's Swap bench | **The NoM-to-NoM swap had its order inverted.** Bob, who does not hold `t`, locked first, and his entry expired last. Alice could claim his entry with `t` at once and never fund hers; or fund hers, let it expire, reclaim it, and then claim Bob's, which was still open. Either way Bob loses his side and learns `t` when there is nothing left to open. The spec's reasoning ("Alice must claim before `E_A`") was false: nothing makes her | The holder of the secret locks first and expires last, everywhere: spec, script, mock test, both page scenarios, the bench and its walk. Two mock-chain tests run the old order losing and a late claim against the new one failing (go-zenon `97c0061`, patch 0012). The page shows the old order losing as a scenario of its own (`wrong-order`). D-30 |
| R-02 | high | `ptlc.unlock`, `ptlc.proxyUnlock`, the Swap and Entries benches, `nom-to-btc` | **A late unlock gives the secret away and pays nothing.** The contract judges an unlock when it answers it, two momentums after the call is sent; the call's witness is public from the moment its block is. An unlock answered after the expiry is refused, and the counterparty reclaims its own side and opens the other with the secret. No client kept a margin; the CLI also judged the expiry by the machine's clock instead of the chain's | 60 s on the chain's clock, refused below it by the CLI and the Swap bench, warned of on the Entries bench, and used by `nom-to-btc` (45 s before). The page shows the cost (`late-reveal`); a mock test shows it too. D-31 |
| R-03 | high | `ptlc.monitor` (Dart CLI) | **The monitor believed the first unlock it found.** It printed the witness of the first `Unlock` or `ProxyUnlock` of the entry on the contract's chain, accepted or refused, and stopped. Anyone can send a refused unlock with a junk witness ahead of the real one. Bob would add `d` to junk, fail to claim, and lose Alice's side at its expiry. A refused `Reclaim` stopped it the same way | Only the call the contract accepted closes the entry; its witness is checked against the lock before it is printed; refused calls are reported as refused and the watch goes on. The Go reader (`ScanWitness`) and the console's fold already filtered on the answer. Found while checking the fix: the monitor decoded every block on the contract's chain as a call, and a payout already received pairs with a block that has no data, on which the SDK's decoder throws, so a monitor with one in its range stopped with an exception, the old one too. It reads only the contract's receive blocks now (CLI `b0a232c`) |
| R-04 | high | `scenarios/nom-to-btc/main.go` | **Bob never weighed Alice's Bitcoin refund height against his NoM expiry.** The comment said he needed to; no code did. With `L` before `E` (plus his claim time), Alice refunds the bitcoin at `L` and still claims the ZNN before `E`. On regtest anyone can mine to `L` in a second | `checkTerms`: at a block every 300 s (twice Bitcoin's rate, the side that hurts Bob) the refund must open at least three blocks after `E`. Refused terms end the swap before anything is locked, shown as `swap-btc-bad-terms`; `TestCheckTerms`. D-32 |
| R-05 | medium | the contract (`checkPointLock`) | **An Ed25519 lock is checked for its length only.** Bytes that encode no point make an entry nothing opens, against the spec's "an entry that can never be opened is never made". A point of small order makes one anything opens: with the identity as the key, the 64 bytes `01 00…00 ‖ 00…00` verify for every message under Go's `crypto/ed25519`, which the contract calls. Run on the devnet on 2026-10-09 at 14:50 UTC: Alice's entry `a919ebcc…` was opened by Carol with that forgery (`a277511e…`), and Carol was paid | The console and the CLI refuse both before sending (vectors shared, tests on both). The page shows the gap (`weak-key`). **The contract is not changed**: it is a consensus rule, and the owner's call on it (T-30), made on 2026-10-09 after the comparison in D-34, is to leave it, as Go's `crypto/ed25519` and Zcash's ZIP-215 do. G-17 is accepted, and the contract's own security notes now say what a client has to refuse (go-zenon `38083f5`, patch 0013) |
| R-06 | medium | the scenarios page's runner (`cmd/flows`) | **A step expected to be turned away passed on any error.** A node that was down, or a race for the account's next height, passed for "the network would not take a lock that names nobody" | Each such step names the error it must meet (`invalid destination`, `invalid point lock`, `invalid point type`, `invalid token or amount`); the catalogue test refuses one that does not |
| R-07 | medium | `cmd/flows` | **A retried call could lock funds twice.** A call that failed was sent again up to three times, including when the node had taken the block and only the answer failed | Before a retry the runner asks the node for the block's hash, and stops if it is there |
| R-08 | medium | `labd`, `/api/btc` | **Anyone could mine regtest through the public name**, 110 blocks a call and any number of calls, every block kept on the lab's disk. Nothing on the page mines; `nom-to-btc` mines with its own credentials | `generatetoaddress` is refused under the public name and still answers on loopback |
| R-09 | medium | `labd`, `/api/runs` | **Every visitor could read every CLI run**, command line and output, by sequential id. `ptlc.keygen` prints a secret; `ptlc.sign` and `ptlc.adaptorSign` take one as an argument | A CLI run's id is 12 random bytes and the list leaves CLI runs out; only the page that started one can follow it. Scenario runs stay listed: the page's tally is everyone's |
| R-10 | low | `labd` | A run's output was held in memory without bound | 20,000 lines, then one line saying the rest was dropped |
| R-11 | low | `nom-to-btc` | Bob locked his ZNN against Alice's funding at one confirmation. On a real chain a reorganisation that takes the funding away leaves his ZNN open to `t` with nothing on Bitcoin for him | `-confirmations`, one on regtest where only the program mines, six named for a real chain |
| R-12 | low | SPEC §1.3, §3, the console, the CLI | A fixed destination that is an embedded contract was said to lose the funds; it does not: the payout fails, the VM rolls the unlock back and the creator reclaims at expiry. An address of the contract type that is not on the embedded list (`z1qyqq…8gqc7n0`, say) passes `Create`, the console's check (which went by the name `z1qxemdeddedx…`) and the CLI's (the SDK's list), and is just as unpayable. §3's opening had the two tokens the wrong way round | Corrected in the spec; the console and the CLI refuse a destination by its type byte (CLI `c8566a8`) |
| R-13 | low | SPEC §1.5, go-zenon `docs/ptlc/SECURITY.md` | "A refused unlock leaves the entry and moves nothing" left out that its witness is public | Both now say so (R-02) |
| R-14 | missing | the scenarios page | T-24's cases were not on it: a signature for another network, for another contract, a lock with nothing in it, a lock type that does not exist | `signed-elsewhere`, and `bad-locks` with two more refusals. The page has nineteen scenarios |

## Checked and sound

Read and, where it could be, run; nothing found:

- the unlock message's binding (domain, chain, contract, point type, entry,
  destination), and a signature for another chain or contract refused on the devnet
  (`signed-elsewhere`);
- one payout per entry: the entry is deleted before the payout block is built;
- the two windows: an unlock at or after the expiry is refused, a reclaim before it is
  refused, and only the creator reclaims;
- a payout the VM cannot deliver (to any contract address) rolls the whole unlock
  back, entry included: no loss, only a wait for the reclaim;
- stored state re-validated before an unlock pays; a reclaim that does not depend on
  the point fields;
- the scalar check (canonical, nonzero, below the order) and the BIP-340 check;
- the adaptor construction in Go and Dart, all four parity cases, against each other;
- the Taproot output's internal key is BIP-341's NUMS point, so Alice has no key path
  to take the bitcoin back early;
- the Go `ScanWitness` and the console's fold take only the accepted unlock;
- `labd`'s command rules (no options, no keystore writes), the gateway's routes, and
  every published port bound to loopback.

## Not changed, and why

- **The contract.** R-05 is the one finding in consensus code, and a fix there is a
  rule change the spork would carry. It was proposed, with what comparable systems do,
  and the owner decided on 2026-10-09 to leave the contract as it is (T-30, D-34): it
  keeps the rule Go's `crypto/ed25519` and Zcash's ZIP-215 have, and the check stays
  in the clients. The go-zenon branch gained tests and a page of its security notes
  and nothing else.
- **The shared devnet accounts.** Anyone can still sign as Alice, Bob and Carol,
  drain them, or race them for a height; stopping a scenario run is still anyone's
  (G-14). Per-visitor accounts are a different lab.
- **The Bitcoin claim's fee** is fixed in the pre-signed transaction; Bob can raise it
  only by a child that spends his output. Fine on regtest, a known cost of a script-path
  leg (G-03).
- **Syrius** still cannot sign an unlock message (G-09).

## How it was checked

All on 2026-10-09, UTC.

| What | Result |
|---|---|
| go-zenon mock chain, `TestPtlc_pointLock_*` on `ptlc/release-0.0.9` and on `ptlc/dev` | pass, including the two new order tests |
| Dart CLI, `test/ptlc_test.dart` | 11 of 11 |
| `scenarios/nom-to-btc`, `go test ./...` | pass, including `TestCheckTerms`, `TestUnlockMessageForIsTheContractsOwn`, `TestIdentityKeyOpensForAnyMessage` |
| `labd`, `go test ./...` | pass, including the three new rules |
| the console, `vitest` | 32 of 32 |
| the image build, which runs the console's, `labd`'s and the runner's tests and checks both patched trees against their pinned hashes | built 15:24:58, `3e1f41782b45` |
| the nine new and changed scenarios, by the runner against the devnet, 14:49 to 15:05 | 9 of 9 ([transcript](lab/e2e/runs/2026-10-09-review-flows-direct.txt)); `weak-key` paid Carol at 14:50, as R-05 says |
| the bench walk, refund path included, on the new page from a local `labd`, 15:05 to 15:16 | 24 of 24 ([transcript](lab/e2e/runs/2026-10-09-review-benches-local.txt)) |
| `ptlc.monitor` before and after the fix, side by side on one entry, a junk unlock sent first, 15:16 to 15:18 | the old one printed the junk as the witness; the fixed one reported the call as refused and printed `t` ([transcript](lab/e2e/runs/2026-10-09-review-monitor.txt)) |
| `scenarios/nom-to-nom/run.sh` in the safe order, on the host and in the image, 15:18 to 15:24 | passed twice, after one stop that was the script's: it read the witness from colour-coded output ([transcript](lab/e2e/runs/2026-10-09-review-nom-to-nom.txt)) |
| the deployed lab through Cloudflare, 15:09 to 15:25 | both pages 200; 19 scenarios listed; `generatetoaddress` refused under the public name while `getblockcount` answers; a CLI run absent from the list and readable by its own id |
| all nineteen on the page through Cloudflare, on the image of 15:24 (`3e1f41782b45`), 15:25 to 15:57 | 18 of 18 passed with every check; the nineteenth, `swap-btc-bad-terms`, passed on the page in one second, before the script looked for it running, and the script stopped there. The script's wait was fixed, and the owner stopped the work at 16:04 before it was run again; its first whole run is three rows down ([transcript](lab/e2e/runs/2026-10-09-review-scenarios-public-1.txt)) |
| the bench walk without its refund path, on the image of 15:41 (`191142aaf67d`), 16:00 to 16:04 | stopped on the owner's word at 15 of 15 checks passed ([transcript](lab/e2e/runs/2026-10-09-review-benches-deployed-stopped.txt)); the whole walk on the last image is the last row |
| the nineteen through Cloudflare with the fixed script, on the image of 15:41 (`191142aaf67d`), 16:31 to 16:43 | the first ten scenarios passed with every check, 32 of 32; stopped there on purpose, because the owner's call on R-05 at about 16:41 changed a sentence of `weak-key`'s verdict and the redeploy that carries it forgets the page's results ([transcript](lab/e2e/runs/2026-10-09-review-scenarios-public-2-stopped.txt)) |
| **all nineteen on the page through Cloudflare, on the image that carries that call** (`fbdd331c5393`, built 16:47:38, deployed 16:48:43), 16:49 to 17:19 | **19 of 19 scenarios, 60 of 60 checks.** `swap-btc-bad-terms`, one second on the page, was followed by the fixed wait; `weak-key`'s verdict reads "The contract leaves this check to wallets"; the page's tally read 19 passed, none failed, none not run, and a reload showed the same ([transcript](lab/e2e/runs/2026-10-09-review-scenarios-public-3.txt)) |
| **the bench walk, whole, refund path included, through Cloudflare on the same image**, 17:19 to 17:30: the first time the benches were walked by the real hostname | **24 of 24 checks**: the swap in the safe order end to end, the key lock, the wrong scalar refused and the right one paid, the CLI's output streamed, and the abandoned swap unwound by two reclaims, Bob's first and Alice's last ([transcript](lab/e2e/runs/2026-10-09-review-benches-public.txt)) |
