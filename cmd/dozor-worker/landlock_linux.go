//go:build linux

package main

import (
	"github.com/landlock-lsm/go-landlock/landlock"
	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// restrictBindTCP limits bind(2) on TCP sockets to ports lo..hi, for every
// thread of this process and for everything it starts afterwards. Only the
// bind right is handled: connect(2), including the implicit bind it performs
// to pick a source port, is not restricted here (nftables does that).
//
// It also installs the Multipath TCP filter (see denyMPTCP): Landlock does not
// see MPTCP sockets, so without it the restriction is a one-line bypass.
//
// The configuration is strict, not best-effort: a kernel without Landlock
// ABI 4 returns an error and the caller fails closed.
func restrictBindTCP(lo, hi uint16) error {
	rules := make([]landlock.Rule, 0, int(hi)-int(lo)+1)
	for p := int(lo); p <= int(hi); p++ {
		rules = append(rules, landlock.BindTCP(uint16(p))) //nolint:gosec // bounded by hi
	}
	cfg := landlock.MustConfig(landlock.AccessNetSet(llsyscall.AccessNetBindTCP))
	if err := cfg.RestrictNet(rules...); err != nil {
		return err
	}
	return denyMPTCP()
}
