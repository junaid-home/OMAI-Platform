package reflection

import (
	"testing"

	uabv1 "github.com/omai/backend/gen/go/uab/v1"
)

func TestVoiceEligibilityHonorsExplicitModalities(t *testing.T) {
	tests := []struct {
		name string
		tool *uabv1.ReflectedTool
		want bool
	}{
		{name: "explicit voice", tool: &uabv1.ReflectedTool{Name: "navigate", Executor: "client.portal", Modalities: []string{"text", "voice"}}, want: true},
		{name: "explicit text only", tool: &uabv1.ReflectedTool{Name: "text_only", Executor: "go.git", Modalities: []string{"text"}}, want: false},
		{name: "default backend tool", tool: &uabv1.ReflectedTool{Name: "git_status", Executor: "go.git"}, want: true},
		{name: "default process tool", tool: &uabv1.ReflectedTool{Name: "write_terminal", Executor: "go.process"}, want: false},
		{name: "health", tool: &uabv1.ReflectedTool{Name: "system_health", Executor: "go.control-plane"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := voiceEligible(test.tool); got != test.want {
				t.Fatalf("voiceEligible()=%v want %v", got, test.want)
			}
		})
	}
}

func TestFullPermissionVoiceCatalogIncludesWorkspaceMutations(t *testing.T) {
	registry, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := registry.VoiceTools([]string{"*"})
	if len(tools) != 50 {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.GetName())
		}
		t.Fatalf("voice tools = %d, want 50: %v", len(tools), names)
	}
	wanted := map[string]bool{
		"create_directory":      false,
		"move_workspace_path":   false,
		"delete_workspace_path": false,
	}
	for _, tool := range tools {
		if _, ok := wanted[tool.GetName()]; ok {
			wanted[tool.GetName()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("voice catalog does not expose %q", name)
		}
	}
}
