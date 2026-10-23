package constants

import (
	"math/big"
	"testing"
)

func TestAcceleratorShareOf(t *testing.T) {
	cases := []struct {
		amount string
		share  string
		rest   string
	}{
		{"0", "0", "0"},
		{"1", "0", "1"},
		{"19", "0", "19"},
		{"20", "1", "19"},
		{"39", "1", "38"},
		{"100", "5", "95"},
		{"187200000000", "9360000000", "177840000000"},
		{"500000000000", "25000000000", "475000000000"},
		{"11538239999", "576911999", "10961328000"},
		{"4611686018427387903", "230584300921369395", "4381101717506018508"},
	}
	for _, c := range cases {
		amount, _ := new(big.Int).SetString(c.amount, 10)
		before := new(big.Int).Set(amount)
		share, rest := AcceleratorShareOf(amount)
		if share.String() != c.share || rest.String() != c.rest {
			t.Fatalf("AcceleratorShareOf(%v) = %v, %v; expected %v, %v", c.amount, share, rest, c.share, c.rest)
		}
		if new(big.Int).Add(share, rest).Cmp(before) != 0 {
			t.Fatalf("AcceleratorShareOf(%v) does not sum to the amount", c.amount)
		}
		if amount.Cmp(before) != 0 {
			t.Fatalf("AcceleratorShareOf(%v) changed its argument", c.amount)
		}
	}
}
