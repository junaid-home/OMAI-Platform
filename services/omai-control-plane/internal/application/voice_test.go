package application

import (
	"encoding/json"
	"testing"

	uabv1 "github.com/omai/backend/gen/go/uab/v1"
)

func TestHideVoiceBoundFields(t *testing.T) {
	tool := &uabv1.ReflectedTool{InputSchemaJson: []byte(`{"type":"object","properties":{"workspaceId":{"type":"string"},"root":{"type":"string"},"query":{"type":"string"}},"required":["workspaceId","root","query"]}`), RequiredFields: []string{"workspace_id", "root", "query"}}
	if err := hideVoiceBoundFields(tool); err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchemaJson, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["workspaceId"]; ok {
		t.Fatal("workspaceId leaked into provider schema")
	}
	if _, ok := properties["root"]; ok {
		t.Fatal("root leaked into provider schema")
	}
	if _, ok := properties["query"]; !ok {
		t.Fatal("caller-owned field was removed")
	}
	if len(tool.RequiredFields) != 1 || tool.RequiredFields[0] != "query" {
		t.Fatalf("required fields=%v", tool.RequiredFields)
	}
}
