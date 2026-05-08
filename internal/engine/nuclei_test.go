package engine

import "testing"

func TestNucleiFinding_ToAlertLevel_NormalisesCase(t *testing.T) {
	cases := []struct {
		sev  string
		want AlertLevel
	}{
		{"critical", AlertCritical},
		{"CRITICAL", AlertCritical},
		{"crit", AlertCritical},
		{"high", AlertError},
		{"error", AlertError},
		{"medium", AlertWarning},
		{"warning", AlertWarning},
		{"low", AlertInfo},
		{"info", AlertInfo},
		{"informational", AlertInfo},
		{"unknown_garbage", AlertWarning},
		{"", AlertWarning},
	}
	for _, c := range cases {
		f := NucleiFinding{Info: NucleiInfo{Severity: c.sev}}
		if got := f.ToAlertLevel(); got != c.want {
			t.Errorf("severity %q → %v, want %v", c.sev, got, c.want)
		}
	}
}
