package tests

import (
	"bytes"
	"math/big"
	"strings"
	"testing"
	"time"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/verifier"
	"github.com/zenon-network/go-zenon/vm"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/embedded/implementation"
	"github.com/zenon-network/go-zenon/vm/vm_context"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

var (
	ownershipPreimage      = bytes.Repeat([]byte{0x5a}, 32)
	ownershipOtherPreimage = bytes.Repeat([]byte{0xa5}, 32)
)

func createAndActivateSpork(t *testing.T, z mock.MockZenon, name string) types.Hash {
	sporkAPI := embedded.NewSporkApi(z)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data:      definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName, name, name),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkList, err := sporkAPI.GetAll(0, 10)
	common.FailIfErr(t, err)
	var id types.Hash
	for _, spork := range sporkList.List {
		if spork.Name == name {
			id = spork.Id
		}
	}
	common.ExpectTrue(t, !id.IsZero())

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data:      definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName, id),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	return id
}

func activatePillarOwnership(t *testing.T, z mock.MockZenon) {
	saveSporkState(t)
	id := createAndActivateSpork(t, z, "spork-pillar-ownership")
	types.PillarOwnershipSpork.SporkId = id
	types.ImplementedSporksMap[id] = true
	z.InsertMomentumsTo(frontierHeight(z) + 2*constants.SporkMinHeightDelay)
}

func setRevokeWindow(t *testing.T, lockTime, revokeTime int64) {
	savedLock := constants.PillarEpochLockTime
	savedRevoke := constants.PillarEpochRevokeTime
	t.Cleanup(func() {
		constants.PillarEpochLockTime = savedLock
		constants.PillarEpochRevokeTime = savedRevoke
	})
	constants.PillarEpochLockTime = lockTime
	constants.PillarEpochRevokeTime = revokeTime
}

func frontierHeight(z mock.MockZenon) uint64 {
	return z.Chain().GetFrontierMomentumStore().Identifier().Height
}

func lockOwnershipBlock(owner types.Address, name string, buyer types.Address, duration uint64, hashType uint8, hashLock []byte) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:   owner,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.LockOwnershipMethodName, name, buyer, duration, hashType, hashLock),
	}
}

func takeOwnershipBlock(buyer types.Address, name string, preimage []byte) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:   buyer,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.TakeOwnershipMethodName, name, preimage),
	}
}

func releaseOwnershipLockBlock(sender types.Address, name string) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:   sender,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.ReleaseOwnershipLockMethodName, name),
	}
}

func revokePillarBlock(sender types.Address, name string) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:   sender,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.RevokeMethodName, name),
	}
}

func callAndExpect(t *testing.T, z mock.MockZenon, block *nom.AccountBlock, expected error) {
	t.Helper()
	expecter := z.CallContract(block)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	expecter.Error(t, expected)
}

func expectOwner(t *testing.T, z mock.MockZenon, name string, owner types.Address) {
	t.Helper()
	pillar, err := embedded.NewPillarApi(z, true).GetByName(name)
	common.FailIfErr(t, err)
	common.ExpectTrue(t, pillar != nil)
	common.ExpectString(t, pillar.StakeAddress.String(), owner.String())
}

func getOwnershipLock(t *testing.T, z mock.MockZenon, name string) *definition.PillarOwnershipLock {
	t.Helper()
	lock, err := embedded.NewPillarApi(z, true).GetOwnershipLock(name)
	common.FailIfErr(t, err)
	return lock
}

func storedOwnershipLock(t *testing.T, z mock.MockZenon, name string) (*definition.PillarOwnershipLock, error) {
	t.Helper()
	store := z.Chain().GetFrontierAccountStore(types.PillarContract)
	return definition.GetPillarOwnershipLock(store.Storage(), name)
}

func TestPillarOwnership_SporkGating(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	z.InsertMomentumsTo(10)

	hashLock := crypto.HashSHA256(ownershipPreimage)
	z.InsertSendBlock(lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock),
		constants.ErrContractMethodNotFound, mock.SkipVmChanges)
	z.InsertSendBlock(takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage),
		constants.ErrContractMethodNotFound, mock.SkipVmChanges)
	z.InsertSendBlock(releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name),
		constants.ErrContractMethodNotFound, mock.SkipVmChanges)

	activatePillarOwnership(t, z)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), nil)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) != nil)
}

func TestPillarOwnership_LockStatic(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	hashLock := crypto.HashSHA256(ownershipPreimage)
	valid := func() *nom.AccountBlock {
		return lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock)
	}
	withValue := valid()
	withValue.TokenStandard = types.ZnnTokenStandard
	withValue.Amount = big.NewInt(1)

	cases := []struct {
		name     string
		block    *nom.AccountBlock
		expected error
	}{
		{"unpack", &nom.AccountBlock{Address: g.Pillar1.Address, ToAddress: types.PillarContract, Data: valid().Data[:40]}, constants.ErrUnpackError},
		{"name", lockOwnershipBlock(g.Pillar1.Address, "not a valid name", g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrInvalidName},
		{"amount", withValue, constants.ErrInvalidTokenOrAmount},
		{"hash type", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, 2, hashLock), constants.ErrInvalidHashType},
		{"hash digest", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock[:31]), constants.ErrInvalidHashDigest},
		{"zero buyer", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, types.ZeroAddress, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrForbiddenParam},
		{"embedded buyer", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, types.HtlcContract, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrPermissionDenied},
		{"too short", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration-1, definition.HashTypeSHA256, hashLock), constants.ErrInvalidExpirationTime},
		{"too long", lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMaxDuration+1, definition.HashTypeSHA256, hashLock), constants.ErrInvalidExpirationTime},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			z.InsertSendBlock(c.block, c.expected, mock.SkipVmChanges)
		})
	}

	z.InsertNewMomentum()
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMaxDuration, definition.HashTypeSHA3, crypto.Hash(ownershipPreimage)), nil)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) != nil)
}

func TestPillarOwnership_LockReceive(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, `
t=2001-09-09T01:46:50+0000 lvl=dbug msg=created module=embedded contract=spork spork="&{Id:592a36615298ad159fdfa67c99b2ad3dafb6dfb224c37b8739d50952ca9a2be9 Name:spork-pillar-ownership Description:spork-pillar-ownership Activated:false EnforcementHeight:0}"
t=2001-09-09T01:47:00+0000 lvl=dbug msg=activated module=embedded contract=spork spork="&{Id:592a36615298ad159fdfa67c99b2ad3dafb6dfb224c37b8739d50952ca9a2be9 Name:spork-pillar-ownership Description:spork-pillar-ownership Activated:true EnforcementHeight:9}"
t=2001-09-09T01:50:10+0000 lvl=dbug msg="locked pillar ownership" module=embedded contract=pillar pillar-name=TEST-pillar-1 owner=z1qqq43dyrswfehx9w9td43exflqzcxrt7g6alah buyer=z1qqaswvt0e3cc5sm7lygkyza9ra63cr8e6zre09 height=22 expiration-height=742
`)
	activatePillarOwnership(t, z)

	hashLock := crypto.HashSHA256(ownershipPreimage)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, "no-such-pillar", g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrDataNonExistent)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar2.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrPermissionDenied)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.Pillar1.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrForbiddenParam)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)

	expecter := z.CallContract(lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock))
	z.InsertNewMomentum()
	receiveHeight := frontierHeight(z)
	z.InsertNewMomentum()
	expecter.Error(t, nil)

	lock := getOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectTrue(t, lock != nil)
	common.ExpectUint64(t, lock.ExpirationHeight, receiveHeight+constants.PillarLockMinDuration)
	common.Json(lock, nil).Equals(t, `
{
	"name": "TEST-pillar-1",
	"buyer": "z1qqaswvt0e3cc5sm7lygkyza9ra63cr8e6zre09",
	"expirationHeight": 742,
	"hashType": 1,
	"hashLock": "YL8HxIiq0Y/aM53wfk+8R7TwC+cXEZNvGNBNNSrQGJA="
}`)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User4.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrPillarOwnershipLocked)
	common.ExpectString(t, getOwnershipLock(t, z, g.Pillar1Name).Buyer.String(), g.User5.Address.String())
	expectOwner(t, z, g.Pillar1Name, g.Pillar1.Address)
}

func testPillarOwnershipTake(t *testing.T, hashType uint8, hash func(...[]byte) []byte) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), constants.ErrDataNonExistent)
	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, "no-such-pillar", ownershipPreimage), constants.ErrDataNonExistent)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, hashType, hash(ownershipPreimage)), nil)

	z.InsertSendBlock(takeOwnershipBlock(g.User5.Address, g.Pillar1Name, bytes.Repeat([]byte{1}, constants.PillarLockPreimageMaxSize+1)),
		constants.ErrInvalidPreimage, mock.SkipVmChanges)
	withValue := takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage)
	withValue.TokenStandard = types.ZnnTokenStandard
	withValue.Amount = big.NewInt(1)
	z.InsertSendBlock(withValue, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)

	callAndExpect(t, z, takeOwnershipBlock(g.User4.Address, g.Pillar1Name, ownershipPreimage), constants.ErrPermissionDenied)
	callAndExpect(t, z, takeOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, ownershipPreimage), constants.ErrPermissionDenied)
	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipOtherPreimage), constants.ErrInvalidPreimage)
	expectOwner(t, z, g.Pillar1Name, g.Pillar1.Address)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) != nil)

	before, err := embedded.NewPillarApi(z, true).GetByName(g.Pillar1Name)
	common.FailIfErr(t, err)

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), nil)
	expectOwner(t, z, g.Pillar1Name, g.User5.Address)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)
	_, err = storedOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectError(t, err, constants.ErrDataNonExistent)

	after, err := embedded.NewPillarApi(z, true).GetByName(g.Pillar1Name)
	common.FailIfErr(t, err)
	common.ExpectString(t, after.BlockProducingAddress.String(), before.BlockProducingAddress.String())
	common.ExpectString(t, after.RewardWithdrawAddress.String(), before.RewardWithdrawAddress.String())
	common.ExpectTrue(t, after.GiveMomentumRewardPercentage == before.GiveMomentumRewardPercentage)
	common.ExpectTrue(t, after.GiveDelegateRewardPercentage == before.GiveDelegateRewardPercentage)
	common.ExpectTrue(t, after.Weight.Cmp(before.Weight) == 0)

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), constants.ErrDataNonExistent)

	updateBlock := func(sender types.Address) *nom.AccountBlock {
		return &nom.AccountBlock{
			Address:   sender,
			ToAddress: types.PillarContract,
			Data:      definition.ABIPillars.PackMethodPanic(definition.UpdatePillarMethodName, g.Pillar1Name, g.Pillar1.Address, g.User5.Address, uint8(10), uint8(90)),
		}
	}
	callAndExpect(t, z, updateBlock(g.Pillar1.Address), constants.ErrPermissionDenied)
	callAndExpect(t, z, updateBlock(g.User5.Address), nil)
	updated, err := embedded.NewPillarApi(z, true).GetByName(g.Pillar1Name)
	common.FailIfErr(t, err)
	common.ExpectString(t, updated.RewardWithdrawAddress.String(), g.User5.Address.String())

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User4.Address, constants.PillarLockMinDuration, hashType, hash(ownershipPreimage)), constants.ErrPermissionDenied)
	callAndExpect(t, z, lockOwnershipBlock(g.User5.Address, g.Pillar1Name, g.User4.Address, constants.PillarLockMinDuration, hashType, hash(ownershipOtherPreimage)), nil)
}

func TestPillarOwnership_TakeSHA256(t *testing.T) {
	testPillarOwnershipTake(t, definition.HashTypeSHA256, crypto.HashSHA256)
}

func TestPillarOwnership_TakeSHA3(t *testing.T) {
	testPillarOwnershipTake(t, definition.HashTypeSHA3, crypto.Hash)
}

func TestPillarOwnership_Expiry(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	hashLock := crypto.HashSHA256(ownershipPreimage)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), nil)
	expiration := getOwnershipLock(t, z, g.Pillar1Name).ExpirationHeight

	z.InsertMomentumsTo(expiration - 1)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) != nil)

	expecter := z.CallContract(takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage))
	z.InsertNewMomentum()
	common.ExpectUint64(t, frontierHeight(z), expiration)
	z.InsertNewMomentum()
	expecter.Error(t, constants.ErrExpired)
	expectOwner(t, z, g.Pillar1Name, g.Pillar1.Address)

	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)
	stored, err := storedOwnershipLock(t, z, g.Pillar1Name)
	common.FailIfErr(t, err)
	common.ExpectUint64(t, stored.ExpirationHeight, expiration)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User4.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipOtherPreimage)), nil)
	relocked := getOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectString(t, relocked.Buyer.String(), g.User4.Address.String())
	common.ExpectTrue(t, relocked.ExpirationHeight > expiration)

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), constants.ErrPermissionDenied)
	callAndExpect(t, z, takeOwnershipBlock(g.User4.Address, g.Pillar1Name, ownershipPreimage), constants.ErrInvalidPreimage)
}

func TestPillarOwnership_TakeLastMomentum(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	expiration := getOwnershipLock(t, z, g.Pillar1Name).ExpirationHeight

	z.InsertMomentumsTo(expiration - 2)
	expecter := z.CallContract(takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage))
	z.InsertNewMomentum()
	common.ExpectUint64(t, frontierHeight(z), expiration-1)
	z.InsertNewMomentum()
	expecter.Error(t, nil)
	expectOwner(t, z, g.Pillar1Name, g.User5.Address)
}

func TestPillarOwnership_Release(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name), constants.ErrDataNonExistent)

	withValue := releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name)
	withValue.TokenStandard = types.ZnnTokenStandard
	withValue.Amount = big.NewInt(1)
	z.InsertSendBlock(withValue, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertSendBlock(releaseOwnershipLockBlock(g.User5.Address, "not a valid name"), constants.ErrInvalidName, mock.SkipVmChanges)

	hashLock := crypto.HashSHA256(ownershipPreimage)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMaxDuration, definition.HashTypeSHA256, hashLock), nil)

	before, err := embedded.NewPillarApi(z, true).GetByName(g.Pillar1Name)
	common.FailIfErr(t, err)

	callAndExpect(t, z, releaseOwnershipLockBlock(g.Pillar1.Address, g.Pillar1Name), constants.ErrPermissionDenied)
	callAndExpect(t, z, releaseOwnershipLockBlock(g.User4.Address, g.Pillar1Name), constants.ErrPermissionDenied)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) != nil)

	callAndExpect(t, z, releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name), nil)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)
	_, err = storedOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectError(t, err, constants.ErrDataNonExistent)

	after, err := embedded.NewPillarApi(z, true).GetByName(g.Pillar1Name)
	common.FailIfErr(t, err)
	common.ExpectString(t, after.StakeAddress.String(), before.StakeAddress.String())
	common.ExpectString(t, after.BlockProducingAddress.String(), before.BlockProducingAddress.String())
	common.ExpectString(t, after.RewardWithdrawAddress.String(), before.RewardWithdrawAddress.String())

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), constants.ErrDataNonExistent)
	expectOwner(t, z, g.Pillar1Name, g.Pillar1.Address)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User4.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), nil)
	common.ExpectString(t, getOwnershipLock(t, z, g.Pillar1Name).Buyer.String(), g.User4.Address.String())
}

func TestPillarOwnership_ReleaseExpired(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	expiration := getOwnershipLock(t, z, g.Pillar1Name).ExpirationHeight
	z.InsertMomentumsTo(expiration + 1)

	_, err := storedOwnershipLock(t, z, g.Pillar1Name)
	common.FailIfErr(t, err)
	callAndExpect(t, z, releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name), nil)
	_, err = storedOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectError(t, err, constants.ErrDataNonExistent)
}

func TestPillarOwnership_RevokeFreeze(t *testing.T) {
	setRevokeWindow(t, 0, 365*constants.SecsInDay)
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	hashLock := crypto.HashSHA256(ownershipPreimage)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), nil)

	callAndExpect(t, z, revokePillarBlock(g.Pillar1.Address, g.Pillar1Name), constants.ErrPillarOwnershipLocked)
	expectOwner(t, z, g.Pillar1Name, g.Pillar1.Address)
	z.ExpectBalance(g.Pillar1.Address, types.ZnnTokenStandard, 1000*g.Zexp)

	callAndExpect(t, z, revokePillarBlock(g.User5.Address, g.Pillar1Name), constants.ErrPermissionDenied)

	callAndExpect(t, z, releaseOwnershipLockBlock(g.User5.Address, g.Pillar1Name), nil)
	callAndExpect(t, z, revokePillarBlock(g.Pillar1.Address, g.Pillar1Name), nil)
	autoreceive(t, z, g.Pillar1.Address)
	z.InsertNewMomentum()
	z.ExpectBalance(g.Pillar1.Address, types.ZnnTokenStandard, 16000*g.Zexp)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), constants.ErrNotActive)
}

func TestPillarOwnership_RevokeAfterExpiry(t *testing.T) {
	setRevokeWindow(t, 0, 365*constants.SecsInDay)
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	expiration := getOwnershipLock(t, z, g.Pillar1Name).ExpirationHeight

	z.InsertMomentumsTo(expiration - 2)
	expecter := z.CallContract(revokePillarBlock(g.Pillar1.Address, g.Pillar1Name))
	z.InsertNewMomentum()
	common.ExpectUint64(t, frontierHeight(z), expiration-1)
	z.InsertNewMomentum()
	expecter.Error(t, constants.ErrPillarOwnershipLocked)

	expecter = z.CallContract(revokePillarBlock(g.Pillar1.Address, g.Pillar1Name))
	z.InsertNewMomentum()
	common.ExpectUint64(t, frontierHeight(z), expiration+1)
	z.InsertNewMomentum()
	expecter.Error(t, nil)

	callAndExpect(t, z, takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage), constants.ErrNotActive)
}

func TestPillarOwnership_RevokeNotDueWinsOverFreeze(t *testing.T) {
	setRevokeWindow(t, 365*constants.SecsInDay, constants.SecsInDay)
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, revokePillarBlock(g.Pillar1.Address, g.Pillar1Name), constants.RevokeNotDue)
	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	callAndExpect(t, z, revokePillarBlock(g.Pillar1.Address, g.Pillar1Name), constants.RevokeNotDue)
}

func TestPillarOwnership_RevokeWithoutSporkIgnoresStoredLock(t *testing.T) {
	setRevokeWindow(t, 0, 365*constants.SecsInDay)
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	z.InsertMomentumsTo(10)

	callAndExpect(t, z, revokePillarBlock(g.Pillar1.Address, g.Pillar1Name), nil)
}

func TestPillarOwnership_SameChainSwap(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateHtlc(t, z)
	activatePillarOwnership(t, z)

	seller := g.Pillar1.Address
	buyer := g.User1.Address
	price := big.NewInt(5000 * g.Zexp)
	hashLock := crypto.HashSHA256(ownershipPreimage)

	callAndExpect(t, z, lockOwnershipBlock(seller, g.Pillar1Name, buyer, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock), nil)
	lock := getOwnershipLock(t, z, g.Pillar1Name)
	common.ExpectTrue(t, lock != nil)

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	create := &nom.AccountBlock{
		Address:   buyer,
		ToAddress: types.HtlcContract,
		Data: definition.ABIHtlc.PackMethodPanic(definition.CreateHtlcMethodName,
			seller,
			frontier.Timestamp.Unix()+3600,
			lock.HashType,
			uint8(constants.PillarLockPreimageMaxSize),
			lock.HashLock,
		),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        price,
	}
	createExpecter := z.CallContract(create)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	createExpecter.Error(t, nil)
	z.ExpectBalance(buyer, types.ZnnTokenStandard, 7000*g.Zexp)
	z.ExpectBalance(types.HtlcContract, types.ZnnTokenStandard, 5000*g.Zexp)

	htlcs, err := embedded.NewHtlcApi(z).GetById(create.Hash)
	common.FailIfErr(t, err)
	common.ExpectString(t, htlcs.HashLocked.String(), seller.String())
	common.ExpectTrue(t, bytes.Equal(htlcs.HashLock, lock.HashLock))
	common.ExpectTrue(t, int(htlcs.KeyMaxSize) >= len(ownershipPreimage))

	callAndExpect(t, z, &nom.AccountBlock{
		Address:   seller,
		ToAddress: types.HtlcContract,
		Data:      definition.ABIHtlc.PackMethodPanic(definition.UnlockHtlcMethodName, create.Hash, ownershipPreimage),
	}, nil)
	autoreceive(t, z, seller)
	z.InsertNewMomentum()
	z.ExpectBalance(seller, types.ZnnTokenStandard, 6000*g.Zexp)
	z.ExpectBalance(types.HtlcContract, types.ZnnTokenStandard, 0)
	expectOwner(t, z, g.Pillar1Name, seller)

	callAndExpect(t, z, takeOwnershipBlock(buyer, g.Pillar1Name, ownershipPreimage), nil)
	expectOwner(t, z, g.Pillar1Name, buyer)
	common.ExpectTrue(t, getOwnershipLock(t, z, g.Pillar1Name) == nil)
	z.ExpectBalance(types.PillarContract, types.ZnnTokenStandard, 45000*g.Zexp)
}

func TestPillarOwnership_RewardSeam(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()
	activatePillarOwnership(t, z)
	pillarApi := embedded.NewPillarApi(z, true)

	seller := g.Pillar1.Address
	buyer := g.Pillar4.Address
	callAndExpect(t, z, lockOwnershipBlock(seller, g.Pillar1Name, buyer, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	callAndExpect(t, z, takeOwnershipBlock(buyer, g.Pillar1Name, ownershipPreimage), nil)
	expectOwner(t, z, g.Pillar1Name, buyer)
	common.ExpectTrue(t, frontierHeight(z) < 360)

	z.InsertMomentumsTo(500)

	sellerReward, err := pillarApi.GetUncollectedReward(seller)
	common.FailIfErr(t, err)
	buyerReward, err := pillarApi.GetUncollectedReward(buyer)
	common.FailIfErr(t, err)
	common.ExpectTrue(t, sellerReward.Znn.Sign() > 0)
	common.ExpectTrue(t, buyerReward.Znn.Sign() == 0)
}

func TestPillarOwnership_VoteByNameFollowsOwner(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateAccelerator(t, z)
	activatePillarOwnership(t, z)

	seller := g.Pillar1.Address
	buyer := g.Pillar4.Address
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.AcceleratorContract,
		Data:          definition.ABIAccelerator.PackMethodPanic(definition.CreateProjectMethodName, "Test Project", "TEST DESCRIPTION", "test.com", big.NewInt(100*g.Zexp), big.NewInt(1000*g.Zexp)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.ProjectCreationAmount,
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	projects, err := embedded.NewAcceleratorApi(z).GetAll(0, 10)
	common.FailIfErr(t, err)
	common.ExpectTrue(t, len(projects.List) == 1)
	projectId := projects.List[0].Id

	voteByName := func(sender types.Address) *nom.AccountBlock {
		return &nom.AccountBlock{
			Address:   sender,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName, projectId, g.Pillar1Name, definition.VoteYes),
		}
	}
	voteByProducer := func(sender types.Address) *nom.AccountBlock {
		return &nom.AccountBlock{
			Address:   sender,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName, projectId, definition.VoteYes),
		}
	}

	callAndExpect(t, z, voteByName(buyer), constants.ErrForbiddenParam)
	callAndExpect(t, z, voteByName(seller), nil)

	callAndExpect(t, z, lockOwnershipBlock(seller, g.Pillar1Name, buyer, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)
	callAndExpect(t, z, voteByName(seller), nil)
	callAndExpect(t, z, takeOwnershipBlock(buyer, g.Pillar1Name, ownershipPreimage), nil)

	callAndExpect(t, z, voteByName(seller), constants.ErrForbiddenParam)
	callAndExpect(t, z, voteByName(buyer), nil)
	callAndExpect(t, z, voteByProducer(seller), nil)
	callAndExpect(t, z, voteByProducer(buyer), constants.ErrForbiddenParam)

	callAndExpect(t, z, &nom.AccountBlock{
		Address:   buyer,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.UpdatePillarMethodName, g.Pillar1Name, buyer, buyer, uint8(0), uint8(100)),
	}, nil)
	callAndExpect(t, z, voteByProducer(seller), constants.ErrForbiddenParam)
	callAndExpect(t, z, voteByProducer(buyer), nil)
}

func TestPillarOwnership_Encoding(t *testing.T) {
	shortName := "TEST-nine"
	longName := strings.Repeat("a", constants.PillarNameLengthMax)
	hashLock := crypto.HashSHA256(ownershipPreimage)
	common.ExpectTrue(t, len(shortName) == 9)

	sizes := func(name string) (int, int, int, int) {
		lock := definition.ABIPillars.PackMethodPanic(definition.LockOwnershipMethodName, name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock)
		take := definition.ABIPillars.PackMethodPanic(definition.TakeOwnershipMethodName, name, ownershipPreimage)
		release := definition.ABIPillars.PackMethodPanic(definition.ReleaseOwnershipLockMethodName, name)

		storage := vm_context.NewGenesisMomentumVMContext().GetAccountStore(types.PillarContract).Storage()
		entry := &definition.PillarOwnershipLock{Name: name, Buyer: g.User5.Address, ExpirationHeight: 1000, HashType: definition.HashTypeSHA256, HashLock: hashLock}
		common.FailIfErr(t, entry.Save(storage))
		stored, err := storage.Get(definition.GetPillarOwnershipLockKey(name))
		common.FailIfErr(t, err)
		parsed, err := definition.GetPillarOwnershipLock(storage, name)
		common.FailIfErr(t, err)
		common.Expect(t, parsed, entry)
		return len(lock), len(take), len(release), len(stored)
	}

	lock, take, release, stored := sizes(shortName)
	common.Expect(t, []int{lock, take, release, stored}, []int{292, 196, 100, 288})
	lock, take, release, stored = sizes(longName)
	common.Expect(t, []int{lock, take, release, stored}, []int{324, 228, 132, 320})

	common.ExpectBytes(t, definition.GetPillarOwnershipLockKey(shortName)[:1], "0x06")
}

func TestPillarOwnership_ParseAsserts(t *testing.T) {
	storage := vm_context.NewGenesisMomentumVMContext().GetAccountStore(types.PillarContract).Storage()
	hashLock := crypto.HashSHA256(ownershipPreimage)

	_, err := definition.GetPillarOwnershipLock(storage, g.Pillar1Name)
	common.ExpectError(t, err, constants.ErrDataNonExistent)

	bad := []*definition.PillarOwnershipLock{
		{Name: g.Pillar1Name, Buyer: g.User5.Address, ExpirationHeight: 10, HashType: definition.HashTypeSHA256, HashLock: hashLock[:31]},
		{Name: g.Pillar1Name, Buyer: g.User5.Address, ExpirationHeight: 10, HashType: 7, HashLock: hashLock},
		{Name: g.Pillar1Name, Buyer: g.User5.Address, ExpirationHeight: 10, HashType: 7, HashLock: []byte{}},
		{Name: g.Pillar1Name, Buyer: types.ZeroAddress, ExpirationHeight: 10, HashType: definition.HashTypeSHA256, HashLock: hashLock},
	}
	for _, entry := range bad {
		common.FailIfErr(t, entry.Save(storage))
		_, err := definition.GetPillarOwnershipLock(storage, g.Pillar1Name)
		common.ExpectTrue(t, err != nil && err != constants.ErrDataNonExistent)
	}
}

func TestPillarOwnership_RepackIsIdentity(t *testing.T) {
	hashLock := crypto.HashSHA256(ownershipPreimage)
	cases := []struct {
		method interface {
			ValidateSendBlock(block *nom.AccountBlock) error
		}
		data []byte
	}{
		{&implementation.LockOwnershipMethod{MethodName: definition.LockOwnershipMethodName}, definition.ABIPillars.PackMethodPanic(definition.LockOwnershipMethodName, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, hashLock)},
		{&implementation.LockOwnershipMethod{MethodName: definition.LockOwnershipMethodName}, definition.ABIPillars.PackMethodPanic(definition.LockOwnershipMethodName, g.Pillar1Name, g.User5.Address, constants.PillarLockMaxDuration, definition.HashTypeSHA3, crypto.Hash(ownershipPreimage))},
		{&implementation.TakeOwnershipMethod{MethodName: definition.TakeOwnershipMethodName}, definition.ABIPillars.PackMethodPanic(definition.TakeOwnershipMethodName, g.Pillar1Name, ownershipPreimage)},
		{&implementation.TakeOwnershipMethod{MethodName: definition.TakeOwnershipMethodName}, definition.ABIPillars.PackMethodPanic(definition.TakeOwnershipMethodName, g.Pillar1Name, []byte{})},
		{&implementation.TakeOwnershipMethod{MethodName: definition.TakeOwnershipMethodName}, definition.ABIPillars.PackMethodPanic(definition.TakeOwnershipMethodName, g.Pillar1Name, []byte{1, 2, 3})},
		{&implementation.ReleaseOwnershipLockMethod{MethodName: definition.ReleaseOwnershipLockMethodName}, definition.ABIPillars.PackMethodPanic(definition.ReleaseOwnershipLockMethodName, g.Pillar1Name)},
	}
	for _, c := range cases {
		block := &nom.AccountBlock{Amount: big.NewInt(0), Data: append([]byte{}, c.data...)}
		common.FailIfErr(t, c.method.ValidateSendBlock(block))
		common.ExpectTrue(t, bytes.Equal(block.Data, c.data))

		padded := &nom.AccountBlock{Amount: big.NewInt(0), Data: append(append([]byte{}, c.data...), make([]byte, 4096)...)}
		common.FailIfErr(t, c.method.ValidateSendBlock(padded))
		common.ExpectTrue(t, bytes.Equal(padded.Data, c.data))
	}
}

func TestPillarOwnership_UnsignedPathRejectsPaddedCall(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activatePillarOwnership(t, z)

	callAndExpect(t, z, lockOwnershipBlock(g.Pillar1.Address, g.Pillar1Name, g.User5.Address, constants.PillarLockMinDuration, definition.HashTypeSHA256, crypto.HashSHA256(ownershipPreimage)), nil)

	generate := func() *nom.AccountBlock {
		supervisor := vm.NewSupervisor(z.Chain(), z.Consensus())
		template := takeOwnershipBlock(g.User5.Address, g.Pillar1Name, ownershipPreimage)
		template.BlockType = nom.BlockTypeUserSend
		transaction, err := supervisor.GenerateFromTemplate(template, g.User5.Signer)
		common.FailIfErr(t, err)
		return transaction.Block
	}

	canonical := generate()
	canonicalData := append([]byte{}, canonical.Data...)
	_, err := vm.NewSupervisor(z.Chain(), z.Consensus()).ApplyBlock(canonical)
	common.FailIfErr(t, err)
	common.ExpectTrue(t, bytes.Equal(canonical.Data, canonicalData))

	padded := generate()
	padded.Data = append(padded.Data, make([]byte, 4096)...)
	padded.Hash = padded.ComputeHash()
	signature, _, publicKey, err := g.User5.Signer(padded.Hash.Bytes())
	common.FailIfErr(t, err)
	padded.Signature = signature
	padded.PublicKey = publicKey
	_, err = vm.NewSupervisor(z.Chain(), z.Consensus()).ApplyBlock(padded)
	common.ExpectError(t, err, verifier.ErrABHashInvalid)
}
