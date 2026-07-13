# ACP integration

`omp-im` communicates with local agents using the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/). The protocol implementation and local agent launchers both live in `internal/agent`.

## Built-in adapters

`omp-im` starts a local ACP server for the selected agent. Install the adapters once on the host that runs `omp-im`:

```bash
npm install -g @agentclientprotocol/claude-agent-acp
npm install -g @agentclientprotocol/codex-acp
```

The corresponding `/agent` names and commands are:

| Agent | ACP command |
| --- | --- |
| `omp` | `omp acp` |
| `claude` | `claude-agent-acp` |
| `codex` | `codex-acp` |

Claude Code and Codex authentication is handled by their own local CLI and credentials. `omp-im` does not issue an ACP authentication request for either adapter. If either adapter command is missing, session startup reports its exact installation command.

## Session lifecycle

One ACP session is created per IM conversation. The session is keyed by the platform plus the user ID (`platform:userId`).

1. `initialize` — protocol and capability exchange.
2. `authenticate` — sent only when the selected adapter requires an ACP auth method; local Claude and Codex adapters use their own CLI credentials.
3. `session/new` — creates a new ACP session for a working directory.
4. `session/prompt` — sends the user message to the agent.
5. `session/update` — agent streams assistant text, tool calls, and attachments back to `omp-im`.

## Session persistence

If the agent advertises support, `omp-im` can resume a previous session after a restart instead of starting a new one. `omp-im` persists agent session IDs in `~/.omp-im/sessions.db` by default.

Two ACP methods are used, in order of preference:

- `session/resume` — restores the session without replaying conversation history. Requires `sessionCapabilities.resume` in the `initialize` response.
- `session/load` — restores the session and replays the full conversation history as `session/update` notifications. Requires `agentCapabilities.loadSession` in the `initialize` response.

If neither is supported, the conversation starts fresh with `session/new`.

Session IDs are saved when a session is created and removed when the user explicitly starts a new session (`/new`) or switches agent/project.

## References

- [ACP overview](https://agentclientprotocol.com/protocol/v1/overview)
- [ACP session setup](https://agentclientprotocol.com/protocol/v1/session-setup)
- [Resuming existing sessions](https://agentclientprotocol.com/rfds/session-resume)
- [Session list](https://agentclientprotocol.com/rfds/session-list)
- [Closing active sessions](https://agentclientprotocol.com/rfds/session-close)
