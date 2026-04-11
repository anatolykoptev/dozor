package mcpclient

import (
	"context"
	"strings"
	"testing"
)

func TestKbDeleteTool_Name(t *testing.T) {
	tool := &kbDeleteTool{}
	if got := tool.Name(); got != "memdb_delete" {
		t.Errorf("expected memdb_delete, got %q", got)
	}
}

func TestKbDeleteTool_Description_MentionsReasonRequired(t *testing.T) {
	desc := (&kbDeleteTool{}).Description()
	if !strings.Contains(strings.ToLower(desc), "reason") {
		t.Error("description should mention the reason parameter")
	}
	if !strings.Contains(strings.ToLower(desc), "required") {
		t.Error("description should state that reason is required")
	}
}

func TestKbDeleteTool_Parameters_Schema(t *testing.T) {
	params := (&kbDeleteTool{}).Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["memory_ids"]; !ok {
		t.Error("parameters must include memory_ids")
	}
	if _, ok := props["reason"]; !ok {
		t.Error("parameters must include reason")
	}
	required, _ := params["required"].([]string)
	if len(required) != 2 {
		t.Errorf("both memory_ids and reason must be required, got %v", required)
	}
}

func TestKbDeleteTool_Execute_RejectsEmptyIDs(t *testing.T) {
	tool := &kbDeleteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"memory_ids": []any{},
		"reason":     "cleanup",
	})
	if err == nil {
		t.Error("expected rejection for empty memory_ids")
	}
}

func TestKbDeleteTool_Execute_RejectsEmptyReason(t *testing.T) {
	tool := &kbDeleteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"memory_ids": []any{"id-123"},
		"reason":     "",
	})
	if err == nil {
		t.Error("expected rejection for empty reason")
	}
}

func TestKbDeleteTool_Execute_RejectsMissingReason(t *testing.T) {
	tool := &kbDeleteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"memory_ids": []any{"id-123"},
	})
	if err == nil {
		t.Error("expected rejection for missing reason")
	}
}

func TestKbDeleteTool_Execute_FiltersNonStringIDs(t *testing.T) {
	tool := &kbDeleteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"memory_ids": []any{42, true, ""},
		"reason":     "cleanup",
	})
	if err == nil {
		t.Error("expected rejection when no valid string IDs remain")
	}
	if !strings.Contains(err.Error(), "no valid string") {
		t.Errorf("unexpected error: %v", err)
	}
}
