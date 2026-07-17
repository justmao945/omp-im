# omp-im

Run local ACP coding agents from Weixin. Each chat gets an isolated agent session and working directory; replies stream back to the IM conversation.

## Quick start

Requirements: Go and the agent command selected by `default.agent`.

```bash
mkdir -p ~/.omp-im
cp config.example.json ~/.omp-im/config.json
# Edit work_dir and retain only the platform(s) you configure.
go run ./cmd/omp-im -config ~/.omp-im/config.json -log-level info
```

`config.example.json` contains a Weixin example. Edit the `work_dir` before running.

Build an executable when running persistently:

```bash
make build
make install-user # installs ~/.local/bin/omp-im
```

## Agents

| Config name | Local ACP command | Installation |
| --- | --- | --- |
| `omp` | `omp acp` | Install and authenticate the `omp` CLI. |
| `claude` | `claude-agent-acp` | `npm install -g @agentclientprotocol/claude-agent-acp` |
| `codex` | `codex-acp` | `npm install -g @agentclientprotocol/codex-acp` |

Claude Code and Codex use their own local CLI credentials. `omp-im` reports the installation command when a selected ACP adapter is unavailable.

> `omp` tool calls are auto-approved. Restrict platform access with `allow_from` before exposing it to other users.

## Platforms

### Weixin

Leave `platforms[].options.token` empty, then log in interactively:

```bash
omp-im -config ~/.omp-im/config.json weixin login
```

The QR-code session is retained under `~/.omp-im/weixin/`. Set `token` instead when using an existing iLink bot token. Limit senders with `allow_from` (`"*"` or empty permits everyone). A turn-summary footer (⏱️ elapsed · 🧠 context%) is appended to each reply by default; turn it off with `/display footer off` or set `display.footer` to `false` in the config.

### Local HTTP testing

Use the test-only HTTP platform without an IM account:

```json
{"type":"http","options":{"addr":":8080"}}
```

Then POST a message:

```bash
curl -X POST http://localhost:8080/send \
  -H 'Content-Type: application/json' \
  -d '{"session_key":"test:u1","user_id":"u1","content":"hello"}'
```

## Operations

| Command | Purpose |
| --- | --- |
| `/agent [name]` | Show or switch agent. |
| `/proj [name]` | Show or switch project. |
| `/p` | Show agent, model, turn status, and token usage. |
| `/esc` | Cancel the active reply. |
| `/new` | Start a fresh conversation. |
| `/ls` | List the current agent's own historical sessions for this project. |
| `/sw <n or id>` | Switch to one of the listed sessions (resumes it next message). |

Agent session IDs persist in `~/.omp-im/sessions.db` by default (bbolt). On restart, `omp-im` resumes ACP sessions when the selected adapter supports it. `/new`, an agent switch, or a project switch clears the saved session.

### PM2

`ecosystem.config.js` runs the installed binary at `~/.local/bin/omp-im` from `~/.omp-im` using `config.json`.

```bash
make install-user
pm2 delete omp-im 2>/dev/null || true
pm2 start ecosystem.config.js
pm2 logs omp-im
pm2 restart omp-im
make pm2-stop
```

## Reference

- [Configuration](docs/config.md)
- [Chat commands](docs/commands.md)
- [ACP integration](docs/acp.md)
