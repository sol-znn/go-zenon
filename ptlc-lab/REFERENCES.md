# References

The sources this implementation answers to, in the order they bind it. Everything
under "canonical" is what the network's founder or its official channels said;
"prior code" is what was built; "standards" is what the rest of the industry agreed.
Local paths are relative to `C:\dev\zenon`.

Back to the [README](README.md).

---

## Canonical: what NoM asked for

| Date | Source | What it fixes |
|---|---|---|
| 2022-02-21 | Mr Kaine, [221608](https://t.me/zenonnetwork/221608): "Schnorr is great for scriptless scripts (atomic swaps with Bitcoin)" | The purpose is Bitcoin swaps by scriptless scripts |
| 2022-03-21 | George links Blockstream's scriptless-scripts atomic swap; Mr Kaine: "Exactly" ([228662](https://t.me/zenonnetwork/228662)) | The reference construction |
| 2022-10-24 | Mr Kaine, [258822](https://t.me/zenonnetwork/258822)–[258828](https://t.me/zenonnetwork/258828): "HTLC is good, but PTLC is better … Instead of hashes, it uses adaptor signatures for locking … Fungibility and less block space … We can have both" | PTLC sits beside HTLC, not in place of it |
| 2022-11-24 | Medium, Towards NoM Phase 1: "hashed and point timelock contracts (HTLC and PTLC) are being developed to provide a first glance into basic Bitcoin interoperability" | PTLC is official Phase 1 scope under Accessibility |
| 2023-03-22 | Mr Kaine, [274867](https://t.me/zenonnetwork/274867): "BIP340 Schnorr … ECDSA is needed only if we want to support older versions"; [274870](https://t.me/zenonnetwork/274870): "Think of them as primitives … BTC/LN will soon fully migrate to PTLCs and we'll be able to natively interoperate with it" | BIP-340 is the Bitcoin-side scheme; no ECDSA |
| 2023-04-06 | Mr Kaine, [278533](https://t.me/zenonnetwork/278533): "The next focus should be PTLCs and core network upgrades" | Priority |
| 2023-05-10 | Mr Kaine, [286634](https://t.me/zenonnetwork/286634): "We need PTLCs, dynamic Plasma and a prototype for the extension chain first" | Ordering |
| 2023-07-18 | Mr Kaine, [303952](https://t.me/zenonnetwork/303952)–[303966](https://t.me/zenonnetwork/303966): asks George for an end-to-end user-flow diagram, links Blockstream's scriptless-scripts; "The code looks clean"; "if a majority of devs approve it, it will get merged" | The review bar: an end-to-end diagram and community approval. [SPEC.md](SPEC.md) carries the diagrams |
| 2021-11 | The whitepaper (summarised in zenon-docs/protocol/whitepaper.md): Ed25519 at Alphanet, "BLS or Schnorr" under study | The curve gap: NoM accounts are Ed25519, Bitcoin is secp256k1 |

The full guidance digest is zenon-docs/corpus/telegram/mr-kaine/guidance.md; the Taproot reading is zenon-docs/taproot/.

## Prior code: what was built

| Date | Source | What it is |
|---|---|---|
| 2023-02-04 | George, [AZ Proposal: Point Time Locked Contract](https://github.com/orgs/ignition-pillar/discussions/2) | The design: point lock + time lock; Ed25519 and BIP-340; unlock message `id ‖ destination` hashed with SHA3 because BIP-340 takes 32 bytes; proxy unlock as a separate method to spare ordinary unlocks the extra bytes |
| 2023-03-06 → 2023-05-22 | George, go-zenon [PR #13 "Add PTLC"](https://github.com/zenon-network/go-zenon/pull/13), head `8ed1ca1`, +1,740 −86. Closed without merging 2026-10-03 by 0x3639, no comment | `Create(expirationTime, pointType, pointLock)`, `Unlock(id, signature)`, `ProxyUnlock(id, destination, signature)`, `Reclaim(id)`; spork id `e59f1e97…` (exists on mainnet, never activated) |
| 2023-09-08 | George, forum [200 PTLCs: The Standard(?)](https://forum.hypercore.one/t/ptlcs-the-standard/200) | The privacy case: use PTLCs for every new identity, random timing, disposable BIP-340 points even for ZTS-only swaps |
| 2024-01-19 | 0x3639, forum [314 PTLC Code Review](https://forum.hypercore.one/t/ptlc-code-review/314) | Review stalled: sumamu had questions, George waited for review |
| 2024-07-15 | KingGorrin, [znn_ptlc_use_cases_go](https://github.com/KingGorrin/znn_ptlc_use_cases_go) `bdc02c1` | Single-chain swap demo on a PTLC devnet. **Its Ed25519 two-party signing leaks a key share** (edgepillar); not reused |
| 2025-11-21 | Copilot review on PR #13 | The fail-open `else` in `unlockPtlc`; raw btcec errors leaking |
| 2026-05-27 → 05-29 | 0x3639, [`codex/ptlc-hardening`](https://github.com/0x3639/go-zenon/tree/codex/ptlc-hardening) and [`codex/ptlc-testnet-local`](https://github.com/0x3639/go-zenon/tree/codex/ptlc-testnet-local), nine commits on `master` `667a69d` | Fail-closed unlock, domain-separated message, stored-state validation, BIP-340 key validated at `Create`, stable errors, signature hashes in logs, 3,400 lines of unit, adversarial, fuzz and live-RPC tests, a two-party swap choreography test, `docs/ptlc/`. **The base of this repo (D-01)** |
| 2026-09-06 | edgepillar's comment on PR #13 | Validate encodings at admission; fail closed; deterministic vectors; proxy-unlock coverage |
| 2026-10-03 | [edgepillar/ptlc-research](https://github.com/edgepillar/ptlc-research) `ac5f41e` | Offline BTC⇄ZNN swap study over PR #13: CANDIDATE-01 graph, two Rust adaptor libraries qualified in all four parity cases, Taproot claim/refund built and run in btcd's engine, nonce journal with crash tests, the KingGorrin leak reproduced. Client rules adopted here: journal a nonce before using it; a secret that may have left the process is public; a timeout after submission is an unknown outcome |
| 2026-10-04 → 10-05 | zenon-zbtc `patches/0001`–`0004`, `swap/`, `btc/`, `signer/` | PR #13 ported onto `dev` (0001); **two lock types inside `htlc`**: scalar point lock and BIP-340 key lock (0002 on `dev`, 0003 on `release/0.0.9`); BIP-340 accounts (0004). The point-lock BTC⇄ZNN swap ran 35 of 35 on a regtest + devnet lab with MuSig2 adaptors in a Rust signer. **Merged here as the third point type (D-04)** |

## Standards

| Standard | Used for |
|---|---|
| [BIP-340](https://github.com/bitcoin/bips/blob/master/bip-0340.mediawiki) Schnorr signatures for secp256k1 | The BIP-340 point type; the Bitcoin leg's signatures; the official test vectors in `test/vectors/bip340` |
| [BIP-341](https://github.com/bitcoin/bips/blob/master/bip-0341.mediawiki) / [BIP-342](https://github.com/bitcoin/bips/blob/master/bip-0342.mediawiki) Taproot and Tapscript | The Bitcoin output of the NoM-to-BTC scenario |
| [BIP-327](https://github.com/bitcoin/bips/blob/master/bip-0327.mediawiki) MuSig2 | The production Bitcoin leg (G-03); not used by the demo |
| [Blockstream scriptless-scripts: atomic swap](https://github.com/BlockstreamResearch/scriptless-scripts/blob/master/md/atomic-swap.md) | The adaptor-signature swap, as Mr Kaine linked it |
| Lightning PTLC proposals ([Bitcoin Optech topic](https://bitcoinops.org/en/topics/ptlc/)); "payment points" | The scalar lock with a per-hop tweak (D-04, D-12). Lightning's spec is unfinished (A-08) |
| [libsecp256k1-zkp `schnorr_adaptor`](https://github.com/BlockstreamResearch/secp256k1-zkp) | The adaptor parity rules (A-09) |
| RFC 8032 Ed25519 | The Ed25519 point type, as NoM wallets sign |
