package agent

func newOMPAgent() *localACPAgent {
	cmd, args := resolveOMPCommand()
	return newLocalACPAgent(localACPConfig{
		name:             "omp",
		command:          cmd,
		args:             args,
		authMethod:       "agent",
		autoApproveTools: true,
	})
}

func resolveOMPCommand() (string, []string) {
	return "omp", []string{"acp"}
}

func newClaudeAgent() *localACPAgent {
	cmd, args := resolveClaudeCommand()
	return newLocalACPAgent(localACPConfig{
		name:        "claude",
		command:     cmd,
		args:        args,
		installHint: "install it with: npm install -g @agentclientprotocol/claude-agent-acp",
	})
}

func resolveClaudeCommand() (string, []string) {
	return "claude-agent-acp", nil
}

func newCodexAgent() *localACPAgent {
	cmd, args := resolveCodexCommand()
	return newLocalACPAgent(localACPConfig{
		name:        "codex",
		command:     cmd,
		args:        args,
		installHint: "install it with: npm install -g @agentclientprotocol/codex-acp",
	})
}

func resolveCodexCommand() (string, []string) {
	return "codex-acp", nil
}
