//go:build linux

package main

import (
	"github.com/landlock-lsm/go-landlock/landlock"
	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// restrictBindTCP limits explicit bind(2) on TCP sockets to ports lo..hi and
// to port 0, for every thread of this process and for everything it starts
// afterwards. Port 0 asks the kernel for an ephemeral port: the agent cannot
// choose which, the kernel picks from ip_local_port_range, which does not
// include the reserved service ports, and tools that need any free port keep
// working. The implicit bind of listen(2) on an unbound socket (autobind) also
// takes an ephemeral port, as does the source-port bind of connect(2), which
// is not restricted at all (nftables owns connect). Inbound exposure of what
// the agent binds is closed at the network layer, not here.
//
// What Landlock does NOT restrict: UDP, and any protocol other than TCP. It
// also does not see Multipath TCP sockets, so without the filter in denyMPTCP
// the restriction would be a one-line bypass.
//
// The configuration is strict, not best-effort: a kernel without Landlock
// ABI 4 returns an error and the caller fails closed.
func restrictBindTCP(lo, hi uint16) error {
	rules := make([]landlock.Rule, 0, int(hi)-int(lo)+2)
	rules = append(rules, landlock.BindTCP(0))
	for p := int(lo); p <= int(hi); p++ {
		rules = append(rules, landlock.BindTCP(uint16(p))) //nolint:gosec // bounded by hi
	}
	cfg := landlock.MustConfig(landlock.AccessNetSet(llsyscall.AccessNetBindTCP))
	if err := cfg.RestrictNet(rules...); err != nil {
		return err
	}
	return denySockets()
}
