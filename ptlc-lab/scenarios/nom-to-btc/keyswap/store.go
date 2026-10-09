package keyswap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zenon-network/go-zenon/common/types"
)

// State is everything one party has to keep for one swap. Three things in it
// cost funds if they are lost at the wrong moment, and each is written here
// before the step that depends on it:
//
//   - Key, the swap key the party's own entry is locked to. Without it the
//     party cannot pre-sign; it is of no further use once the entry is settled.
//   - Secret. Alice's t from the start: without it she cannot claim, and waits
//     out her own entry. Bob's once he has read it off the chain.
//   - Theirs.PreSig, the other side's pre-signature. Bob must hold Alice's
//     before he locks: if he lost it after she claimed, he would know t and
//     still be unable to complete the signature that pays him.
//
// The file holds private keys in the clear and is written with the owner's
// permissions only. It is a few hundred bytes and can be deleted once Stage is
// StageDone.
type State struct {
	Role    string        `json:"role"`
	Stage   string        `json:"stage"`
	Outcome string        `json:"outcome,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Account types.Address `json:"account"`

	Key    []byte  `json:"key,omitempty"`
	Secret []byte  `json:"secret,omitempty"`
	Offer  *Offer  `json:"offer,omitempty"`
	Accept *Accept `json:"accept,omitempty"`
	Terms  *Terms  `json:"terms,omitempty"`

	// Since is the height of the party's own account chain before its Create,
	// and From the contract's: where to look for the Create after a crash, and
	// where to start reading unlocks.
	Since uint64 `json:"since,omitempty"`
	From  uint64 `json:"from,omitempty"`

	Mine   Locked     `json:"mine"`   // own entry, and the pre-signature sent for it
	Theirs Locked     `json:"theirs"` // the other entry, and the pre-signature received
	Call   types.Hash `json:"call"`   // the unlock or reclaim last sent
}

// Store keeps one State in one file of one directory. A directory belongs to
// one party and one swap.
type Store struct {
	dir string
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "swap.json") }

// Load returns the saved state, or nil if the swap has not begun.
func (s *Store) Load() (*State, error) {
	body, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := new(State)
	if err := json.Unmarshal(body, st); err != nil {
		return nil, fmt.Errorf("%s does not parse, and the swap cannot be resumed from it: %w", s.path(), err)
	}
	return st, nil
}

// Save replaces the state on disk. The new file is written beside the old one
// and flushed before it takes its place, so a crash leaves the old state or
// the new one, never part of either.
func (s *Store) Save(st *State) error {
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return err
	}
	// The rename is durable once the directory is flushed. Windows cannot
	// flush a directory this way, and the error is not one to act on.
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
