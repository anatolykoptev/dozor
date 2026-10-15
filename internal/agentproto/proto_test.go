package agentproto

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestValidRunAs(t *testing.T) {
	good := []string{"ab", "agent1", "a-b_c", "a" + strings.Repeat("b", 31)}
	bad := []string{"", "a", "A", "1ab", "a b", "a/b", "../x", "a.b", "root", "krolik", "ubuntu",
		"a" + strings.Repeat("b", 32), "ab\n", "é"}
	for _, s := range good {
		if !ValidRunAs(s) {
			t.Errorf("ValidRunAs(%q) = false", s)
		}
	}
	for _, s := range bad {
		if ValidRunAs(s) {
			t.Errorf("ValidRunAs(%q) = true", s)
		}
	}
}

func TestSocketPath(t *testing.T) {
	p, err := SocketPath(SocketDir, "ab")
	if err != nil || p != SocketDir+"/ab.sock" {
		t.Fatalf("%q %v", p, err)
	}
	for _, id := range []string{"", "../ab", "root", "a/b"} {
		if _, err := SocketPath(SocketDir, id); err == nil {
			t.Errorf("SocketPath accepted %q", id)
		}
	}
}

func TestVersionOK(t *testing.T) {
	for _, tc := range []struct {
		v    int
		want bool
	}{{ProtoVersion, true}, {ProtoVersion - 1, true}, {ProtoVersion - 2, false},
		{ProtoVersion + 1, false}, {0, false}, {-1, false}} {
		if VersionOK(tc.v) != tc.want {
			t.Errorf("VersionOK(%d) = %v, want %v", tc.v, !tc.want, tc.want)
		}
	}
	if MinProtoVersion < 1 {
		t.Errorf("MinProtoVersion %d admits the zero value of a missing field", MinProtoVersion)
	}
}

func TestDecodeStrict(t *testing.T) {
	var r Response
	if err := DecodeStrict([]byte(`{"v":2,"build":"x"}`), &r); err != nil || r.V != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	// Brackets inside strings do not count towards depth.
	if err := DecodeStrict([]byte(`{"v":2,"build":"[[[[[[[[[[[[[[[[[[[[[[[[\"[[["}`), &r); err != nil {
		t.Fatalf("brackets in a string rejected: %v", err)
	}
	deep := strings.Repeat("[", MaxJSONDepth+1) + strings.Repeat("]", MaxJSONDepth+1)
	if err := DecodeStrict([]byte(`{"v":2,"status":{"notes":`+deep+`}}`), &r); !errors.Is(err, ErrDepth) {
		t.Errorf("deep document: %v", err)
	}
	for name, doc := range map[string]string{
		"unknown":  `{"v":2,"zzz":1}`,
		"trailing": `{"v":2} {"v":2}`,
		"junk":     `{"v":2} x`,
		"empty":    ``,
		"array":    `[1]`,
	} {
		if err := DecodeStrict([]byte(doc), &r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPeerUID_ReportsTheCreator(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fds[0]), "a")
	defer f.Close()
	other := os.NewFile(uintptr(fds[1]), "b")
	defer other.Close()
	c, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, err := PeerUID(c.(*net.UnixConn))
	if err != nil || uid != uint32(os.Getuid()) { //nolint:gosec // uid is non-negative
		t.Fatalf("PeerUID = %d, %v; want %d", uid, err, os.Getuid())
	}
}
