# Chat commands

Send these as messages in any supported IM conversation.

| Command | Description |
|---------|-------------|
| `/agent` | Show the current agent and available agents. |
| `/agent <name>` | Switch to the named agent. The current session is closed; the next message starts a new session. |
| `/proj` | Show the current project and available projects. |
| `/proj <name>` | Switch to the named project. The current session is closed; the next message starts a new session in the new working directory. |
| `/p` | Show current agent, project, active model, and context usage. |
| `/esc` | Cancel the currently generating reply. Sends `session/cancel` so the agent stops immediately instead of burning tokens. |
| `/new` | Close the current session and start a fresh conversation on the next message. |
| `/ls` | List the current agent's own historical sessions for the current project's working directory. |
| `/sw <n or id>` | Switch to one of the sessions listed by `/ls` (by 1-based index or session-id prefix). The next message resumes that conversation. |
| `/display` | Toggle stream display between **full** (thinking + tools inline) and simplified (body text only). `/display full` or `/display off` sets it explicitly. |
| `//<cmd>` | Pass a slash command through to the agent (e.g. `//web query` sends `/web query` to the agent as a normal prompt). |
| `/help`, `/?` | Show the command list. |

## Status output (`/p`)

`/p` replies with a markdown list. Example:

```markdown
## Status

- **Agent:** omp
- **Project:** default
- **Model:** kimi-code/kimi-for-coding
- **Reasoning effort:** auto
- **Context:** 8% / 262K
```

- `Model` and `Reasoning effort` show the active model configuration from the agent.
- `Context` shows used context as a percentage and the total context window.
- `Tokens` shows input / output token counts for the current turn.
- `Command` is displayed only while an `execute` tool is running.

## Session listing and switching (`/ls`, `/sw`)

`/ls` queries the current agent's ACP `session/list` method (spawning a short-lived instance of the agent's own ACP command) and lists historical sessions whose working directory matches the current project, up to 20, most recent first.

`/sw <n>` resumes the n-th session from the last `/ls` output; `/sw <id>` resumes by session-id prefix. The current session is closed and the selected one is resumed on the next message. The current agent and project selection are preserved.

## Slash command passthrough (`//`)

Agents advertise their own slash commands (e.g. `/web`, `/test`, `/plan`). omp-im intercepts messages starting with a single `/` as its own commands. To send a slash command to the agent, prefix it with an extra `/`:

```
//web search for agent client protocol
```

omp-im strips one `/` and sends `/web search for agent client protocol` as a normal prompt. The agent recognizes the `/web` prefix and processes it.
