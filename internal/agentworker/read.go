package agentworker

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// ReadFile returns a byte range of a regular file. Valid UTF-8 is returned as
// text, anything else as base64. The range is shortened, never failed, when
// the encoded reply would exceed the reply cap; the caller pages with Offset.
func (c *Config) ReadFile(req *agentproto.ReadFileRequest) (*agentproto.ReadFileResult, *agentproto.Error) {
	if req == nil {
		return nil, badReq("read_file is required")
	}
	path, err := c.resolve(req.Path)
	if err != nil {
		return nil, badReq(err.Error())
	}
	if req.Offset < 0 || req.Length < 0 || req.Length > agentproto.MaxReadBytes {
		return nil, badReq("offset/length out of range")
	}
	length := req.Length
	if length == 0 {
		length = agentproto.DefaultReadBytes
	}
	// O_NONBLOCK so opening a FIFO cannot hang the worker; it has no effect
	// on regular files.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, opErr(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, opErr(err)
	}
	if !st.Mode().IsRegular() {
		return nil, opErr(errors.New("not a regular file"))
	}
	size := st.Size()

	for {
		buf := make([]byte, length)
		n, rerr := f.ReadAt(buf, req.Offset)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, opErr(rerr)
		}
		buf = buf[:n]
		res := encodeRead(path, size, req.Offset, buf)
		if fits(&agentproto.Response{Build: agentproto.BuildID, ReadFile: res}) || length <= 1 {
			return res, nil
		}
		length /= 2
	}
}

func encodeRead(path string, size, offset int64, buf []byte) *agentproto.ReadFileResult {
	// A range that ends mid-rune would flip to base64; give the partial rune
	// back to the next page unless the file really ends there.
	if offset+int64(len(buf)) < size && !utf8.Valid(buf) {
		for k := 1; k < utf8.UTFMax && k < len(buf); k++ {
			if utf8.Valid(buf[:len(buf)-k]) {
				buf = buf[:len(buf)-k]
				break
			}
		}
	}
	res := &agentproto.ReadFileResult{
		Path: path, Size: size, Offset: offset, Length: int64(len(buf)),
		EOF: offset+int64(len(buf)) >= size,
	}
	if utf8.Valid(buf) {
		res.Encoding, res.Content = agentproto.EncodingUTF8, string(buf)
	} else {
		res.Encoding, res.Content = agentproto.EncodingBase64, base64.StdEncoding.EncodeToString(buf)
	}
	return res
}
