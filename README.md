# omp-im

Run local ACP coding agents from Weixin or WeCom. Each chat gets an isolated agent session and working directory; replies stream back to the IM conversation.

## Quick start

Requirements: Go and the agent command selected by `default.agent`.

```bash
mkdir -p ~/.omp-im
cp config.example.json ~/.omp-im/config.json
# Edit work_dir and retain only the platform(s) you configure.
go run ./cmd/omp-im -config ~/.omp-im/config.json -log-level info
```

`config.example.json` contains both Weixin and WeCom examples. Remove an unconfigured platform: WeCom requires both `bot_id` and `secret`.

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

> `omp` tool calls are auto-approved. Restrict platform access with `allow_from` and `group_allow_from` before exposing it to other users.

## Platforms

### Weixin

Leave `platforms[].options.token` empty, then log in interactively:

```bash
omp-im -config ~/.omp-im/config.json weixin login
```

The QR-code session is retained under `~/.omp-im/weixin/`. Set `token` instead when using an existing iLink bot token. Limit senders with `allow_from` (`"*"` or empty permits everyone). A turn-summary footer (⏱️ elapsed · 🧠 context%) is appended to each reply by default; set `footer` to `false` to disable it.

### WeCom

Configure a `wecom` platform with `bot_id` and `secret`. It connects to the AI bot WebSocket gateway, supports direct and group chats, and accepts text, images, files, voice, mixed messages, and quoted messages. Replies stream by default; set `stream` to `false` to send only a completed reply. A turn-summary footer (⏱️ elapsed · 🧠 context%) is appended by default; set `footer` to `false` to disable it. Quote content is appended to the agent prompt under `[quoted message]`.

Use `allow_from` for direct-message user IDs and `group_allow_from` for group chat IDs. Both default to allowing everyone; configure explicit IDs in production. Set the top-level `display` option to `"full"` to show agent thinking and tool activity inline during streaming (default is body-text-only); toggle it at runtime with the `/display` command.

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
