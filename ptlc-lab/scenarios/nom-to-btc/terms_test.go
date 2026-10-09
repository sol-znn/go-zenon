package main

import (
	"strings"
	"testing"

	"zenon-ptlc/scenarios/nom-to-btc/adaptor"
)

// Bob weighs Alice's Bitcoin refund height against his NoM expiry before he
// gives her anything. If the refund could open while his entry is still open
// to her t, she could take the bitcoin back and the ZNN as well.
func TestCheckTerms(t *testing.T) {
	secret := make([]byte, 32)
	secret[31] = 7
	T, err := adaptor.PointOf(secret)
	if err != nil {
		t.Fatal(err)
	}
	const now, tip = int64(1_000_000), int64(500)
	l := func(blocks int64) uint32 { return uint32(tip + blocks) }
	for _, c := range []struct {
		name    string
		e       int64
		l       uint32
		refused string
	}{
		{"the lab's defaults", now + 240, l(30), ""},
		{"the refund scenario's", now + 150, l(10), ""},
		{"a refund two blocks out", now + 240, l(2), "refund opens 2 blocks"},
		{"an hour's expiry against five blocks", now + 3600, l(5), "refund opens 5 blocks"},
		{"an hour's expiry against fifteen blocks", now + 3600, l(15), ""},
		{"an expiry a minute out", now + 60, l(30), "needs at least 120"},
	} {
		err := checkTerms(terms{T: T, E: c.e, L: c.l}, now, tip)
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("%s: want a refusal naming %q, got %v", c.name, c.refused, err)
		}
	}
	if err := checkTerms(terms{T: T[:32], E: now + 240, L: l(30)}, now, tip); err == nil {
		t.Error("a T that is not 33 bytes was taken")
	}
}
