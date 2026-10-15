package identity

import (
	"strings"
	"testing"
)

func attrs(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestValidate_Accepts(t *testing.T) {
	for _, a := range []map[string]string{
		attrs("role", "agent-user", "run_as", "muse"),
		attrs("role", "agent-user", "run_as", "muse-2_x"),
	} {
		if err := ValidateAttrs("k", a); err != nil {
			t.Errorf("%v rejected: %v", a, err)
		}
	}
}

func TestValidate_RequiredAttrs(t *testing.T) {
	for name, a := range map[string]map[string]string{
		"no attrs":     nil,
		"no role":      attrs("run_as", "muse"),
		"empty role":   attrs("role", "", "run_as", "muse"),
		"no run_as":    attrs("role", "agent-user"),
		"unknown role": attrs("role", "reader", "run_as", "muse"),
	} {
		if ValidateAttrs("k", a) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// RED-on-revert: delete the roleOperator case in ValidateAttrs.
func TestValidate_OperatorRoleRejected(t *testing.T) {
	err := ValidateAttrs("k", attrs("role", "operator", "run_as", "muse"))
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("role=operator: %v", err)
	}
}

// RED-on-revert: drop the Reserved(runAs) check (operator/env-/system-), or
// an entry from agentproto's reserved list (root/krolik/ubuntu).
func TestValidate_RunAsReserved(t *testing.T) {
	for _, bad := range []string{
		"root", "krolik", "ubuntu", // deployment accounts, agentproto.ValidRunAs
		"operator", "operator-2", "env-prod", "system-core", // reserved namespaces
		"", "a", "Muse", "1abc", "has space", "x;y", strings.Repeat("a", 40), "../etc",
	} {
		if err := ValidateAttrs("k", attrs("role", "agent-user", "run_as", bad)); err == nil {
			t.Errorf("run_as=%q accepted", bad)
		}
	}
}

// RED-on-revert: delete the unknown-key loop in ValidateAttrs.
func TestValidate_UnknownAttr(t *testing.T) {
	for _, extra := range []string{"exec", "home", "tools", "uid", "readonly"} {
		a := attrs("role", "agent-user", "run_as", "muse", extra, "x")
		if err := ValidateAttrs("k", a); err == nil {
			t.Errorf("unknown attr %q accepted", extra)
		}
	}
}

// Error text is logged; attacker-controlled values are clamped.
func TestValidate_ErrorsAreClamped(t *testing.T) {
	long := strings.Repeat("x", 500) + "\nforged log line"
	for _, a := range []map[string]string{
		attrs("role", "agent-user", "run_as", long),
		attrs("role", long, "run_as", "muse"),
	} {
		err := ValidateAttrs("k", a)
		if err == nil || len(err.Error()) > 120 || strings.ContainsAny(err.Error(), "\n\r") {
			t.Errorf("unclamped error: %q", err)
		}
	}
}
