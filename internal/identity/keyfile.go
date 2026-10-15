package identity

import (
	"log/slog"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
)

// TokenPrefix gives dozor key-file tokens the form "dzk1_...".
const TokenPrefix = "dzk"

// KeyfileOptions is the one keyfile.Options for dozor, used by the listener
// and by `dozor mcp-key lint`, so what lint accepts is what the server loads.
// Tools is the agent tool set: an entry naming any other tool is dropped by
// keyfile itself.
func KeyfileOptions(path string, log *slog.Logger) keyfile.Options {
	return keyfile.Options{
		Path:             path,
		Prefix:           TokenPrefix,
		Tools:            agentsurface.ToolNames,
		ReservedPrefixes: ReservedPrefixes,
		ValidateAttrs:    ValidateAttrs,
		Logger:           log,
	}
}
