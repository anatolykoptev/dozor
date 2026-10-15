// Command dozor-worker serves exactly one agent request on its stdin socket
// and exits. It is started by a socket-activated, per-identity systemd unit
// (Accept=yes) running as the agent's own uid, so everything it does is
// bounded by that uid. It never imports anything that runs with dozor's own
// privileges (fitness function F1).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/anatolykoptev/dozor/internal/agentworker"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, realDeps()))
}

// deps are the process facts the self-check reads; tests substitute them.
type deps struct {
	geteuid     func() int
	getegid     func() int
	getgroups   func() ([]int, error)
	procStatus  func() (capEff string, noNewPrivs int, err error)
	mountCheck  func(dir string) error
	home, user  string
	findCommand func(string) (string, error)
	// restrictBind limits TCP bind(2) to ports lo..hi for this process and all
	// its descendants (Landlock). Tests substitute it: the restriction is
	// irreversible.
	restrictBind func(lo, hi uint16) error
}

func realDeps() deps {
	return deps{
		geteuid:     os.Geteuid,
		getegid:     os.Getegid,
		getgroups:   os.Getgroups,
		procStatus:  readProcStatus,
		mountCheck:  checkMount,
		home:        os.Getenv("HOME"),
		user:        os.Getenv("USER"),
		findCommand: exec.LookPath,

		restrictBind: restrictBindTCP,
	}
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("dozor-worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	protocol := fs.Bool("protocol", false, "print the protocol version and exit")
	callerUID := fs.Int64("caller-uid", -1, "uid of the only process allowed to connect (the dozor process)")
	mountDir := fs.String("require-mount", "", "directory that must be a separate ext4 mount; HOME must be under it")
	noMount := fs.Bool("no-mount-check", false, "development/CI only: skip the mount requirement")
	noIdle := fs.Bool("no-idle", false, "allow agent commands to run at normal CPU priority when chrt is unavailable")
	bindPorts := fs.String("bind-ports", defaultBindPorts, "TCP ports agent processes may bind, LO-HI or PORT (Landlock)")
	noLandlock := fs.Bool("no-landlock", false, "development/CI only: do not restrict TCP bind; without Landlock ABI 4 agent_exec is otherwise disabled")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *protocol {
		_, _ = fmt.Fprintln(stdout, agentproto.ProtoVersion)
		return 0
	}
	bindLo, bindHi, err := parsePortRange(*bindPorts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: --bind-ports: %v\n", err)
		return 2
	}
	if *callerUID < 0 || *callerUID > math.MaxUint32 {
		_, _ = fmt.Fprintln(stderr, "dozor-worker: --caller-uid is required and must be a valid uid")
		return 2
	}
	if (*mountDir == "") == !*noMount { // exactly one of the two flags
		_, _ = fmt.Fprintln(stderr, "dozor-worker: exactly one of --require-mount DIR and --no-mount-check is required")
		return 2
	}
	agentworker.SealInherited()
	conn, err := net.FileConn(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: stdin is not a socket: %v\n", err)
		return 1
	}
	defer conn.Close()

	caller := uint32(*callerUID) //nolint:gosec // bounded to MaxUint32 above
	if err := selfCheck(conn, caller, *mountDir, d); err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: refused: %v\n", err)
		_ = agentworker.Refuse(conn, err.Error())
		return 1
	}

	var inherited []*os.File
	for _, w := range []any{stdin, stdout} {
		if f, ok := w.(*os.File); ok {
			inherited = append(inherited, f)
		}
	}
	cfg := &agentworker.Config{Home: d.home, User: d.user}
	cfg.ExecPrefix, cfg.ExecUnavailable, cfg.Notes = idleSetup(d, *noIdle)
	// Before the first request is read, so before any child can exist: the
	// restriction is inherited by everything the agent starts.
	bindNote, bindUnavailable := bindSetup(d, *noLandlock, bindLo, bindHi)
	cfg.Notes = append(cfg.Notes, bindNote)
	if cfg.ExecUnavailable == "" {
		cfg.ExecUnavailable = bindUnavailable
	}
	if err := cfg.Serve(context.Background(), conn, inherited...); err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: %v\n", err)
		return 1
	}
	return 0
}

// idleSetup decides how agent commands are scheduled. They run under
// SCHED_IDLE (chrt --idle 0) while the worker keeps normal priority. If chrt
// is missing, exec fails with an explicit error unless --no-idle was given;
// the state is always reported in agent_status.
func idleSetup(d deps, noIdle bool) (prefix []string, unavailable string, notes []string) {
	path, err := d.findCommand("chrt")
	switch {
	case err == nil:
		return []string{path, "--idle", "0"}, "", []string{"agent_exec runs at idle CPU priority (SCHED_IDLE)"}
	case noIdle:
		return nil, "", []string{"agent_exec runs at NORMAL CPU priority (chrt unavailable, --no-idle given)"}
	default:
		return nil, "idle CPU scheduling unavailable (chrt not found); agent_exec is disabled until chrt is installed or the worker is started with --no-idle",
			[]string{"agent_exec is DISABLED: chrt not found"}
	}
}

// bindSetup applies the TCP bind restriction. Without Landlock ABI 4 the
// worker cannot keep an agent off the host's service ports, so exec is
// disabled (fail closed) unless --no-landlock was given. File operations do
// not bind sockets and keep working. The state is always reported in
// agent_status.
func bindSetup(d deps, noLandlock bool, lo, hi uint16) (note, unavailable string) {
	if noLandlock {
		return "TCP bind is UNRESTRICTED (--no-landlock given)", ""
	}
	if err := d.restrictBind(lo, hi); err != nil {
		return "agent_exec is DISABLED: TCP bind restriction (Landlock ABI 4) unavailable",
			"TCP bind restriction unavailable (Landlock ABI 4 required); agent_exec is disabled until the kernel supports it or the worker is started with --no-landlock"
	}
	return fmt.Sprintf("TCP bind limited to ports %d-%d (Landlock)", lo, hi), ""
}

// defaultBindPorts is the range reserved for agent-owned servers.
const defaultBindPorts = "30000-30999"

// parsePortRange parses "LO-HI" or "PORT". Port 0 is not a valid bound.
func parsePortRange(s string) (lo, hi uint16, err error) {
	a, b, isRange := strings.Cut(s, "-")
	l, err := strconv.ParseUint(a, 10, 16)
	if err != nil || l == 0 {
		return 0, 0, fmt.Errorf("%q is not a port range", s)
	}
	h := l
	if isRange {
		if h, err = strconv.ParseUint(b, 10, 16); err != nil || h < l {
			return 0, 0, fmt.Errorf("%q is not a port range", s)
		}
	}
	return uint16(l), uint16(h), nil
}

// selfCheck is the authoritative gate of §"where run_as is checked": it runs
// once per connection, before any request byte is read.
func selfCheck(conn net.Conn, caller uint32, mountDir string, d deps) error {
	if err := checkPeer(conn, caller); err != nil {
		return err
	}
	if err := checkEUID(d.geteuid(), caller); err != nil {
		return err
	}
	groups, err := d.getgroups()
	if err != nil {
		return fmt.Errorf("read groups: %w", err)
	}
	if err := checkGIDs(d.getegid(), groups); err != nil {
		return err
	}
	if err := checkPrivileges(d.procStatus); err != nil {
		return err
	}
	if mountDir != "" {
		if err := d.mountCheck(mountDir); err != nil {
			return fmt.Errorf("mount check: %w", err)
		}
		if err := checkHomeUnder(d.home, mountDir); err != nil {
			return err
		}
	}
	return nil
}

// Refusal reasons. The worker's reply text is built from these; code that
// needs to branch uses errors.Is on them, never the message.
var (
	errPeerUID  = errors.New("peer uid is not the expected caller")
	errRoot     = errors.New("running as root")
	errSameUID  = errors.New("running as the caller's uid")
	errUIDRange = errors.New("euid outside the agent range")
	errGIDRange = errors.New("gid outside the agent range")
	errCaps     = errors.New("effective capabilities are not empty")
	errNNP      = errors.New("NoNewPrivs is not set")
	errHome     = errors.New("HOME is not under the required mount")
)

// checkPeer: the process on the other end of the socket must be the expected
// caller. Peer credentials are kernel-provided and cannot be forged.
func checkPeer(conn net.Conn, caller uint32) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("connection is not a unix socket")
	}
	uid, err := agentproto.PeerUID(uc)
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if uid != caller {
		return fmt.Errorf("%w (got %d)", errPeerUID, uid)
	}
	return nil
}

// checkEUID: refuse root, refuse the caller's own uid, and refuse anything
// outside the agent uid range. This is what makes a misconfigured unit
// (User=root, User=<operator>) harmless instead of a shell.
func checkEUID(euid int, caller uint32) error {
	switch {
	case euid == 0:
		return errRoot
	case int64(euid) == int64(caller):
		return errSameUID
	case euid < agentproto.UIDMin || euid > agentproto.UIDMax:
		return fmt.Errorf("%w: %d", errUIDRange, euid)
	}
	return nil
}

// checkGIDs: the egid and every supplementary group must be agent-range ids,
// so the process holds no group access to anything outside the agent.
func checkGIDs(egid int, groups []int) error {
	for _, g := range append([]int{egid}, groups...) {
		if g < agentproto.UIDMin || g > agentproto.UIDMax {
			return fmt.Errorf("%w: %d", errGIDRange, g)
		}
	}
	return nil
}

// checkHomeUnder requires home to be the checked mount or inside it, so the
// mount check is about the directory the agent actually writes to.
func checkHomeUnder(home, dir string) error {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	rel, err := filepath.Rel(resolve(dir), resolve(home))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errHome
	}
	return nil
}

func checkPrivileges(read func() (string, int, error)) error {
	capEff, nnp, err := read()
	if err != nil {
		return fmt.Errorf("read process status: %w", err)
	}
	if strings.Trim(capEff, "0") != "" {
		return errCaps
	}
	if nnp != 1 {
		return errNNP
	}
	return nil
}

// parseProcStatus extracts CapEff and NoNewPrivs from /proc/<pid>/status.
func parseProcStatus(status string) (capEff string, noNewPrivs int, err error) {
	noNewPrivs = -1
	for _, line := range strings.Split(status, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "CapEff":
			capEff = strings.TrimSpace(v)
		case "NoNewPrivs":
			if _, e := fmt.Sscanf(strings.TrimSpace(v), "%d", &noNewPrivs); e != nil {
				noNewPrivs = -1
			}
		}
	}
	if capEff == "" || noNewPrivs < 0 {
		return "", 0, errors.New("CapEff/NoNewPrivs missing")
	}
	return capEff, noNewPrivs, nil
}

const ext4Magic = 0xEF53

func mountOK(rootDev, dirDev uint64, fsType int64) error {
	if rootDev == dirDev {
		return errors.New("directory is on the root filesystem")
	}
	if fsType != ext4Magic {
		return fmt.Errorf("filesystem type %#x is not ext4", fsType)
	}
	return nil
}
