package embedded

import (
	"reflect"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	cabi "github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/embedded/implementation"
)

func TestAcceleratorShare_VariantTable(t *testing.T) {
	embeddedVariantsOnce.Do(buildEmbeddedVariants)

	shared := map[types.Address]reflect.Type{
		types.PillarContract:   reflect.TypeOf(&implementation.UpdateSharedEmbeddedPillarMethod{}),
		types.SentinelContract: reflect.TypeOf(&implementation.UpdateSharedEmbeddedSentinelMethod{}),
		types.StakeContract:    reflect.TypeOf(&implementation.UpdateSharedEmbeddedStakeMethod{}),
	}

	for key := 0; key < embeddedVariantCombinations; key++ {
		if key&acceleratorShareSporkBit != 0 {
			continue
		}
		if key&bridgeAndLiquiditySporkBit != 0 {
			shared[types.LiquidityContract] = reflect.TypeOf(&implementation.UpdateSharedRewardEmbeddedLiquidityMethod{})
		} else {
			shared[types.LiquidityContract] = reflect.TypeOf(&implementation.UpdateSharedEmbeddedLiquidityMethod{})
		}
		before := embeddedVariants[key]
		after := embeddedVariants[key|acceleratorShareSporkBit]

		if len(before) != len(after) {
			t.Fatalf("key %d: %d contracts before, %d after", key, len(before), len(after))
		}
		for address, contract := range before {
			sharedContract, ok := after[address]
			if !ok {
				t.Fatalf("key %d: contract %v missing after", key, address)
			}
			if len(contract.m) != len(sharedContract.m) {
				t.Fatalf("key %d: contract %v has %d methods before, %d after", key, address, len(contract.m), len(sharedContract.m))
			}
			for name, method := range contract.m {
				sharedMethod, ok := sharedContract.m[name]
				if !ok {
					t.Fatalf("key %d: method %v.%v missing after", key, address, name)
				}

				plasma, err := method.GetPlasma(&constants.AlphanetPlasmaTable)
				if err != nil {
					t.Fatal(err)
				}
				sharedPlasma, err := sharedMethod.GetPlasma(&constants.AlphanetPlasmaTable)
				if err != nil {
					t.Fatal(err)
				}
				if plasma != sharedPlasma {
					t.Fatalf("key %d: plasma of %v.%v changed from %d to %d", key, address, name, plasma, sharedPlasma)
				}

				expected := reflect.TypeOf(method)
				if sharedType, ok := shared[address]; ok && name == cabi.UpdateMethodName {
					expected = sharedType
				}
				if reflect.TypeOf(sharedMethod) != expected {
					t.Fatalf("key %d: %v.%v is %v, expected %v", key, address, name, reflect.TypeOf(sharedMethod), expected)
				}
			}
		}
	}
}
