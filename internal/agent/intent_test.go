package agent

import "testing"

func TestNeedsMemoryContext(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"привет", false},
		{"hi", false},
		{"ok", false},
		{"да", false},
		{"hello there my friend", false},
		{"Почему сервер не отвечает?", true},
		{"Проверь статус nginx", true},
		{"What happened with the deploy yesterday?", true},
		{"a", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NeedsMemoryContext(tt.input)
			if got != tt.want {
				t.Errorf("NeedsMemoryContext(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
