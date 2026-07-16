package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// loadMCPServers resolves MCP configuration through the same source used by
// the selected standalone agent, then converts it to ACP's session format.
func loadMCPServers(agentName, workDir string) ([]any, error) {
	switch agentName {
	case "omp":
		return loadMCPServersFromFile(filepath.Join(".omp", "agent", "mcp.json"))
	case "claude":
		return loadClaudeMCPServers(workDir)
	case "codex":
		return loadCodexMCPServers()
	default:
		return nil, nil
	}
}

func loadMCPServersFromFile(relativePath string) ([]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory for MCP configuration: %w", err)
	}
	contents, err := os.ReadFile(filepath.Join(home, relativePath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read user MCP configuration: %w", err)
	}
	return parseMCPServers(contents)
}

func loadClaudeMCPServers(workDir string) ([]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory for Claude MCP configuration: %w", err)
	}
	contents, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Claude MCP configuration: %w", err)
	}
	var config struct {
		Servers  map[string]json.RawMessage `json:"mcpServers"`
		Projects map[string]struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		return nil, fmt.Errorf("parse Claude MCP configuration: %w", err)
	}
	if project, ok := config.Projects[workDir]; ok {
		config.Servers = project.Servers
	}
	return parseMCPServerMap(config.Servers)
}

func loadCodexMCPServers() ([]any, error) {
	output, err := exec.Command("codex", "mcp", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("list Codex MCP servers: %w", err)
	}
	var listed []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Cwd     string            `json:"cwd"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(output, &listed); err != nil {
		return nil, fmt.Errorf("parse Codex MCP server list: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory for Codex MCP configuration: %w", err)
	}

	servers := make([]any, 0, len(listed))
	for _, server := range listed {
		if !server.Enabled {
			continue
		}
		cwd := server.Transport.Cwd
		if cwd != "" && !filepath.IsAbs(cwd) {
			cwd = filepath.Join(home, ".codex", cwd)
		}
		switch server.Transport.Type {
		case "stdio":
			servers = append(servers, map[string]any{
				"name":    server.Name,
				"command": resolveMCPPath(cwd, server.Transport.Command),
				"args":    resolveMCPArgs(cwd, server.Transport.Args),
				"env":     nameValues(server.Transport.Env),
			})
		case "http", "sse":
			servers = append(servers, map[string]any{
				"name":    server.Name,
				"type":    server.Transport.Type,
				"url":     server.Transport.URL,
				"headers": nameValues(server.Transport.Headers),
			})
		default:
			return nil, fmt.Errorf("Codex MCP server %q uses unsupported transport %q", server.Name, server.Transport.Type)
		}
	}
	return servers, nil
}

func resolveMCPArgs(cwd string, args []string) []string {
	resolved := make([]string, len(args))
	for i, arg := range args {
		resolved[i] = resolveMCPPath(cwd, arg)
	}
	return resolved
}

func resolveMCPPath(cwd, value string) string {
	if cwd == "" || filepath.IsAbs(value) || !strings.HasPrefix(value, ".") {
		return value
	}
	return filepath.Join(cwd, value)
}

func parseMCPServers(contents []byte) ([]any, error) {
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		return nil, fmt.Errorf("parse MCP configuration: %w", err)
	}
	return parseMCPServerMap(config.Servers)
}

func parseMCPServerMap(config map[string]json.RawMessage) ([]any, error) {
	servers := make([]any, 0, len(config))
	for name, raw := range config {
		var server struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Enabled *bool             `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &server); err != nil {
			return nil, fmt.Errorf("parse MCP server %q: %w", name, err)
		}
		if server.Enabled != nil && !*server.Enabled {
			continue
		}

		switch {
		case server.Command != "":
			servers = append(servers, map[string]any{
				"name":    name,
				"command": server.Command,
				"args":    server.Args,
				"env":     nameValues(server.Env),
			})
		case server.Type == "http" && server.URL != "":
			servers = append(servers, map[string]any{
				"name":    name,
				"type":    "http",
				"url":     server.URL,
				"headers": nameValues(server.Headers),
			})
		case server.Type == "sse" && server.URL != "":
			servers = append(servers, map[string]any{
				"name":    name,
				"type":    "sse",
				"url":     server.URL,
				"headers": nameValues(server.Headers),
			})
		default:
			return nil, fmt.Errorf("MCP server %q has unsupported configuration", name)
		}
	}
	return servers, nil
}

func nameValues(values map[string]string) []map[string]string {
	result := make([]map[string]string, 0, len(values))
	for name, value := range values {
		result = append(result, map[string]string{"name": name, "value": value})
	}
	return result
}
