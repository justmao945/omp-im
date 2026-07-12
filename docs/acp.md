# ACP integration

`omp-im` communicates with local agents using the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/). The protocol implementation lives in `internal/acp` and is transport-agnostic; `internal/agent/omp` is a thin launcher that starts the `omp acp` command and wires it to the ACP client.

## Session lifecycle

One ACP session is created per IM conversation. The session is keyed by the platform plus the user ID (`platform:userId`).

1. `initialize` — protocol and capability exchange.
2. `authenticate` — attempted unconditionally with the `agent` method (uses local `~/.omp` credentials).
3. `session/new` — creates a new ACP session for a working directory.
4. `session/prompt` — sends the user message to the agent.
5. `session/update` — agent streams assistant text, tool calls, and attachments back to `omp-im`.

## Session persistence

If the agent advertises support, `omp-im` can resume a previous session after a restart instead of starting a new one. `omp-im` persists agent session IDs in `~/.omp-im/sessions.json`.

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
