# Chat commands

Send these as messages in any supported IM conversation.

| Command | Description |
|---------|-------------|
| `/agent` | Show the current agent and available agents. |
| `/agent <name>` | Switch to the named agent. The current session is closed; the next message starts a new session. |
| `/proj` | Show the current project and available projects. |
| `/proj <name>` | Switch to the named project. The current session is closed; the next message starts a new session in the new working directory. |
| `/p` | Show current agent, project, session status, turn timing, tool usage, and token counts. |
| `/esc` | Cancel the currently generating agent reply. |
| `/new` | Close the current session and start a fresh conversation on the next message. |
| `/help`, `/?` | Show the command list. |

## Status output (`/p`)

`/p` replies with a markdown list. Example:

```markdown
## Status

- **Agent:** omp
- **Project:** default
- **Model:** kimi-code/kimi-for-coding
- **Reasoning effort:** auto
- **Elapsed:** 45s
- **Context:** 8% / 262K
- **Tools used:** 2
- **Current tool:** 12s
- **Command:** `mkdir -p caopan && ...`
- **Tokens:** 1234 / 567
```

- `Model` and `Reasoning effort` show the active model configuration from the agent.
- `Context` shows used context as a percentage and the total context window.
- `Tokens` shows input / output token counts for the current turn.
- `Command` is displayed only while an `execute` tool is running.
