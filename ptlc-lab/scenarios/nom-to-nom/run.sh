#!/usr/bin/env bash
# NoM-to-NoM swap on two tweaked point locks, driven by znn-cli (SPEC.md §3).
#
# Alice (index 1) holds the secret t, so she locks first: QSR to T + d*G for
# Bob, for two hours. Bob (index 3) checks her entry and locks ZNN to T for
# Alice, for one: his entry ends first, so whenever Alice claims it and shows
# t, hers is still open to him. Alice's unlock publishes t; Bob reads it off
# the chain with ptlc.monitor, adds d, and unlocks Alice's leg. On chain the
# two entries show unrelated points and unrelated scalars.
#
# Until 2026-10-09 this ran Bob first and Bob last, which lets Alice claim his
# entry without ever funding hers, or let hers expire, take it back, and claim
# his afterwards (zenon-ptlc DECISIONS D-30).
#
# Needs: the PTLC devnet (zenon-ptlc/devnet, RPC ws://127.0.0.1:35998, chain
# 69) and the CLI built from the znn_cli_dart-ptlc worktree.
set -euo pipefail

CLI="${CLI:-/c/dev/zenon/znn_cli_dart-ptlc/build/znn-cli.exe}"
URL="${URL:-ws://127.0.0.1:35998}"
CHAIN="${CHAIN:-69}"
KEYSTORE="${KEYSTORE:-ptlc-devnet}"
PASS="${PASS:-ptlc}"
MNEMONIC="abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"
ALICE_INDEX=1
BOB_INDEX=3
ZNN_AMOUNT="${ZNN_AMOUNT:-10}"
QSR_AMOUNT="${QSR_AMOUNT:-100}"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }
alice() { "$CLI" -u "$URL" -c "$CHAIN" -k "$KEYSTORE" -p "$PASS" -i "$ALICE_INDEX" "$@"; }
bob()   { "$CLI" -u "$URL" -c "$CHAIN" -k "$KEYSTORE" -p "$PASS" -i "$BOB_INDEX" "$@"; }
# the last hex token on the first line that starts with the label
field() { sed 's/\x1b\[[0-9;]*m//g' | tr -d '\r' | grep -m1 "^$1" | grep -o '[0-9a-f]\{64,66\}' | tail -1; }
# an entry is visible only once the contract's receive has landed, two momentums on
wait_entry() { local who=$1 id=$2 n=0; until $who ptlc.get "$id" 2>/dev/null | grep -q "^Ptlc id"; do n=$((n+1)); [ $n -gt 40 ] && { echo "entry $id never appeared" >&2; exit 1; }; sleep 5; done; }

# ---- wallets ----------------------------------------------------------------
# wallet.list prints addresses, not names, so probe the keystore by using it
if ! "$CLI" -k "$KEYSTORE" -p "$PASS" wallet.deriveAddresses 1 2 > /dev/null 2>&1; then
  log "creating keystore $KEYSTORE from the devnet mnemonic"
  "$CLI" wallet.createFromMnemonic "$MNEMONIC" "$PASS" "$KEYSTORE" || true
fi
addr_at() { "$CLI" -k "$KEYSTORE" -p "$PASS" wallet.deriveAddresses "$1" "$(($1 + 1))" | grep -o 'z1[0-9a-z]\{38\}' | head -1; }
ALICE_ADDR=$(addr_at $ALICE_INDEX)
BOB_ADDR=$(addr_at $BOB_INDEX)
log "Alice $ALICE_ADDR"
log "Bob   $BOB_ADDR"
log "balances before:"; alice balance "$ALICE_ADDR"; bob balance "$BOB_ADDR"

# ---- the secret, its point, and the tweak -----------------------------------
KEYGEN=$("$CLI" ptlc.keygen)
T_SECRET=$(echo "$KEYGEN" | field "Secret")
T_POINT=$(echo "$KEYGEN" | field "Point")
D_TWEAK=$("$CLI" ptlc.keygen | field "Secret")
T2_POINT=$("$CLI" ptlc.tweak "$T_POINT" "$D_TWEAK" | field "Tweaked point")
log "Alice's point T  = $T_POINT"
log "tweak d (shared off chain) = $D_TWEAK"
log "Bob's point T+dG = $T2_POINT"

# ---- Alice, who holds t, locks first and expires last -----------------------
log "Alice locks $QSR_AMOUNT QSR to T+dG for Bob, 2 h"
ALICE_ENTRY=$(alice ptlc.create QSR "$QSR_AMOUNT" 2 2 "$T2_POINT" "$BOB_ADDR" | field "Submitted ptlc with id")
log "  entry $ALICE_ENTRY"
wait_entry alice "$ALICE_ENTRY"

# Bob holds it to the terms before he locks anything: the tweaked point he can
# work out himself, his own address, and the amount.
ALICE_INFO=$(bob ptlc.get "$ALICE_ENTRY" | sed 's/\x1b\[[0-9;]*m//g' | tr -d '\r')
echo "$ALICE_INFO"
echo "$ALICE_INFO" | grep -q "lock $T2_POINT" || { echo "Alice's entry is not locked to T+dG" >&2; exit 1; }
echo "$ALICE_INFO" | grep -q "unlocked to $BOB_ADDR" || { echo "Alice's entry does not pay Bob" >&2; exit 1; }

log "Bob locks $ZNN_AMOUNT ZNN to T for Alice, 1 h, ending an hour before hers"
BOB_ENTRY=$(bob ptlc.create ZNN "$ZNN_AMOUNT" 1 2 "$T_POINT" "$ALICE_ADDR" | field "Submitted ptlc with id")
log "  entry $BOB_ENTRY"
wait_entry bob "$BOB_ENTRY"
alice ptlc.get "$BOB_ENTRY"

# ---- Bob watches Bob's entry for the scalar ---------------------------------
MON=$(mktemp)
bob ptlc.monitor "$BOB_ENTRY" > "$MON" 2>&1 &
MON_PID=$!

# ---- Alice claims the ZNN, publishing t -------------------------------------
log "Alice unlocks Bob's entry with t"
alice ptlc.unlock "$BOB_ENTRY" "$T_SECRET"
wait "$MON_PID" || true
sed 's/\x1b\[[0-9;]*m//g' "$MON"
WITNESS=$(sed 's/\x1b\[[0-9;]*m//g' "$MON" | grep -o 'witness [0-9a-f]\{64\}' | head -1 | cut -d' ' -f2 || true)
log "Bob read the witness off the chain: $WITNESS"
[ "$WITNESS" = "$T_SECRET" ] || { echo "witness differs from t" >&2; exit 1; }

# ---- Bob adds the tweak and claims the QSR ----------------------------------
T2_SECRET=$(bob ptlc.tweakSecret "$WITNESS" "$D_TWEAK" | field "Tweaked secret")
log "Bob unlocks Alice's entry with t + d"
bob ptlc.unlock "$ALICE_ENTRY" "$T2_SECRET"
until ! alice ptlc.get "$ALICE_ENTRY" 2>/dev/null | grep -q "^Ptlc id"; do sleep 5; done

log "collecting"
alice receiveAll > /dev/null; bob receiveAll > /dev/null
sleep 15
log "balances after:"; alice balance "$ALICE_ADDR"; bob balance "$BOB_ADDR"
log "done: Alice has Bob's ZNN, Bob has Alice's QSR; the chain shows T and T+dG, t and t+d"
