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
	"net"
	"os"
	"os/exec"
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
	procStatus  func() (capEff string, noNewPrivs int, err error)
	mountCheck  func(dir string) error
	home, user  string
	findCommand func(string) (string, error)
}

func realDeps() deps {
	return deps{
		geteuid:     os.Geteuid,
		procStatus:  readProcStatus,
		mountCheck:  checkMount,
		home:        os.Getenv("HOME"),
		user:        os.Getenv("USER"),
		findCommand: exec.LookPath,
	}
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("dozor-worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	protocol := fs.Bool("protocol", false, "print the protocol version and exit")
	callerUID := fs.Int64("caller-uid", -1, "uid of the only process allowed to connect (the dozor process)")
	mountDir := fs.String("require-mount", "", "directory that must be a separate ext4 mount, not the root filesystem")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *protocol {
		_, _ = fmt.Fprintln(stdout, agentproto.ProtoVersion)
		return 0
	}
	if *callerUID < 0 {
		_, _ = fmt.Fprintln(stderr, "dozor-worker: --caller-uid is required")
		return 2
	}
	conn, err := net.FileConn(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: stdin is not a socket: %v\n", err)
		return 1
	}
	defer conn.Close()

	if err := selfCheck(conn, uint32(*callerUID), *mountDir, d); err != nil { //nolint:gosec // range-checked above
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
	cfg := &agentworker.Config{Home: d.home, User: d.user, ExecPrefix: idlePrefix(d, stderr)}
	if err := cfg.Serve(context.Background(), conn, inherited...); err != nil {
		_, _ = fmt.Fprintf(stderr, "dozor-worker: %v\n", err)
		return 1
	}
	return 0
}

// idlePrefix runs agent commands under SCHED_IDLE while the worker itself
// keeps normal priority. Without chrt the commands run at normal priority.
func idlePrefix(d deps, stderr io.Writer) []string {
	path, err := d.findCommand("chrt")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dozor-worker: chrt not found; agent commands run at normal CPU priority")
		return nil
	}
	return []string{path, "--idle", "0"}
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
	if err := checkPrivileges(d.procStatus); err != nil {
		return err
	}
	if mountDir != "" {
		if err := d.mountCheck(mountDir); err != nil {
			return fmt.Errorf("mount check: %w", err)
		}
	}
	return nil
}

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
		return fmt.Errorf("peer uid %d is not the expected caller", uid)
	}
	return nil
}

// checkEUID: refuse root, refuse the caller's own uid, and refuse anything
// outside the agent uid range. This is what makes a misconfigured unit
// (User=root, User=<operator>) harmless instead of a shell.
func checkEUID(euid int, caller uint32) error {
	switch {
	case euid == 0:
		return errors.New("running as root")
	case int64(euid) == int64(caller):
		return errors.New("running as the caller's uid")
	case euid < agentproto.UIDMin || euid > agentproto.UIDMax:
		return fmt.Errorf("euid %d outside the agent range %d-%d", euid, agentproto.UIDMin, agentproto.UIDMax)
	}
	return nil
}

func checkPrivileges(read func() (string, int, error)) error {
	capEff, nnp, err := read()
	if err != nil {
		return fmt.Errorf("read process status: %w", err)
	}
	if strings.Trim(capEff, "0") != "" {
		return errors.New("effective capabilities are not empty")
	}
	if nnp != 1 {
		return errors.New("NoNewPrivs is not set")
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
