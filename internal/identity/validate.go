package identity

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// Key-file attribute names and the one role v1 knows.
const (
	AttrRole      = "role"
	AttrRunAs     = "run_as"
	RoleAgentUser = "agent-user"

	// roleOperator is named only to be refused: the master token is the
	// operator and never lives in the key file.
	roleOperator = "operator"

	maxEchoLen = 32
)

// ReservedPrefixes are key names (and run_as names) no key file entry may use:
// the built-in operator identity and the namespaces other tooling owns.
var ReservedPrefixes = []string{"operator", "env-", "system-"}

// ValidateAttrs is keyfile.Options.ValidateAttrs for dozor. It is pure: a
// syntax and policy check on one entry's attrs, with no user lookup and no
// environment. It must be called for every entry on every reload, so it
// requires the attrs a missing field would otherwise default to nothing
// for: an entry without a role or run_as is dropped, not granted defaults.
//
// Error text is logged next to the entry name and never contains a secret;
// attacker-supplied values are clamped.
func ValidateAttrs(_ string, attrs map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		if k != AttrRole && k != AttrRunAs {
			return fmt.Errorf("unknown attr %q", clamp(k))
		}
	}
	role := attrs[AttrRole]
	switch {
	case role == "":
		return errors.New("attr role is required")
	case role == roleOperator:
		return errors.New("role operator is not allowed in the key file")
	case role != RoleAgentUser:
		return fmt.Errorf("unknown role %q", clamp(role))
	}
	runAs := attrs[AttrRunAs]
	switch {
	case runAs == "":
		return errors.New("attr run_as is required")
	case !agentproto.ValidRunAs(runAs):
		return fmt.Errorf("run_as %q is not a valid agent identity", clamp(runAs))
	case Reserved(runAs):
		return fmt.Errorf("run_as %q uses a reserved name", clamp(runAs))
	}
	return nil
}

// Reserved reports whether name falls under ReservedPrefixes.
func Reserved(name string) bool {
	for _, p := range ReservedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func clamp(s string) string {
	if len(s) > maxEchoLen {
		s = s[:maxEchoLen]
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
