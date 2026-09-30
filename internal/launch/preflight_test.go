package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoWith(t *testing.T, mcp, settings string) string {
	t.Helper()
	dir := t.TempDir()
	if mcp != "" {
		if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckMCPApproved(t *testing.T) {
	two := `{"mcpServers":{"nugit":{"command":"nugit"},"other":{"command":"x"}}}`
	tests := []struct {
		name, mcp, settings, wantErr string
	}{
		{"no .mcp.json", "", "", ""},
		{"no servers", `{"mcpServers":{}}`, "", ""},
		{"all approved", two, `{"enabledMcpjsonServers":["other","nugit"]}`, ""},
		{"enable all", two, `{"enableAllProjectMcpServers":true}`, ""},
		{"one missing", two, `{"enabledMcpjsonServers":["nugit"]}`, "not pre-approved"},
		{"no settings", two, "", "nugit, other"},
		{"bad .mcp.json", "{", "", "parse"},
		{"bad settings", two, "{", "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckMCPApproved(repoWith(t, tt.mcp, tt.settings))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckMCPApproved = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckMCPApproved = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// The committed repo must pass its own preflight.
func TestCheckMCPApprovedRepo(t *testing.T) {
	if err := CheckMCPApproved(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}
