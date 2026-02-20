package tools

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// ValidSecurityModes lists accepted values for the exec security mode.
var ValidSecurityModes = []string{"safe", "ask", "full"}

// ExecConfig holds the mutable default security mode for server_exec.
// Thread-safe; shared between the exec tool and the exec_security tool.
type ExecConfig struct {
	mu       sync.RWMutex
	security string
}

// NewExecConfig creates an ExecConfig seeded from DOZOR_EXEC_SECURITY (default: "ask").
func NewExecConfig() *ExecConfig {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("DOZOR_EXEC_SECURITY")))
	if !isValidSecurityMode(mode) {
		mode = "ask"
	}
	return &ExecConfig{security: mode}
}

// Get returns the current default security mode.
func (c *ExecConfig) Get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.security
}

// Set updates the default security mode. Returns an error if the mode is invalid.
func (c *ExecConfig) Set(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if !isValidSecurityMode(mode) {
		return fmt.Errorf("invalid security mode %q: must be one of %v", mode, ValidSecurityModes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.security = mode
	return nil
}

func isValidSecurityMode(mode string) bool {
	for _, v := range ValidSecurityModes {
		if mode == v {
			return true
		}
	}
	return false
}
