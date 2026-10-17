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
func CheckMCPApproved(repo string) error {
	return CheckMCPApprovedIn(repo, func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(repo, path))
	})
}

// CheckMCPApprovedIn is CheckMCPApproved over read, which returns a file by
// its repo-relative path, or an error wrapping os.ErrNotExist when there is
// none. `greenhouse doctor` reads the base branch with it, which is what a
// stage session's worktree starts from, rather than the checkout's working
// tree. where names the files' location in errors.
func CheckMCPApprovedIn(where string, read func(path string) ([]byte, error)) error {
	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	switch err := readJSON(read, ".mcp.json", &mcp); {
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
	settingsPath := filepath.Join(".claude", "settings.json")
	if err := readJSON(read, settingsPath, &settings); err != nil && !errors.Is(err, os.ErrNotExist) {
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
			filepath.Join(where, settingsPath), strings.Join(missing, ", "))
	}
	return nil
}

func readJSON(read func(string) ([]byte, error), path string, v any) error {
	b, err := read(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
