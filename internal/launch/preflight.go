package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CheckMCPApproved fails if any server in repo's .mcp.json is not listed in
// enabledMcpjsonServers of repo's .claude/settings.json. Claude Code asks
// before it starts an unapproved project MCP server, and an unattended stage
// session would stall on that prompt. A repo without .mcp.json passes.
// `greenhouse doctor` runs this before the factory launches anything.
func CheckMCPApproved(repo string) error {
	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	switch err := readJSON(filepath.Join(repo, ".mcp.json"), &mcp); {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if len(mcp.Servers) == 0 {
		return nil
	}

	var settings struct {
		Enabled   []string `json:"enabledMcpjsonServers"`
		EnableAll bool     `json:"enableAllProjectMcpServers"`
	}
	settingsPath := filepath.Join(repo, ".claude", "settings.json")
	if err := readJSON(settingsPath, &settings); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if settings.EnableAll {
		return nil
	}
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(mcp.Servers)) {
		if !slices.Contains(settings.Enabled, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("MCP servers in .mcp.json not pre-approved in %s enabledMcpjsonServers (a stage session would stall on the approval prompt): %s",
			settingsPath, strings.Join(missing, ", "))
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
