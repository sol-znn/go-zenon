# The PTLC devnet

The go-zenon-ptlc branch carries its own devnet (`docker/devnet/` and
`docker-compose.yml` there, from 0x3639's `codex/ptlc-testnet-local`): three pillars,
an RPC node and an observer, chain id 69, the PTLC spork activated in genesis under
its real id `e59f1e97…`, and two funded accounts. This directory runs that devnet under
names and a subnet of its own (`ptlc-devnet-*`, `10.69.0.0/24`), because this host
also keeps a stopped `znnd-devnet-*` set that belongs to another project
([DECISIONS D-11](../DECISIONS.md)).

```sh
# the usual way: the whole lab, from the repository root
docker compose up -d --build
# or the devnet alone, from this directory
docker compose -p ptlc -f compose.yml up -d --build
docker compose -p ptlc -f compose.yml down -v     # wipe; the next up reproduces the same genesis
```

The image is built by [`../docker/Dockerfile`](../docker/Dockerfile) from go-zenon
`release/0.0.9` at `a681890` with [`../patches`](../patches) applied, and the build
fails unless the patched tree is the branch's tree, hash for hash. The worktree is not
needed. The network, the containers and the volumes carry fixed `ptlc-devnet` names, so
the chain is the same chain whether the lab or this file alone started it; the two
cannot run at once.

| | |
|---|---|
| HTTP RPC | `http://localhost:35997` (rpc node), `http://localhost:35991` (pillar 1) |
| WS RPC | `ws://localhost:35998` |
| Chain id | 69 |
| Funded accounts | indexes 1 (`z1qq6eg8n…`) and 3 (`z1qp3yph5…`) of the mnemonic in `../scenarios/nom-to-btc/nom/nom.go`, fused so no block needs proof of work |
| Sporks at genesis | az, htlc, bridge-liq, ptlc |

`assets/` is the branch's `docker/devnet/` with two changes to the node configs:
`172.30.0.x` rewritten to `10.69.0.x`, and `MinConnectedPeers` raised to 4 on the RPC
node, the observer and pillars 2 and 3 ([DECISIONS D-24](../DECISIONS.md)). Regenerate
it from the branch if the branch's devnet changes, and make both again.

The second one matters more than it looks. A node passes a new account block to its
own peers and no further, and a node stops dialling once it has `MinConnectedPeers`
outbound connections. With the branch's values (2 for the relays, 1 for the pillars)
the RPC node can settle on two peers of which only one is a pillar. Blocks published
to it then reach that pillar alone: they are included only on its turn, and a contract
call that normally is answered one momentum later was seen to wait sixteen. With 4,
every node has the other four. To check:

```sh
curl -sX POST http://localhost:35997 -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"stats.networkInfo","params":[]}'     # "numPeers":4
```

The lab's Start here bench shows the same number. A node copies its config from
`assets/` only when its volume is empty, so after editing a config either wipe the
chain (`down -v`) or copy the file into the volume.

The first momentums after `up` can be reorganised while the three pillars find each
other; wait for height 7 or so before running a scenario. On this host momentums come
about every 10 to 15 s ([clock-step note](../ASSUMPTIONS.md) A-07).

The branch's own live suite runs against this devnet too:

```sh
docker compose run --rm test-live          # from the repository root, on Linux, from the patched tree
PTLC_TESTNET_RPC=http://localhost:35997 go -C /c/dev/zenon/go-zenon-ptlc test -tags testnet -count=1 -timeout 25m ./testnet/ptlc/
```
