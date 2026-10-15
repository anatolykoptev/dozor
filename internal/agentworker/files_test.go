package agentworker

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

func ptr[T any](v T) *T { return &v }

func mustWrite(t *testing.T, c *Config, req agentproto.WriteFileRequest) *agentproto.WriteFileResult {
	t.Helper()
	res, perr := c.WriteFile(&req)
	if perr != nil {
		t.Fatalf("write: %+v", perr)
	}
	return res
}

func TestWrite_Atomic(t *testing.T) {
	c := newCfg(t)
	dst := filepath.Join(c.Home, "f.txt")
	mustWrite(t, c, agentproto.WriteFileRequest{Path: dst, Text: ptr("old")})

	// A failure after the new bytes are written, before the commit, must
	// leave the previous content intact and no temp file behind.
	c.beforeCommit = func() error { return errors.New("injected") }
	_, perr := c.WriteFile(&agentproto.WriteFileRequest{Path: dst, Text: ptr("brand new content"), Overwrite: true})
	if perr == nil {
		t.Fatal("expected the injected failure")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old" {
		t.Fatalf("destination modified before commit: %q", got)
	}
	ents, _ := os.ReadDir(c.Home)
	if len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}

	// A successful overwrite replaces the file (new inode), never edits it.
	c.beforeCommit = nil
	before, _ := os.Stat(dst)
	mustWrite(t, c, agentproto.WriteFileRequest{Path: dst, Text: ptr("new"), Overwrite: true})
	after, _ := os.Stat(dst)
	if os.SameFile(before, after) {
		t.Fatal("overwrite edited the file in place")
	}
	if got, _ := os.ReadFile(dst); string(got) != "new" {
		t.Fatalf("content = %q", got)
	}
}

func TestWrite_NoOverwriteByDefault(t *testing.T) {
	c := newCfg(t)
	dst := filepath.Join(c.Home, "f")
	mustWrite(t, c, agentproto.WriteFileRequest{Path: dst, Text: ptr("one")})
	if _, perr := c.WriteFile(&agentproto.WriteFileRequest{Path: dst, Text: ptr("two")}); perr == nil {
		t.Fatal("second write without overwrite succeeded")
	}
	if got, _ := os.ReadFile(dst); string(got) != "one" {
		t.Fatalf("content = %q", got)
	}
	if ents, _ := os.ReadDir(c.Home); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestWrite_ModeParentsBinary(t *testing.T) {
	c := newCfg(t)
	bin := []byte{0, 1, 2, 0xff, 0xfe}
	res := mustWrite(t, c, agentproto.WriteFileRequest{
		Path: "a/b/blob", Base64: ptr(base64.StdEncoding.EncodeToString(bin)), CreateParents: true,
	})
	st, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || res.Mode != "0600" {
		t.Fatalf("default mode = %v / %s", st.Mode().Perm(), res.Mode)
	}
	if got, _ := os.ReadFile(res.Path); !bytes.Equal(got, bin) {
		t.Fatalf("binary round trip: %v", got)
	}
	res = mustWrite(t, c, agentproto.WriteFileRequest{Path: "x", Text: ptr("x"), Mode: "0644"})
	if st, _ = os.Stat(res.Path); st.Mode().Perm() != 0o644 {
		t.Fatalf("explicit mode ignored despite umask: %v", st.Mode().Perm())
	}
	if _, perr := c.WriteFile(&agentproto.WriteFileRequest{Path: "no/parent/f", Text: ptr("x")}); perr == nil {
		t.Fatal("missing parent without create_parents succeeded")
	}
}

func TestWrite_BadRequests(t *testing.T) {
	c := newCfg(t)
	big := strings.Repeat("a", agentproto.MaxWriteBytes+1)
	cases := map[string]agentproto.WriteFileRequest{
		"neither":     {Path: "f"},
		"both":        {Path: "f", Text: ptr("a"), Base64: ptr("YQ==")},
		"bad base64":  {Path: "f", Base64: ptr("!!!")},
		"too large":   {Path: "f", Text: &big},
		"suid mode":   {Path: "f", Text: ptr("a"), Mode: "4755"},
		"junk mode":   {Path: "f", Text: ptr("a"), Mode: "rwx"},
		"empty path":  {Text: ptr("a")},
		"nul in path": {Path: "a\x00b", Text: ptr("a")},
	}
	for name, req := range cases {
		if _, perr := c.WriteFile(&req); perr == nil || perr.Code != agentproto.CodeBadRequest {
			t.Errorf("%s: got %+v, want bad_request", name, perr)
		}
	}
	edge := strings.Repeat("a", agentproto.MaxWriteBytes)
	mustWrite(t, c, agentproto.WriteFileRequest{Path: "edge", Text: &edge})
}

func readReq(t *testing.T, c *Config, req agentproto.ReadFileRequest) *agentproto.ReadFileResult {
	t.Helper()
	res, perr := c.ReadFile(&req)
	if perr != nil {
		t.Fatalf("read: %+v", perr)
	}
	return res
}

func TestRead_TextBinaryAndPaging(t *testing.T) {
	c := newCfg(t)
	mustWrite(t, c, agentproto.WriteFileRequest{Path: "t", Text: ptr("héllo wörld")})
	res := readReq(t, c, agentproto.ReadFileRequest{Path: "t"})
	if res.Encoding != "utf8" || res.Content != "héllo wörld" || !res.EOF {
		t.Fatalf("got %+v", res)
	}

	// A page boundary inside "é" must not flip the page to base64.
	res = readReq(t, c, agentproto.ReadFileRequest{Path: "t", Length: 2})
	if res.Encoding != "utf8" || res.Content != "h" || res.EOF || res.Length != 1 {
		t.Fatalf("mid-rune page: %+v", res)
	}
	next := readReq(t, c, agentproto.ReadFileRequest{Path: "t", Offset: res.Offset + res.Length})
	if res.Content+next.Content != "héllo wörld" {
		t.Fatalf("paging lost bytes: %q + %q", res.Content, next.Content)
	}

	bin := []byte{0xff, 0xfe, 0x00}
	mustWrite(t, c, agentproto.WriteFileRequest{Path: "b", Base64: ptr(base64.StdEncoding.EncodeToString(bin))})
	res = readReq(t, c, agentproto.ReadFileRequest{Path: "b"})
	dec, _ := base64.StdEncoding.DecodeString(res.Content)
	if res.Encoding != "base64" || !bytes.Equal(dec, bin) {
		t.Fatalf("binary: %+v", res)
	}
}

func TestRead_ShrinksToFitReplyCap(t *testing.T) {
	c := newCfg(t)
	nul := base64.StdEncoding.EncodeToString(make([]byte, 2<<20))
	mustWrite(t, c, agentproto.WriteFileRequest{Path: "nul", Base64: &nul})
	// 2 MiB of NUL is valid UTF-8 whose JSON escape is 6x the size.
	res := readReq(t, c, agentproto.ReadFileRequest{Path: "nul", Length: agentproto.MaxReadBytes})
	if res.Length <= 0 || res.EOF || !fitsRead(res) {
		t.Fatalf("not shrunk to fit: length=%d eof=%v", res.Length, res.EOF)
	}
	next := readReq(t, c, agentproto.ReadFileRequest{Path: "nul", Offset: res.Length, Length: 16})
	if next.Offset != res.Length || next.Length != 16 {
		t.Fatalf("paging: %+v", next)
	}
}

func fitsRead(r *agentproto.ReadFileResult) bool {
	return fits(&agentproto.Response{Build: agentproto.BuildID, ReadFile: r})
}

func TestRead_Errors(t *testing.T) {
	c := newCfg(t)
	if _, perr := c.ReadFile(&agentproto.ReadFileRequest{Path: "missing"}); perr == nil || perr.Code != agentproto.CodeOp {
		t.Errorf("missing: %+v", perr)
	}
	if _, perr := c.ReadFile(&agentproto.ReadFileRequest{Path: "."}); perr == nil {
		t.Error("directory read succeeded")
	}
	for name, req := range map[string]agentproto.ReadFileRequest{
		"neg offset": {Path: "x", Offset: -1},
		"too long":   {Path: "x", Length: agentproto.MaxReadBytes + 1},
		"empty":      {},
	} {
		if _, perr := c.ReadFile(&req); perr == nil || perr.Code != agentproto.CodeBadRequest {
			t.Errorf("%s: %+v", name, perr)
		}
	}
	fifo := filepath.Join(c.Home, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan *agentproto.Error, 1)
	go func() { _, perr := c.ReadFile(&agentproto.ReadFileRequest{Path: fifo}); done <- perr }()
	select {
	case perr := <-done:
		if perr == nil {
			t.Error("FIFO read succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading a FIFO hung the worker")
	}
}
