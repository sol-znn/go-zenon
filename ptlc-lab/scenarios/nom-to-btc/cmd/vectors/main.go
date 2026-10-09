// vectors prints known-answer values of the contract's unlock message, for
// the Dart SDK's test to check its own construction against go-zenon's.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

func main() {
	type row struct {
		ChainIdentifier uint64 `json:"chainIdentifier"`
		PointType       uint8  `json:"pointType"`
		Id              string `json:"id"`
		Destination     string `json:"destination"`
		Message         string `json:"message"`
	}
	ids := []string{
		"1111111111111111111111111111111111111111111111111111111111111111",
		"47f57134b5d8308335b5c146e586cfdcb62dc476c0279dd6c04963ecca586a0e",
	}
	dests := []string{"z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", "z1qp3yph55qgresyytz83anynr2f4z39x2z3ej3e", types.ZeroAddress.String()}
	var rows []row
	for _, chain := range []uint64{1, 69} {
		for _, pt := range []uint8{0, 1} {
			for _, idHex := range ids {
				for _, d := range dests {
					id := types.HexToHashPanic(idHex)
					dest := types.ParseAddressPanic(d)
					m := definition.GetPtlcUnlockMessage(chain, pt, id, dest)
					rows = append(rows, row{chain, pt, idHex, d, hex.EncodeToString(m)})
				}
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(rows); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
