# NoM-to-NoM swap on two tweaked point locks, driven by znn-cli

Alice (index 1 of the devnet mnemonic) sells QSR for Bob's ZNN (index 3). Alice holds
`t`; the two agree a tweak `d` off chain. Alice locks QSR to `T + d·G` payable to Bob,
for two hours; Bob checks it and locks ZNN to `T = t·G` payable to Alice, for one.
Alice's unlock publishes `t`; Bob reads it with `ptlc.monitor`, adds `d`, and unlocks
Alice's entry. The two entries show unrelated points and unrelated scalars, so nothing
on chain says they were one trade.
Protocol: [SPEC.md §3](../../SPEC.md#3-nom-to-nom-swap-on-point-locks-with-a-tweak).

The holder of the secret locks first and expires last. Until 2026-10-09 this script
had Bob lock first and expire last, which is unsafe for Bob: Alice could claim his
entry and never fund hers, or let hers expire, reclaim it, and claim his afterwards.
[DECISIONS D-30](../../DECISIONS.md) has the correction and the tests that show both.

The same swap runs as a go-zenon mock-chain test, with no containers:
`TestPtlc_pointLock_tweakedSwap` in `vm/embedded/tests/ptlc_point_test.go` on the
branch.

## Run

In containers, from the repository root, with the CLI the lab image builds from
[`dart/`](../../dart):

```sh
docker compose up -d --build
docker compose run --rm nom-to-nom
```

The lab console at http://localhost:8470 runs it from its *CLI and scenarios* bench, and
walks the same swap one call at a time, with expiries in minutes, on its *Swap, step by
step* bench ([lab/README.md](../../lab/README.md)).

From the host, against the same devnet:

```sh
bash run.sh
```

On the host the script uses the CLI built from the `znn_cli_dart-ptlc` worktree
(`dart compile exe znn-cli.dart -o build/znn-cli.exe` there), creates a keystore
named `ptlc-devnet` from the devnet mnemonic on first use, and prints every value
the two parties exchange. `CLI`, `URL`, `CHAIN`, `KEYSTORE`, `PASS`, `ZNN_AMOUNT`
and `QSR_AMOUNT` can be overridden in the environment.

## The commands it uses

| Step | Who | Command |
|---|---|---|
| secret and point | Alice | `ptlc.keygen` |
| tweak | both | `ptlc.keygen` (its secret is `d`), `ptlc.tweak T d` |
| lock QSR to `T + d·G` for Bob, 2 h | Alice | `ptlc.create QSR 100 2 2 T' <bob>` |
| check her entry: `T'`, pays Bob | Bob | `ptlc.get <alice's entry>` |
| lock ZNN to `T` for Alice, 1 h | Bob | `ptlc.create ZNN 10 1 2 T <alice>` |
| watch for the reveal | Bob | `ptlc.monitor <bob's entry>` |
| claim, revealing `t` | Alice | `ptlc.unlock <bob's entry> t` |
| derive `t + d` | Bob | `ptlc.tweakSecret t d` |
| claim | Bob | `ptlc.unlock <alice's entry> t+d` |

Alice has to claim Bob's entry before it expires, and `ptlc.unlock` will not send with
under a minute left on the chain's clock, so whenever she shows `t` Bob has the hour
between the two expiries to claim hers. `ptlc.monitor` takes only the unlock the
contract accepted, and checks its witness against the lock: a refused unlock with a
junk witness, sent first by anyone, does not mislead it. The CLI's minimum expiry is
one hour, so a refund rehearsal is quicker on the mock chain
(`TestPtlc_pointLock_expiry_and_reclaim`) or on the lab's swap bench.

## If a run stops half way

An entry a stopped run leaves behind stays locked until its expiry, when its creator
reclaims it with `ptlc.reclaim <id>`; the secret of a run that printed only the point
is gone with the process. That is the contract working as designed, and the reason
a real client journals its secret before it funds anything (zenon-zbtc D14).

Ran 2026-10-09 in the safe order on the devnet, from the host 15:19:48 to 15:22:38 UTC
and in the lab image 15:22:53 to 15:24:22: Alice's 100 QSR went to Bob and Bob's 10 ZNN
to Alice, and `ptlc.monitor` handed Bob the scalar 24 s and 11 s after Alice's unlock
([transcript](../../lab/e2e/runs/2026-10-09-review-nom-to-nom.txt)). A first try in
the image stopped after Alice's unlock: under `docker compose run` the CLI colours its
output, and the line that took the witness out of the monitor's did not strip the
colour codes. It does now. The run of 2026-10-08 (03:51 to 03:54) was in the old,
unsafe order.

The monitor's fix was run on its own the same day, against a junk unlock sent first:
the CLI before the fix printed the junk as the witness, the fixed one reported the
call as refused and printed `t`
([transcript](../../lab/e2e/runs/2026-10-09-review-monitor.txt)).
