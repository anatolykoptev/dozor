package agentworker

import (
	"encoding/json"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// fits reports whether resp encodes within the reply cap, leaving room for
// the trailing newline and the envelope.
func fits(resp *agentproto.Response) bool {
	resp.V = agentproto.ProtoVersion
	b, err := json.Marshal(resp)
	return err == nil && len(b)+1 <= agentproto.MaxReplyBytes
}
