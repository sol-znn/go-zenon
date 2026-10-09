#!/bin/sh
# Gives znn-cli the keystore the scenarios and the console's CLI panel sign
# with: the devnet mnemonic the go-zenon branch commits, under a name and a
# password that are as public as it is. Nothing here holds value.
set -e
: "${PTLC_KEYSTORE:=ptlc-devnet}"
: "${PTLC_KEYSTORE_PASS:=ptlc}"
MNEMONIC="abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"
if ! znn-cli -k "$PTLC_KEYSTORE" -p "$PTLC_KEYSTORE_PASS" wallet.deriveAddresses 1 2 >/dev/null 2>&1; then
  znn-cli wallet.createFromMnemonic "$MNEMONIC" "$PTLC_KEYSTORE_PASS" "$PTLC_KEYSTORE" >/dev/null 2>&1 || true
fi
exec "$@"
