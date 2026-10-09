# NoM-to-BTC swap: a point lock on NoM, an adaptor signature on Bitcoin

Alice has regtest bitcoin and wants devnet ZNN; Bob has ZNN and wants bitcoin. Alice
holds a secret `t`. Bob locks ZNN to `T = t·G` for Alice in the ptlc contract; Alice
pre-signs Bob's Bitcoin claim as an adaptor on `T`; her NoM claim publishes `t`; Bob
completes the Bitcoin signature with it. Protocol and timelocks:
[SPEC.md §4](../../SPEC.md#4-nom-to-btc-swap-a-point-lock-on-nom-an-adaptor-signature-on-bitcoin).

Both roles run in one process and talk over channels. Every value that crosses is one
a real client would send over a wire, and every check a role makes is against a chain,
not against what the other role says.

## Run

In containers, from the repository root. The lab's compose file starts the devnet and
bitcoind, and the scenario is one of its tools:

```sh
docker compose up -d --build
docker compose run --rm nom-to-btc -scenario happy
docker compose run --rm nom-to-btc -scenario refund     # Alice never reveals: Bob reclaims, Alice refunds
docker compose run --rm nom-to-btc -scenario bad-terms  # Alice's refund would open too soon: Bob refuses
```

The lab console at http://localhost:8470 runs the same two from its *CLI and scenarios*
bench and shows the output as it is printed ([lab/README.md](../../lab/README.md)).

From the host, against the same containers, with the `go-zenon-ptlc` worktree beside
this checkout:

```sh
go build -o nom-to-btc.exe . && ./nom-to-btc.exe -scenario happy
./nom-to-btc.exe -scenario refund
```

Flags: `-nom` (default `http://localhost:35997`), `-btc` (`127.0.0.1:18443`), `-sats`,
`-znn`, `-expiry` (NoM expiry in seconds after the frontier momentum, default 240),
`-locktime` (Bitcoin refund height, blocks after the tip, default 30), `-confirmations`
(what Bob waits for on Alice's funding before he locks, default 1). The devnet's
momentums are about 10 s apart, so the happy path takes about two minutes and the
refund path about five.

## What Bob checks before he answers

Alice proposes the terms; Bob refuses them, before anything is locked on either chain,
unless (`checkTerms` in [`main.go`](main.go), D-32):

- the NoM expiry `E` is at least 120 s away, so that Alice has time to claim;
- `T` is a compressed point;
- the Bitcoin refund height `L` comes at least three blocks after `E`, counting a block
  every 300 s. Before that, Alice could take the bitcoin back by her refund leaf and
  the ZNN with `t`, both. Faster blocks are what hurt Bob, hence half Bitcoin's ten
  minutes; three blocks are his time to get a claim mined after he reads `t`.

`-scenario bad-terms` proposes a refund two blocks out against a four-minute expiry, and
passes when Bob refuses. The program checked neither the third rule nor its
confirmations before 2026-10-09 ([REVIEW.md](../../REVIEW.md) R-04, R-11). Alice keeps
her own rule: she does not reveal `t` with under 60 s left on the NoM expiry, because an
unlock answered after it is refused and shows `t` all the same (D-31).

## What each package is

| Package | What it does | Tested by |
|---|---|---|
| `adaptor/` | BIP-340 single-signer adaptor signatures on `btcec`: `Sign`, `Verify`, `Complete`, `Extract`, `PointOf` | `TestAllParities` (40 cases over all four key/nonce parities); `TestDartVectors` checks the Dart SDK's pre-signatures verify, complete and extract identically |
| `btc/` | The Taproot output (NUMS internal key, claim leaf `<A> CHECKSIGVERIFY <B> CHECKSIG`, refund leaf `<L> CLTV DROP <A> CHECKSIG`), claim and refund spends, BIP-341 sighashes, a regtest client | `TestClaimAndRefundInEngine`: the whole leg in btcd's script engine with no node: the pre-signature does not spend, the completed one does, Bob alone cannot, the refund works at `L` and not before, and not for Bob |
| `nom/` | The devnet client: fused dev accounts, send/receive blocks, ptlc `Create`/`Unlock`/`Reclaim`, waits, `ScanWitness`, which reads the scalar of the unlock the contract accepted off the contract's own chain, and `ScanAttempts`, every unlock of an entry, refused ones included | the scenario |
| `.` | The two parties, and Bob's test of the terms | `TestCheckTerms` |
| `keyswap/` | One party's side of the [key swap](#the-key-swap): the four messages, every check one side makes of the other, the pre-signature calls, the saved state, and the machine of stages that drives it | seventeen tests, both parties on go-zenon's mock chain through the node's RPC served in process |
| `cmd/keyswap/` | The key swap on the devnet, both parties in one process | the devnet runs below |
| `cmd/flows/` | The scenarios page's back end ([lab/README.md](../../lab/README.md#the-scenarios-page)) | its catalogue test, and the page's end-to-end script |

```sh
go test ./...     # no containers needed
```

## The key swap

A second swap lives in this module because it uses the same NoM client and the same
adaptor code, and has no Bitcoin in it: QSR for ZNN on two BIP340 key locks, with a
pre-signature each way and nothing of the secret on the chain. Protocol, checks and
what each side must keep:
[SPEC.md §5](../../SPEC.md#5-nom-to-nom-swap-on-key-locks-no-secret-on-the-chain).

```sh
docker compose run --rm keyswap                      # in the lab's containers
go build -o keyswap.exe ./cmd/keyswap && ./keyswap.exe
./keyswap.exe -dir swap1 -stop bob:locked            # stop dead once Bob has locked
./keyswap.exe -dir swap1                             # carry the same swap on from its files
```

Flags: `-nom`, `-dir` (where the two parties keep their state, one directory each
under it; a temporary directory if not given), `-qsr` and `-znn` (10 and 1),
`-alice-for` (how long Alice's entry lasts, 600 s), `-gap` (how much sooner Bob's
ends, 300 s), `-stop role:stage`, `-kill role:point`. It exits 0 when both sides end `swapped`, 3 when it
stopped as asked, 1 otherwise.

Each party is a `keyswap.Party` over its own directory and its own connection to the
node. It knows of the other only what the link carries, and the link here is a pipe
inside the process, which forgets everything when the process ends. That is why a
party sends its last message again whenever it starts. Two processes joined by a real
transport are the same code with another `Link`; the transport, and a wallet in place
of the account key the party holds, are not built ([GAPS G-05, G-19](../../GAPS.md)).

A party's directory holds one file, `swap.json`: its stage, the swap key, the secret
(Alice from the start, Bob once he has read it), both entry ids and both
pre-signatures. It is in the clear ([G-21](../../GAPS.md)), and it is all a party
needs to carry on.

Run on the devnet on 2026-10-09 (UTC):

| When | What | Result |
|---|---|---|
| 18:30:09 to 18:31:51 | the whole swap | both `swapped` in 1 min 42 s; Bob read `t` from Alice's signature ten seconds after she sent it |
| 18:32:00 to 18:33:41 | the same with `-stop bob:locked`, then run again over the same directory | stopped at 18:32:22 with both entries locked, Alice at `presigned` and Bob at `locked` on disk; carried on at 18:32:23, Alice sent her pre-signature again since the pipe had forgotten it, both `swapped` at 18:33:41 ([transcript](../../lab/e2e/runs/2026-10-09-keyswap-devnet-resume.txt)) |

| 19:22:44 to 19:24:20 | `-kill bob:created`: the process dies with Bob's `Create` on its way to the node and nothing of it on disk; then run again | Bob, back at `locking`, found the entry on his own account chain and did not lock a second time; both `swapped` ([transcript](../../lab/e2e/runs/2026-10-09-keyswap-devnet-kill.txt)) |
| 19:24:20 to 19:25:51 | `-kill alice:claimed`: the same with Alice's claim; then run again | Alice, back at `revealing` before the contract had answered, claimed again; the second was refused behind the first, which she read as the swap done; both `swapped` |
| 19:27:26 to 19:29:10 | `docker compose run --rm keyswap`, the image's build of the program | both `swapped` |

`-kill role:point` (`created`, `claimed`, `reclaimed`) ends the process between an
action and the saving of it. What a devnet run cannot do is choose how long a party
stays down, or put the clock half a minute before an expiry. The package's tests do
both, on the mock chain:

```sh
go test ./keyswap/     # no containers needed; about 12 s
```

## What the refund scenario shows

Alice funds Bitcoin, Bob locks ZNN, Alice never reveals. Bob waits until the NoM
expiry `E`, reclaims his ZNN, and Alice spends the Bitcoin refund leaf once the tip
reaches `L`. Nobody loses anything; nobody needed the other's cooperation to get out.

## Known limits

- Script-path claim, not MuSig2 key path ([GAPS G-03](../../GAPS.md)).
- The regtest miner is this process (`generatetoaddress`), so confirmations are
  instant; on a real network Bob's margin between seeing `t` and `L` is what the
  `-locktime` flag stands for, and `checkTerms`'s two figures are regtest's, not
  measured ones.
- The claim's fee is fixed in the transaction Alice pre-signs (500 sats); Bob can raise
  it only with a child that spends his output.
- The dev accounts are fused in genesis, so no block needs proof of work; an unfused
  account would add about a minute per block (latency facts).
