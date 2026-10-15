package agentproto

import (
	"fmt"
	"path/filepath"
)

// SocketPath returns the worker socket for an agent identity under dir
// (normally SocketDir). The identity must pass ValidRunAs, so it can never
// contain a path separator or select a socket that belongs to a non-agent
// account. Only the verified identity is ever passed here, never tool input.
func SocketPath(dir, runAs string) (string, error) {
	if !ValidRunAs(runAs) {
		return "", fmt.Errorf("agentproto: invalid identity %q", runAs)
	}
	return filepath.Join(dir, runAs+".sock"), nil
}
