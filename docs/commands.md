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

Example:

```
Agent: omp
Project: default
Model: gpt-4o
Status: using_tools
Elapsed: 45s
Tools used: 2
Current tool: 12s
Command: mkdir -p caopan && ...
Tokens: 1234 / 567
```

- `Tokens` shows input / output token counts for the current turn.
- `Command` is displayed only while an `execute` tool is running.
