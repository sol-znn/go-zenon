// Package nom is the swap's NoM side: a client for the devnet's RPC that
// builds, signs and publishes account blocks for the ptlc contract, waits
// for them, and reads the scalar out of an unlock once it lands. It follows
// the branch's own live harness (go-zenon-ptlc/testnet/ptlc) and uses the
// devnet's funded accounts, whose plasma is fused in genesis so no block
// needs proof of work.
package nom

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/tyler-smith/go-bip39"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	embeddedapi "github.com/zenon-network/go-zenon/rpc/api/embedded"
	rpcclient "github.com/zenon-network/go-zenon/rpc/server"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/wallet"
)

// DevMnemonic funds indexes 1 and 3 in docker/devnet/genesis.json on the
// branch. It is a devnet key; nothing of value is behind it.
const DevMnemonic = "abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"

const (
	Zexp              = int64(100000000)
	PointTypeScalar   = definition.PointTypeSecp256k1Point
	contractSuccess   = uint64(1)
	defaultPoll       = 2 * time.Second
	defaultBlockWait  = 3 * time.Minute
	defaultEntryWait  = 4 * time.Minute
	readyMomentumHigh = uint64(3)
)

// DevKey derives one of the devnet's funded keys.
func DevKey(index uint32) (*wallet.KeyPair, error) {
	seed := bip39.NewSeed(DevMnemonic, "")
	return wallet.DeriveWithIndex(index, seed)
}

// Client is one party's view of the devnet.
type Client struct {
	c   *rpcclient.Client
	log func(string, ...interface{})
}

// Dial connects to a znnd HTTP RPC endpoint and waits until it has produced
// a few momentums.
func Dial(ctx context.Context, url string, log func(string, ...interface{})) (*Client, error) {
	var c *rpcclient.Client
	deadline := time.Now().Add(90 * time.Second)
	for {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cc, err := rpcclient.DialContext(dctx, url)
		cancel()
		if err == nil {
			c = cc
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial %s: %w", url, err)
		}
		time.Sleep(time.Second)
	}
	cl := &Client{c: c, log: log}
	for {
		m, err := cl.FrontierMomentum()
		if err == nil && m.Height >= readyMomentumHigh {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("node is not producing momentums")
		}
		time.Sleep(time.Second)
	}
	return cl, nil
}

func (cl *Client) Close() { cl.c.Close() }

func (cl *Client) call(result interface{}, method string, args ...interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return cl.c.CallContext(ctx, result, method, args...)
}

func (cl *Client) FrontierMomentum() (*nom.Momentum, error) {
	var m *api.Momentum
	if err := cl.call(&m, "ledger.getFrontierMomentum"); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("no frontier momentum")
	}
	return m.Momentum, nil
}

// Now is the chain's clock: the frontier momentum's timestamp. Expiries are
// judged against it, never against a wall clock.
func (cl *Client) Now() (int64, error) {
	m, err := cl.FrontierMomentum()
	if err != nil {
		return 0, err
	}
	// Timestamp is a cache the RPC does not fill; TimestampUnix is the hashed field.
	return int64(m.TimestampUnix), nil
}

func (cl *Client) ChainIdentifier() (uint64, error) {
	m, err := cl.FrontierMomentum()
	if err != nil {
		return 0, err
	}
	return m.ChainIdentifier, nil
}

func (cl *Client) Balance(address types.Address, token types.ZenonTokenStandard) (*big.Int, error) {
	var info *api.AccountInfo
	if err := cl.call(&info, "ledger.getAccountInfoByAddress", address); err != nil {
		return nil, err
	}
	if info == nil || info.BalanceInfoMap == nil {
		return big.NewInt(0), nil
	}
	b, ok := info.BalanceInfoMap[token]
	if !ok || b.Balance == nil {
		return big.NewInt(0), nil
	}
	return b.Balance, nil
}

func (cl *Client) frontierAccount(address types.Address) (*api.AccountBlock, error) {
	var block *api.AccountBlock
	if err := cl.call(&block, "ledger.getFrontierAccountBlock", address); err != nil {
		return nil, err
	}
	return block, nil
}

func (cl *Client) requiredPlasma(address types.Address, blockType uint64, to *types.Address, data []byte) (uint64, error) {
	var result embeddedapi.GetRequiredResult
	err := cl.call(&result, "embedded.plasma.getRequiredPoWForAccountBlock", embeddedapi.GetRequiredParam{
		SelfAddr: address, BlockType: blockType, ToAddr: to, Data: data,
	})
	if err != nil {
		return 0, err
	}
	if result.RequiredDifficulty != 0 {
		return 0, fmt.Errorf("account %s would need proof of work (difficulty %d); the devnet accounts are fused", address, result.RequiredDifficulty)
	}
	return result.BasePlasma, nil
}

func (cl *Client) newBlock(address types.Address, blockType uint64) (*nom.AccountBlock, error) {
	momentum, err := cl.FrontierMomentum()
	if err != nil {
		return nil, err
	}
	block := &nom.AccountBlock{
		Version:              1,
		ChainIdentifier:      momentum.ChainIdentifier,
		BlockType:            blockType,
		MomentumAcknowledged: momentum.Identifier(),
		Address:              address,
		Amount:               big.NewInt(0),
		TokenStandard:        types.ZeroTokenStandard,
		DescendantBlocks:     make([]*nom.AccountBlock, 0),
	}
	frontier, err := cl.frontierAccount(address)
	if err != nil {
		return nil, err
	}
	if frontier != nil {
		block.PreviousHash = frontier.Hash
		block.Height = frontier.Height + 1
	} else {
		block.Height = 1
	}
	return block, nil
}

func (cl *Client) publish(block *nom.AccountBlock, key *wallet.KeyPair) (types.Hash, error) {
	block.Hash = block.ComputeHash()
	block.PublicKey = key.Public
	block.Signature = key.Sign(block.Hash.Bytes())
	err := cl.call(nil, "ledger.publishRawTransaction", &api.AccountBlock{AccountBlock: *block})
	return block.Hash, err
}

// Send publishes a user send block.
func (cl *Client) Send(key *wallet.KeyPair, to types.Address, amount *big.Int, token types.ZenonTokenStandard, data []byte) (types.Hash, error) {
	block, err := cl.newBlock(key.Address, nom.BlockTypeUserSend)
	if err != nil {
		return types.Hash{}, err
	}
	block.ToAddress = to
	block.Amount = new(big.Int).Set(amount)
	block.TokenStandard = token
	block.Data = append([]byte(nil), data...)
	if block.FusedPlasma, err = cl.requiredPlasma(key.Address, nom.BlockTypeUserSend, &to, data); err != nil {
		return types.Hash{}, err
	}
	return cl.publish(block, key)
}

// Receive publishes a receive block for one unreceived send.
func (cl *Client) Receive(key *wallet.KeyPair, from types.Hash) (types.Hash, error) {
	block, err := cl.newBlock(key.Address, nom.BlockTypeUserReceive)
	if err != nil {
		return types.Hash{}, err
	}
	block.FromBlockHash = from
	if block.FusedPlasma, err = cl.requiredPlasma(key.Address, nom.BlockTypeUserReceive, nil, nil); err != nil {
		return types.Hash{}, err
	}
	return cl.publish(block, key)
}

// ReceiveAll receives everything pending for key and returns how many.
func (cl *Client) ReceiveAll(key *wallet.KeyPair) (int, error) {
	var list *api.AccountBlockList
	if err := cl.call(&list, "ledger.getUnreceivedBlocksByAddress", key.Address, 0, 50); err != nil {
		return 0, err
	}
	n := 0
	if list == nil {
		return 0, nil
	}
	for _, b := range list.List {
		if _, err := cl.Receive(key, b.Hash); err != nil {
			return n, err
		}
		n++
		if _, err := cl.WaitBlock(b.Hash); err != nil {
			return n, err
		}
	}
	return n, nil
}

// WaitBlock waits until hash is in a momentum.
func (cl *Client) WaitBlock(hash types.Hash) (*api.AccountBlock, error) {
	deadline := time.Now().Add(defaultBlockWait)
	for {
		var block *api.AccountBlock
		err := cl.call(&block, "ledger.getAccountBlockByHash", hash)
		if err == nil && block != nil && block.ConfirmationDetail != nil && block.ConfirmationDetail.NumConfirmations >= 1 {
			return block, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("block %s not confirmed in time (%v)", hash, err)
		}
		time.Sleep(defaultPoll)
	}
}

// WaitContract waits for the contract's receive of sendHash and returns
// whether the contract accepted the call.
func (cl *Client) WaitContract(sendHash types.Hash) (bool, error) {
	deadline := time.Now().Add(defaultEntryWait)
	for {
		var block *api.AccountBlock
		err := cl.call(&block, "ledger.getAccountBlockByHash", sendHash)
		if err == nil && block != nil && block.PairedAccountBlock != nil {
			// the contract's receive block carries the method's status as
			// eight bytes of data: 1 is success, 2 is failure
			paired := block.PairedAccountBlock
			if paired.BlockType == nom.BlockTypeContractReceive && len(paired.Data) == 8 {
				return common.BytesToUint64(paired.Data) == contractSuccess, nil
			}
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("contract never received %s", sendHash)
		}
		time.Sleep(defaultPoll)
	}
}

// ---- ptlc -----------------------------------------------------------------

// CreatePoint locks amount of token to the compressed point, payable to
// destination, until expiry. It returns the entry id (the send's hash).
func (cl *Client) CreatePoint(key *wallet.KeyPair, point []byte, destination types.Address, expiry int64, token types.ZenonTokenStandard, amount *big.Int) (types.Hash, error) {
	data, err := definition.ABIPtlc.PackMethod(definition.CreatePtlcMethodName, expiry, PointTypeScalar, point, destination)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PtlcContract, amount, token, data)
}

// Unlock opens an entry to the caller with the witness.
func (cl *Client) Unlock(key *wallet.KeyPair, id types.Hash, witness []byte) (types.Hash, error) {
	data, err := definition.ABIPtlc.PackMethod(definition.UnlockPtlcMethodName, id, witness)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PtlcContract, big.NewInt(0), types.ZnnTokenStandard, data)
}

// Reclaim returns an expired entry to its creator.
func (cl *Client) Reclaim(key *wallet.KeyPair, id types.Hash) (types.Hash, error) {
	data, err := definition.ABIPtlc.PackMethod(definition.ReclaimPtlcMethodName, id)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PtlcContract, big.NewInt(0), types.ZnnTokenStandard, data)
}

// GetEntry reads an entry; a nil entry means it does not exist.
func (cl *Client) GetEntry(id types.Hash) (*definition.PtlcInfo, error) {
	var info *definition.PtlcInfo
	err := cl.call(&info, "embedded.ptlc.getById", id)
	if err != nil {
		if bytes.Contains([]byte(err.Error()), []byte("non existent")) {
			return nil, nil
		}
		return nil, err
	}
	return info, nil
}

// WaitEntry waits until the entry exists and the contract has stored it.
func (cl *Client) WaitEntry(id types.Hash) (*definition.PtlcInfo, error) {
	deadline := time.Now().Add(defaultEntryWait)
	for {
		info, err := cl.GetEntry(id)
		if err == nil && info != nil {
			return info, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("entry %s never appeared (%v)", id, err)
		}
		time.Sleep(defaultPoll)
	}
}

// ContractHeight is the height of the contract's account chain.
func (cl *Client) ContractHeight() (uint64, error) {
	f, err := cl.frontierAccount(types.PtlcContract)
	if err != nil {
		return 0, err
	}
	if f == nil {
		return 0, nil
	}
	return f.Height, nil
}

// ScanWitness walks the contract's account chain from height from and returns
// the witness of the first Unlock or ProxyUnlock of id that the contract
// accepted. It scans the contract, not the payee: a proxy unlock sits on the
// submitter's chain, and the fixed destination means the submitter can be
// anyone.
func (cl *Client) ScanWitness(id types.Hash, from uint64) ([]byte, uint64, error) {
	head, err := cl.ContractHeight()
	if err != nil {
		return nil, from, err
	}
	for h := from + 1; h <= head; h++ {
		var list *api.AccountBlockList
		if err := cl.call(&list, "ledger.getAccountBlocksByHeight", types.PtlcContract, h, 1); err != nil {
			return nil, h - 1, err
		}
		if list == nil {
			continue
		}
		for _, b := range list.List {
			if b.PairedAccountBlock == nil || len(b.Data) != 8 || common.BytesToUint64(b.Data) != contractSuccess {
				continue
			}
			send := b.PairedAccountBlock
			if w := witnessFor(id, send.Data); w != nil {
				return w, h, nil
			}
		}
	}
	return nil, head, nil
}

func witnessFor(id types.Hash, data []byte) []byte {
	var unlock definition.UnlockPtlcParam
	if err := definition.ABIPtlc.UnpackMethod(&unlock, definition.UnlockPtlcMethodName, data); err == nil && unlock.Id == id {
		return unlock.Signature
	}
	var proxy definition.ProxyUnlockPtlcParam
	if err := definition.ABIPtlc.UnpackMethod(&proxy, definition.ProxyUnlockPtlcMethodName, data); err == nil && proxy.Id == id {
		return proxy.Signature
	}
	return nil
}

// WaitWitness polls ScanWitness until the entry's witness appears, the
// deadline passes, or the context ends.
func (cl *Client) WaitWitness(ctx context.Context, id types.Hash, from uint64, until time.Time) ([]byte, error) {
	cursor := from
	for {
		w, h, err := cl.ScanWitness(id, cursor)
		if err != nil {
			return nil, err
		}
		cursor = h
		if w != nil {
			return w, nil
		}
		if time.Now().After(until) {
			return nil, errors.New("no unlock seen before the deadline")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(defaultPoll):
		}
	}
}

// Summary formats an entry for the log.
func Summary(info *definition.PtlcInfo) string {
	return fmt.Sprintf("id %s %s %s to %s until %d, lock %x", info.Id, info.Amount, info.TokenStandard, info.Destination, info.ExpirationTime, info.PointLock)
}

// ---- what the flows runner adds --------------------------------------------
// cmd/flows makes every call the contract has, with every lock type, and
// checks what the contract did with the funds. nom-to-btc itself needs none
// of this.

const (
	PointTypeEd25519 = definition.PointTypeED25519
	PointTypeBIP340  = definition.PointTypeBIP340
)

// Create locks amount of token to lock, of any point type, until expiry. The
// zero destination leaves the choice to the witness, which only the key types
// allow. It returns the entry id (the send's hash).
func (cl *Client) Create(key *wallet.KeyPair, pointType uint8, lock []byte, destination types.Address, expiry int64, token types.ZenonTokenStandard, amount *big.Int) (types.Hash, error) {
	data, err := definition.ABIPtlc.PackMethod(definition.CreatePtlcMethodName, expiry, pointType, lock, destination)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PtlcContract, amount, token, data)
}

// ProxyUnlock opens an entry to destination, sent by anyone.
func (cl *Client) ProxyUnlock(key *wallet.KeyPair, id types.Hash, destination types.Address, witness []byte) (types.Hash, error) {
	data, err := definition.ABIPtlc.PackMethod(definition.ProxyUnlockPtlcMethodName, id, destination, witness)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PtlcContract, big.NewInt(0), types.ZnnTokenStandard, data)
}

// UnlockMessage is the digest a key-type witness signs: it binds the chain,
// the lock type, the entry and the address to pay.
func (cl *Client) UnlockMessage(pointType uint8, id types.Hash, destination types.Address) ([]byte, error) {
	chain, err := cl.ChainIdentifier()
	if err != nil {
		return nil, err
	}
	return definition.GetPtlcUnlockMessage(chain, pointType, id, destination), nil
}

// Block reads one account block, with its descendants.
func (cl *Client) Block(hash types.Hash) (*api.AccountBlock, error) {
	var block *api.AccountBlock
	if err := cl.call(&block, "ledger.getAccountBlockByHash", hash); err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block %s is not on the node", hash)
	}
	return block, nil
}

// Paid is one transfer the contract made while answering a call.
type Paid struct {
	To     types.Address
	Amount *big.Int
	Token  types.ZenonTokenStandard
}

// ContractPaid lists what the contract sent out in its answer to sendHash: the
// payout of an unlock, the return of a reclaim, or the refund of a refused
// Create. The call must already have been answered.
func (cl *Client) ContractPaid(sendHash types.Hash) ([]Paid, error) {
	send, err := cl.Block(sendHash)
	if err != nil {
		return nil, err
	}
	if send.PairedAccountBlock == nil {
		return nil, fmt.Errorf("the contract has not answered %s", sendHash)
	}
	receive, err := cl.Block(send.PairedAccountBlock.Hash)
	if err != nil {
		return nil, err
	}
	var out []Paid
	for _, d := range receive.DescendantBlocks {
		if d.Amount != nil && d.Amount.Sign() > 0 {
			out = append(out, Paid{To: d.ToAddress, Amount: d.Amount, Token: d.TokenStandard})
		}
	}
	return out, nil
}

// Plasma is the plasma an address can spend now, and the most it can hold.
func (cl *Client) Plasma(address types.Address) (current, max uint64, err error) {
	var info struct {
		CurrentPlasma uint64 `json:"currentPlasma"`
		MaxPlasma     uint64 `json:"maxPlasma"`
	}
	if err = cl.call(&info, "embedded.plasma.get", address); err != nil {
		return 0, 0, err
	}
	return info.CurrentPlasma, info.MaxPlasma, nil
}

// Fuse locks qsr of key's QSR as plasma for beneficiary.
func (cl *Client) Fuse(key *wallet.KeyPair, beneficiary types.Address, qsr *big.Int) (types.Hash, error) {
	data, err := definition.ABIPlasma.PackMethod(definition.FuseMethodName, beneficiary)
	if err != nil {
		return types.Hash{}, err
	}
	return cl.Send(key, types.PlasmaContract, qsr, types.QsrTokenStandard, data)
}

// Attempt is one Unlock or ProxyUnlock of an entry as the contract's chain
// shows it. A refused one carries its witness as plainly as an accepted one.
type Attempt struct {
	Height   uint64
	From     types.Address
	Accepted bool
	Witness  []byte
}

// ScanAttempts lists every Unlock and ProxyUnlock of id that the contract
// answered above height from, refused ones included. ScanWitness wants the
// one that opened the entry; this is what a counterparty reads to learn that
// a call it lost to was refused, and what it gave away anyway.
func (cl *Client) ScanAttempts(id types.Hash, from uint64) ([]Attempt, error) {
	head, err := cl.ContractHeight()
	if err != nil {
		return nil, err
	}
	var out []Attempt
	for h := from + 1; h <= head; h++ {
		var list *api.AccountBlockList
		if err := cl.call(&list, "ledger.getAccountBlocksByHeight", types.PtlcContract, h, 1); err != nil {
			return nil, err
		}
		if list == nil {
			continue
		}
		for _, b := range list.List {
			if b.PairedAccountBlock == nil || len(b.Data) != 8 {
				continue
			}
			if w := witnessFor(id, b.PairedAccountBlock.Data); w != nil {
				out = append(out, Attempt{Height: h, From: b.PairedAccountBlock.Address, Accepted: common.BytesToUint64(b.Data) == contractSuccess, Witness: w})
			}
		}
	}
	return out, nil
}

// ---- what the key swap's client adds -----------------------------------------
// keyswap drives one party in steps that never block, and has to find its way
// again after a crash, so it asks rather than waits.

// Wrap is a Client over a connection made elsewhere: an in-process server in
// keyswap's tests.
func Wrap(c *rpcclient.Client, log func(string, ...interface{})) *Client {
	return &Client{c: c, log: log}
}

// AccountHeight is the height of an account's chain, unconfirmed blocks
// included.
func (cl *Client) AccountHeight(address types.Address) (uint64, error) {
	f, err := cl.frontierAccount(address)
	if err != nil || f == nil {
		return 0, err
	}
	return f.Height, nil
}

// Answered reports whether the contract has answered a call yet and, if it
// has, whether it accepted it. WaitContract is this in a loop.
func (cl *Client) Answered(sendHash types.Hash) (done, accepted bool, err error) {
	var block *api.AccountBlock
	if err := cl.call(&block, "ledger.getAccountBlockByHash", sendHash); err != nil {
		return false, false, err
	}
	if block == nil || block.PairedAccountBlock == nil {
		return false, false, nil
	}
	paired := block.PairedAccountBlock
	if paired.BlockType != nom.BlockTypeContractReceive || len(paired.Data) != 8 {
		return false, false, nil
	}
	return true, common.BytesToUint64(paired.Data) == contractSuccess, nil
}

// FindCreate looks on creator's own chain, above height since, for a Create
// locked to lock, and returns its hash, which is the entry's id. A client that
// stopped between publishing a Create and writing its id down finds it here
// rather than locking the funds a second time. The lock is a key or a point
// made for one swap, so a match is that swap's Create and no other.
func (cl *Client) FindCreate(creator types.Address, lock []byte, since uint64) (types.Hash, bool, error) {
	head, err := cl.AccountHeight(creator)
	if err != nil {
		return types.Hash{}, false, err
	}
	for h := since + 1; h <= head; h++ {
		var list *api.AccountBlockList
		if err := cl.call(&list, "ledger.getAccountBlocksByHeight", creator, h, 1); err != nil {
			return types.Hash{}, false, err
		}
		if list == nil {
			continue
		}
		for _, b := range list.List {
			if b.ToAddress != types.PtlcContract {
				continue
			}
			var create definition.CreatePtlcParam
			if err := definition.ABIPtlc.UnpackMethod(&create, definition.CreatePtlcMethodName, b.Data); err == nil && bytes.Equal(create.PointLock, lock) {
				return b.Hash, true, nil
			}
		}
	}
	return types.Hash{}, false, nil
}
