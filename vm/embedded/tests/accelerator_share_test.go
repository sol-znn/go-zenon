package tests

import (
	"math/big"
	"testing"
	"time"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

const acceleratorShareSporkName = "spork-accelerator-share"

type shareSnapshot map[string]*big.Int

func createAcceleratorShareSpork(t *testing.T, z mock.MockZenon, activate bool) {
	saveSporkState(t)
	sporkAPI := embedded.NewSporkApi(z)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName,
			acceleratorShareSporkName,
			"activate spork for accelerator share",
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	if !activate {
		z.InsertNewMomentum()
		return
	}

	sporkList, err := sporkAPI.GetAll(0, 10)
	common.FailIfErr(t, err)
	var id types.Hash
	for _, spork := range sporkList.List {
		if spork.Name == acceleratorShareSporkName {
			id = spork.Id
		}
	}

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data:      definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName, id),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.AcceleratorShareSpork.SporkId = id
	types.ImplementedSporksMap[id] = true
}

func saveAcceleratorShareConstants(t *testing.T) {
	savedUpdate := constants.UpdateMinNumMomentums
	savedDuration := constants.AcceleratorDuration
	t.Cleanup(func() {
		constants.UpdateMinNumMomentums = savedUpdate
		constants.AcceleratorDuration = savedDuration
	})
}

func shareOf(amount int64) *big.Int {
	share, _ := constants.AcceleratorShareOf(big.NewInt(amount))
	return share
}

func restOf(amount int64) *big.Int {
	_, rest := constants.AcceleratorShareOf(big.NewInt(amount))
	return rest
}

func balanceOf(t *testing.T, z mock.MockZenon, address types.Address, zts types.ZenonTokenStandard) *big.Int {
	amount, err := z.Chain().GetFrontierAccountStore(address).GetBalance(zts)
	common.FailIfErr(t, err)
	if amount == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(amount)
}

func takeShareSnapshot(t *testing.T, z mock.MockZenon) shareSnapshot {
	pillarAPI := embedded.NewPillarApi(z, true)
	sentinelAPI := embedded.NewSentinelApi(z)
	stakeAPI := embedded.NewStakeApi(z)
	liquidityAPI := embedded.NewLiquidityApi(z)

	snapshot := shareSnapshot{
		"pillar-znn":    big.NewInt(0),
		"sentinel-znn":  big.NewInt(0),
		"sentinel-qsr":  big.NewInt(0),
		"stake-qsr":     big.NewInt(0),
		"liquidity-znn": big.NewInt(0),
		"liquidity-qsr": big.NewInt(0),
	}
	for _, keyPair := range g.AllKeyPairs {
		deposit, err := pillarAPI.GetUncollectedReward(keyPair.Address)
		common.FailIfErr(t, err)
		snapshot["pillar-znn"].Add(snapshot["pillar-znn"], deposit.Znn)

		deposit, err = sentinelAPI.GetUncollectedReward(keyPair.Address)
		common.FailIfErr(t, err)
		snapshot["sentinel-znn"].Add(snapshot["sentinel-znn"], deposit.Znn)
		snapshot["sentinel-qsr"].Add(snapshot["sentinel-qsr"], deposit.Qsr)

		deposit, err = stakeAPI.GetUncollectedReward(keyPair.Address)
		common.FailIfErr(t, err)
		snapshot["stake-qsr"].Add(snapshot["stake-qsr"], deposit.Qsr)

		deposit, err = liquidityAPI.GetUncollectedReward(keyPair.Address)
		common.FailIfErr(t, err)
		snapshot["liquidity-znn"].Add(snapshot["liquidity-znn"], deposit.Znn)
		snapshot["liquidity-qsr"].Add(snapshot["liquidity-qsr"], deposit.Qsr)
	}
	snapshot["liquidity-balance-znn"] = balanceOf(t, z, types.LiquidityContract, types.ZnnTokenStandard)
	snapshot["liquidity-balance-qsr"] = balanceOf(t, z, types.LiquidityContract, types.QsrTokenStandard)
	snapshot["accelerator-znn"] = balanceOf(t, z, types.AcceleratorContract, types.ZnnTokenStandard)
	snapshot["accelerator-qsr"] = balanceOf(t, z, types.AcceleratorContract, types.QsrTokenStandard)
	return snapshot
}

func (snapshot shareSnapshot) since(previous shareSnapshot) shareSnapshot {
	delta := make(shareSnapshot, len(snapshot))
	for key, value := range snapshot {
		delta[key] = new(big.Int).Sub(value, previous[key])
	}
	return delta
}

func expectShareAmount(t *testing.T, name string, got, expected *big.Int) {
	t.Helper()
	if got.Cmp(expected) != 0 {
		t.Fatalf("%v is %v, expected %v", name, got, expected)
	}
}

func acceleratorMints(t *testing.T, z mock.MockZenon) []*api.AccountBlock {
	ledgerAPI := api.NewLedgerApi(z)
	mints := make([]*api.AccountBlock, 0)
	for height := uint64(1); ; height += api.RpcMaxCountSize {
		list, err := ledgerAPI.GetAccountBlocksByHeight(types.TokenContract, height, api.RpcMaxCountSize)
		common.FailIfErr(t, err)
		for _, block := range list.List {
			if block.BlockType == nom.BlockTypeContractSend && block.ToAddress == types.AcceleratorContract {
				mints = append(mints, block)
			}
		}
		if len(list.List) < api.RpcMaxCountSize {
			return mints
		}
	}
}

func stakeForShare(t *testing.T, z mock.MockZenon, address types.Address) {
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       address,
		ToAddress:     types.StakeContract,
		Data:          definition.ABIStake.PackMethodPanic(definition.StakeMethodName, constants.StakeTimeMaxSec),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(10 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func runShareScenario(t *testing.T, activate bool) []shareSnapshot {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	createAcceleratorShareSpork(t, z, activate)
	registerSentinel(z, t, g.User1.Address)
	stakeForShare(t, z, g.User2.Address)

	snapshots := []shareSnapshot{takeShareSnapshot(t, z)}
	for _, height := range []uint64{380, 740, 1100} {
		z.InsertMomentumsTo(height)
		snapshots = append(snapshots, takeShareSnapshot(t, z))
	}
	return snapshots
}

func TestAcceleratorShare_ConservationAndEpochGate(t *testing.T) {
	plain := runShareScenario(t, false)
	shared := runShareScenario(t, true)

	epochZeroPlain := plain[1].since(plain[0])
	epochZeroShared := shared[1].since(shared[0])
	for key, value := range epochZeroPlain {
		expectShareAmount(t, "epoch 0 "+key, epochZeroShared[key], value)
	}
	expectShareAmount(t, "epoch 0 accelerator-znn", epochZeroShared["accelerator-znn"], common.Big0)
	expectShareAmount(t, "epoch 0 accelerator-qsr", epochZeroShared["accelerator-qsr"], common.Big0)

	dust := big.NewInt(int64(len(g.AllKeyPairs)))
	for epoch := 1; epoch <= 2; epoch++ {
		before := plain[epoch+1].since(plain[epoch])
		after := shared[epoch+1].since(shared[epoch])

		expectShareAmount(t, "plain accelerator-znn", before["accelerator-znn"], common.Big0)
		expectShareAmount(t, "plain accelerator-qsr", before["accelerator-qsr"], common.Big0)

		expectShareAmount(t, "plain sentinel-znn", before["sentinel-znn"], big.NewInt(187200000000))
		expectShareAmount(t, "plain sentinel-qsr", before["sentinel-qsr"], big.NewInt(500000000000))
		expectShareAmount(t, "plain stake-qsr", before["stake-qsr"], big.NewInt(1000000000000))
		expectShareAmount(t, "plain liquidity-balance-znn", before["liquidity-balance-znn"], big.NewInt(187200000000))
		expectShareAmount(t, "plain liquidity-balance-qsr", before["liquidity-balance-qsr"], big.NewInt(500000000000))

		expectShareAmount(t, "sentinel-znn", after["sentinel-znn"], restOf(187200000000))
		expectShareAmount(t, "sentinel-qsr", after["sentinel-qsr"], restOf(500000000000))
		expectShareAmount(t, "stake-qsr", after["stake-qsr"], restOf(1000000000000))
		expectShareAmount(t, "liquidity-balance-znn", after["liquidity-balance-znn"], restOf(187200000000))
		expectShareAmount(t, "liquidity-balance-qsr", after["liquidity-balance-qsr"], restOf(500000000000))

		expectedQsr := new(big.Int).Add(shareOf(500000000000), shareOf(1000000000000))
		expectedQsr.Add(expectedQsr, shareOf(500000000000))
		expectShareAmount(t, "accelerator-qsr", after["accelerator-qsr"], expectedQsr)

		pillarShare := new(big.Int).Sub(after["accelerator-znn"], shareOf(187200000000))
		pillarShare.Sub(pillarShare, shareOf(187200000000))
		if pillarShare.Sign() <= 0 {
			t.Fatalf("epoch %d: pillar share is %v", epoch, pillarShare)
		}

		conserved := new(big.Int).Add(after["pillar-znn"], pillarShare)
		difference := new(big.Int).Sub(before["pillar-znn"], conserved)
		if difference.CmpAbs(dust) > 0 {
			t.Fatalf("epoch %d: pillar deposits %v plus share %v differ from %v by %v", epoch, after["pillar-znn"], pillarShare, before["pillar-znn"], difference)
		}

		expectedShare := new(big.Int).Mul(before["pillar-znn"], big.NewInt(constants.AcceleratorSharePercentage))
		expectedShare.Quo(expectedShare, common.Big100)
		if new(big.Int).Sub(expectedShare, pillarShare).CmpAbs(dust) > 0 {
			t.Fatalf("epoch %d: pillar share is %v, expected about %v", epoch, pillarShare, expectedShare)
		}
	}
}

func TestAcceleratorShare_OneMintPerTokenAcrossEpochs(t *testing.T) {
	saveAcceleratorShareConstants(t)
	constants.UpdateMinNumMomentums = 1090

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	createAcceleratorShareSpork(t, z, true)
	registerSentinel(z, t, g.User1.Address)
	stakeForShare(t, z, g.User2.Address)

	z.InsertMomentumsTo(1085)
	if mints := acceleratorMints(t, z); len(mints) != 0 {
		t.Fatalf("%d mints to the accelerator before any update", len(mints))
	}
	z.InsertMomentumsTo(1100)

	snapshot := takeShareSnapshot(t, z)
	expectShareAmount(t, "sentinel-znn", snapshot["sentinel-znn"], new(big.Int).Add(big.NewInt(187200000000), new(big.Int).Mul(restOf(187200000000), common.Big2)))
	expectShareAmount(t, "stake-qsr", snapshot["stake-qsr"], new(big.Int).Add(big.NewInt(1000000000000), new(big.Int).Mul(restOf(1000000000000), common.Big2)))

	sentinelAndLiquidityZnn := new(big.Int).Mul(shareOf(187200000000), common.Big2)
	sentinelAndLiquidityQsr := new(big.Int).Mul(shareOf(500000000000), common.Big2)
	stakeQsr := new(big.Int).Mul(shareOf(1000000000000), common.Big2)

	mints := acceleratorMints(t, z)
	if len(mints) != 6 {
		t.Fatalf("%d mints to the accelerator, expected 6", len(mints))
	}
	znn := big.NewInt(0)
	qsr := big.NewInt(0)
	znnAmounts := make(map[string]int)
	qsrAmounts := make(map[string]int)
	for _, mint := range mints {
		if mint.TokenStandard == types.ZnnTokenStandard {
			znn.Add(znn, mint.Amount)
			znnAmounts[mint.Amount.String()]++
		} else {
			qsr.Add(qsr, mint.Amount)
			qsrAmounts[mint.Amount.String()]++
		}
	}
	if len(znnAmounts) != 2 || znnAmounts[sentinelAndLiquidityZnn.String()] != 2 {
		t.Fatalf("unexpected znn mints %v", znnAmounts)
	}
	if len(qsrAmounts) != 2 || qsrAmounts[sentinelAndLiquidityQsr.String()] != 2 || qsrAmounts[stakeQsr.String()] != 1 {
		t.Fatalf("unexpected qsr mints %v", qsrAmounts)
	}
	expectShareAmount(t, "accelerator-znn", snapshot["accelerator-znn"], znn)
	expectShareAmount(t, "accelerator-qsr", snapshot["accelerator-qsr"], qsr)
}

func TestAcceleratorShare_EmptyStreams(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	createAcceleratorShareSpork(t, z, true)
	z.InsertMomentumsTo(380)
	if mints := acceleratorMints(t, z); len(mints) != 0 {
		t.Fatalf("%d mints to the accelerator for epoch 0", len(mints))
	}
	before := takeShareSnapshot(t, z)
	z.InsertMomentumsTo(740)
	delta := takeShareSnapshot(t, z).since(before)

	expectShareAmount(t, "sentinel-znn", delta["sentinel-znn"], common.Big0)
	expectShareAmount(t, "sentinel-qsr", delta["sentinel-qsr"], common.Big0)
	expectShareAmount(t, "stake-qsr", delta["stake-qsr"], common.Big0)
	expectShareAmount(t, "liquidity-balance-znn", delta["liquidity-balance-znn"], restOf(187200000000))
	expectShareAmount(t, "liquidity-balance-qsr", delta["liquidity-balance-qsr"], restOf(500000000000))
	expectShareAmount(t, "accelerator-qsr", delta["accelerator-qsr"], shareOf(500000000000))

	mints := acceleratorMints(t, z)
	if len(mints) != 3 {
		t.Fatalf("%d mints to the accelerator, expected 3", len(mints))
	}
	liquidityZnn := 0
	for _, mint := range mints {
		if mint.TokenStandard == types.ZnnTokenStandard && mint.Amount.Cmp(shareOf(187200000000)) == 0 {
			liquidityZnn++
		}
	}
	if liquidityZnn != 1 {
		t.Fatalf("%d liquidity znn mints to the accelerator, expected 1", liquidityZnn)
	}
}

func TestAcceleratorShare_Sunset(t *testing.T) {
	saveAcceleratorShareConstants(t)
	constants.AcceleratorDuration = 9000

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	createAcceleratorShareSpork(t, z, true)
	registerSentinel(z, t, g.User1.Address)
	stakeForShare(t, z, g.User2.Address)

	z.InsertMomentumsTo(380)
	epochZero := takeShareSnapshot(t, z)
	z.InsertMomentumsTo(740)
	epochOne := takeShareSnapshot(t, z)
	z.InsertMomentumsTo(1100)
	epochTwo := takeShareSnapshot(t, z)

	running := epochOne.since(epochZero)
	expectShareAmount(t, "running sentinel-znn", running["sentinel-znn"], restOf(187200000000))
	expectShareAmount(t, "running stake-qsr", running["stake-qsr"], restOf(1000000000000))
	if running["accelerator-znn"].Sign() <= 0 || running["accelerator-qsr"].Sign() <= 0 {
		t.Fatalf("no share while the accelerator is running: %v znn, %v qsr", running["accelerator-znn"], running["accelerator-qsr"])
	}

	ended := epochTwo.since(epochOne)
	expectShareAmount(t, "ended sentinel-znn", ended["sentinel-znn"], big.NewInt(187200000000))
	expectShareAmount(t, "ended sentinel-qsr", ended["sentinel-qsr"], big.NewInt(500000000000))
	expectShareAmount(t, "ended stake-qsr", ended["stake-qsr"], big.NewInt(1000000000000))
	expectShareAmount(t, "ended liquidity-balance-znn", ended["liquidity-balance-znn"], big.NewInt(187200000000))
	expectShareAmount(t, "ended liquidity-balance-qsr", ended["liquidity-balance-qsr"], big.NewInt(500000000000))
	expectShareAmount(t, "ended accelerator-znn", ended["accelerator-znn"], common.Big0)
	expectShareAmount(t, "ended accelerator-qsr", ended["accelerator-qsr"], common.Big0)
}

func TestAcceleratorShare_LiquidityStakeAndAdditionalReward(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	activateLiquidityStep1(t, z)
	createAcceleratorShareSpork(t, z, true)

	znnPercentages := []uint32{uint32(7000), uint32(3000)}
	qsrPercentages := []uint32{uint32(3000), uint32(7000)}
	minAmounts := []*big.Int{big.NewInt(1000), big.NewInt(2000)}
	issueMultipleTokensSetup(t, z)
	setTokensTuple(t, z, g.User5.Address, tokensString, znnPercentages, qsrPercentages, minAmounts)

	z.InsertSendBlock(liquidityStake(g.User1.Address, tokens[0], big.NewInt(10*g.Zexp), 1), nil, mock.SkipVmChanges)
	insertMomentums(z, 2)
	z.InsertSendBlock(liquidityStake(g.User1.Address, tokens[1], big.NewInt(10*g.Zexp), 1), nil, mock.SkipVmChanges)
	insertMomentums(z, 2)

	z.InsertMomentumsTo(740)
	epochOne := takeShareSnapshot(t, z)
	expectShareAmount(t, "epoch 1 liquidity-znn", epochOne["liquidity-znn"], big.NewInt(187200000000))
	expectShareAmount(t, "epoch 1 liquidity-qsr", epochOne["liquidity-qsr"], big.NewInt(500000000000))
	expectShareAmount(t, "epoch 1 accelerator-qsr", epochOne["accelerator-qsr"], common.Big0)

	z.InsertMomentumsTo(1100)
	epochTwo := takeShareSnapshot(t, z)
	delta := epochTwo.since(epochOne)
	expectShareAmount(t, "epoch 2 liquidity-znn", delta["liquidity-znn"], restOf(187200000000))
	expectShareAmount(t, "epoch 2 liquidity-qsr", delta["liquidity-qsr"], restOf(500000000000))
	expectShareAmount(t, "epoch 2 liquidity-balance-znn", delta["liquidity-balance-znn"], common.Big0)
	expectShareAmount(t, "epoch 2 liquidity-balance-qsr", delta["liquidity-balance-qsr"], common.Big0)
	expectShareAmount(t, "epoch 2 accelerator-qsr", delta["accelerator-qsr"], shareOf(500000000000))

	additionalZnn := big.NewInt(10 * g.Zexp)
	additionalQsr := big.NewInt(20 * g.Zexp)
	setAdditionalReward(t, z, g.User5.Address, additionalZnn, additionalQsr)

	z.InsertMomentumsTo(1460)
	delta = takeShareSnapshot(t, z).since(epochTwo)
	expectShareAmount(t, "epoch 3 liquidity-znn", delta["liquidity-znn"], new(big.Int).Add(restOf(187200000000), additionalZnn))
	expectShareAmount(t, "epoch 3 liquidity-qsr", delta["liquidity-qsr"], new(big.Int).Add(restOf(500000000000), additionalQsr))
	expectShareAmount(t, "epoch 3 liquidity-balance-znn", delta["liquidity-balance-znn"], new(big.Int).Neg(additionalZnn))
	expectShareAmount(t, "epoch 3 liquidity-balance-qsr", delta["liquidity-balance-qsr"], new(big.Int).Neg(additionalQsr))
	expectShareAmount(t, "epoch 3 accelerator-qsr", delta["accelerator-qsr"], shareOf(500000000000))
}
