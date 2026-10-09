// Package btc is the Bitcoin side of the swap: the Taproot output both
// parties agree on, the claim and refund transactions that spend it, their
// BIP-341 sighashes, and a thin regtest client.
//
// The output has an unspendable internal key and two leaves:
//
//	claim:  <A> OP_CHECKSIGVERIFY <B> OP_CHECKSIG
//	refund: <L> OP_CHECKLOCKTIMEVERIFY OP_DROP <A> OP_CHECKSIG
//
// Alice (A) funds it and pre-signs Bob's (B) claim as an adaptor on the swap
// point; Bob completes that signature with the scalar Alice reveals on NoM
// and adds his own. After block L Alice can take it back alone. A MuSig2 key
// path would be cheaper and would reveal no script; the leaf is used so that
// this demo needs no two-party nonce exchange (zenon-ptlc GAPS.md G-03).
package btc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// numsKeyHex is BIP-341's "nothing up my sleeve" point H, the x coordinate of
// SHA256(G) lifted to the curve. An output whose internal key is H can only be
// spent through a leaf.
const numsKeyHex = "50929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0"

// NUMSKey returns the unspendable internal key.
func NUMSKey() *btcec.PublicKey {
	b, _ := hex.DecodeString(numsKeyHex)
	k, err := schnorr.ParsePubKey(b)
	if err != nil {
		panic(err)
	}
	return k
}

// SwapOutput is the agreed output and everything needed to spend it.
type SwapOutput struct {
	AliceX, BobX [32]byte
	Locktime     uint32
	ClaimLeaf    txscript.TapLeaf
	RefundLeaf   txscript.TapLeaf
	Tree         *txscript.IndexedTapScriptTree
	OutputKey    *btcec.PublicKey
	PkScript     []byte
	Address      btcutil.Address
}

func claimScript(aliceX, bobX [32]byte) ([]byte, error) {
	return txscript.NewScriptBuilder().
		AddData(aliceX[:]).AddOp(txscript.OP_CHECKSIGVERIFY).
		AddData(bobX[:]).AddOp(txscript.OP_CHECKSIG).
		Script()
}

func refundScript(locktime uint32, aliceX [32]byte) ([]byte, error) {
	return txscript.NewScriptBuilder().
		AddInt64(int64(locktime)).AddOp(txscript.OP_CHECKLOCKTIMEVERIFY).AddOp(txscript.OP_DROP).
		AddData(aliceX[:]).AddOp(txscript.OP_CHECKSIG).
		Script()
}

// NewSwapOutput builds the output for Alice's and Bob's x-only keys and the
// refund height.
func NewSwapOutput(aliceX, bobX [32]byte, locktime uint32, params *chaincfg.Params) (*SwapOutput, error) {
	claim, err := claimScript(aliceX, bobX)
	if err != nil {
		return nil, err
	}
	refund, err := refundScript(locktime, aliceX)
	if err != nil {
		return nil, err
	}
	claimLeaf := txscript.NewBaseTapLeaf(claim)
	refundLeaf := txscript.NewBaseTapLeaf(refund)
	tree := txscript.AssembleTaprootScriptTree(claimLeaf, refundLeaf)
	root := tree.RootNode.TapHash()
	outputKey := txscript.ComputeTaprootOutputKey(NUMSKey(), root[:])
	pkScript, err := txscript.PayToTaprootScript(outputKey)
	if err != nil {
		return nil, err
	}
	addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(outputKey), params)
	if err != nil {
		return nil, err
	}
	return &SwapOutput{
		AliceX: aliceX, BobX: bobX, Locktime: locktime,
		ClaimLeaf: claimLeaf, RefundLeaf: refundLeaf, Tree: tree,
		OutputKey: outputKey, PkScript: pkScript, Address: addr,
	}, nil
}

func (o *SwapOutput) controlBlock(leaf txscript.TapLeaf) ([]byte, error) {
	idx, ok := o.Tree.LeafProofIndex[leaf.TapHash()]
	if !ok {
		return nil, errors.New("leaf not in tree")
	}
	proof := o.Tree.LeafMerkleProofs[idx]
	cb := proof.ToControlBlock(NUMSKey())
	return cb.ToBytes()
}

// Spend is an unsigned transaction spending the swap output to one address.
type Spend struct {
	Tx      *wire.MsgTx
	PrevOut *wire.TxOut
}

// NewSpend builds a one-input, one-output transaction spending outpoint
// (worth value) to dest, paying fee. locktime and sequence are what the
// refund leaf needs; the claim passes 0 and wire.MaxTxInSequenceNum.
func NewSpend(outpoint wire.OutPoint, value int64, swapPkScript []byte, dest btcutil.Address, fee int64, locktime uint32, sequence uint32) (*Spend, error) {
	if value <= fee {
		return nil, fmt.Errorf("value %d does not cover fee %d", value, fee)
	}
	destScript, err := txscript.PayToAddrScript(dest)
	if err != nil {
		return nil, err
	}
	tx := wire.NewMsgTx(2)
	tx.LockTime = locktime
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint, Sequence: sequence})
	tx.AddTxOut(wire.NewTxOut(value-fee, destScript))
	return &Spend{Tx: tx, PrevOut: wire.NewTxOut(value, swapPkScript)}, nil
}

// Sighash is the BIP-341 tapscript sighash (SIGHASH_DEFAULT) of the spend's
// only input under leaf.
func (s *Spend) Sighash(leaf txscript.TapLeaf) ([]byte, error) {
	fetcher := txscript.NewCannedPrevOutputFetcher(s.PrevOut.PkScript, s.PrevOut.Value)
	hashes := txscript.NewTxSigHashes(s.Tx, fetcher)
	return txscript.CalcTapscriptSignaturehash(hashes, txscript.SigHashDefault, s.Tx, 0, fetcher, leaf)
}

// SignLeaf signs the spend under leaf with priv, SIGHASH_DEFAULT.
func (s *Spend) SignLeaf(leaf txscript.TapLeaf, priv *btcec.PrivateKey) ([]byte, error) {
	h, err := s.Sighash(leaf)
	if err != nil {
		return nil, err
	}
	sig, err := schnorr.Sign(priv, h)
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}

// SetClaimWitness installs the claim leaf's witness: Bob's signature, then
// Alice's, then the leaf and its control block. The script checks the top
// item (Alice's) against A first, then Bob's against B.
func (s *Spend) SetClaimWitness(o *SwapOutput, aliceSig, bobSig []byte) error {
	cb, err := o.controlBlock(o.ClaimLeaf)
	if err != nil {
		return err
	}
	s.Tx.TxIn[0].Witness = wire.TxWitness{bobSig, aliceSig, o.ClaimLeaf.Script, cb}
	return nil
}

// SetRefundWitness installs the refund leaf's witness.
func (s *Spend) SetRefundWitness(o *SwapOutput, aliceSig []byte) error {
	cb, err := o.controlBlock(o.RefundLeaf)
	if err != nil {
		return err
	}
	s.Tx.TxIn[0].Witness = wire.TxWitness{aliceSig, o.RefundLeaf.Script, cb}
	return nil
}

// Execute runs the spend through btcd's script engine against the swap
// output, with the chain's rules for a block at height. It is the same check
// a node makes, and lets the tests run without a node.
func (s *Spend) Execute(height int32) error {
	fetcher := txscript.NewCannedPrevOutputFetcher(s.PrevOut.PkScript, s.PrevOut.Value)
	hashes := txscript.NewTxSigHashes(s.Tx, fetcher)
	flags := txscript.StandardVerifyFlags
	vm, err := txscript.NewEngine(s.PrevOut.PkScript, s.Tx, 0, flags, nil, hashes, s.PrevOut.Value, fetcher)
	if err != nil {
		return err
	}
	return vm.Execute()
}

// Serialize returns the transaction's raw bytes.
func (s *Spend) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := s.Tx.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// KeyAddress is the P2TR address of a key's own key path (BIP-86 style, no
// script), used for the parties' plain receiving addresses.
func KeyAddress(pub *btcec.PublicKey, params *chaincfg.Params) (btcutil.Address, error) {
	tweaked := txscript.ComputeTaprootKeyNoScript(pub)
	return btcutil.NewAddressTaproot(schnorr.SerializePubKey(tweaked), params)
}

// Regtest is a Bitcoin Core regtest node over JSON-RPC.
type Regtest struct {
	*rpcclient.Client
	Params *chaincfg.Params
}

// Dial connects to bitcoind's HTTP RPC.
func Dial(host, user, pass string) (*Regtest, error) {
	c, err := rpcclient.New(&rpcclient.ConnConfig{
		Host: host, User: user, Pass: pass,
		HTTPPostMode: true, DisableTLS: true,
	}, nil)
	if err != nil {
		return nil, err
	}
	return &Regtest{Client: c, Params: &chaincfg.RegressionNetParams}, nil
}

// Height is the chain tip height.
func (r *Regtest) Height() (int64, error) {
	return r.GetBlockCount()
}

// Broadcast submits a transaction with a raw sendrawtransaction call. btcd's
// typed SendRawTransaction first asks getnetworkinfo to learn the node's
// version, and Bitcoin Core 31 returns its warnings as an array where the
// client expects a string, so that path fails before anything is sent.
func (r *Regtest) Broadcast(tx *wire.MsgTx) (*chainhash.Hash, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(hex.EncodeToString(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	res, err := r.RawRequest("sendrawtransaction", []json.RawMessage{raw})
	if err != nil {
		return nil, err
	}
	var txid string
	if err := json.Unmarshal(res, &txid); err != nil {
		return nil, err
	}
	return chainhash.NewHashFromStr(txid)
}

// Mine mines n blocks to addr.
func (r *Regtest) Mine(n int64, addr btcutil.Address) ([]*chainhash.Hash, error) {
	return r.GenerateToAddress(n, addr, nil)
}

// FindOutput returns the outpoint and value in tx that pays pkScript.
func FindOutput(tx *wire.MsgTx, pkScript []byte) (wire.OutPoint, int64, error) {
	for i, out := range tx.TxOut {
		if bytes.Equal(out.PkScript, pkScript) {
			return wire.OutPoint{Hash: tx.TxHash(), Index: uint32(i)}, out.Value, nil
		}
	}
	return wire.OutPoint{}, 0, errors.New("no output pays the swap script")
}
