# omp-im

IM connector for local AI agents. Currently supports Weixin (personal) via the iLink Bot protocol and WeCom (enterprise) via AI bot WebSocket long connection; more platforms may follow.

## Run

```bash
mkdir -p ~/.omp-im
cp config.example.json ~/.omp-im/config.json
# Edit projects and optionally set Weixin token, then run.
go run ./cmd/omp-im
```

Or build and install a binary:

```bash
make build
make install        # installs to /usr/local/bin (may need sudo)
# or
make install-user   # installs to ~/.local/bin

omp-im
```

### Run with PM2 (background)

```bash
make pm2-start
pm2 save
pm2 startup          # follow the printed command to enable auto-start on boot
```

Manage the process:

```bash
pm2 status
pm2 logs omp-im                    # stream logs (keeps running)
pm2 logs omp-im --lines 20 --nostream   # print last 20 lines and exit
pm2 restart omp-im
make pm2-stop
```

All working data is stored under `~/.omp-im`.

## Quick links

- [Configuration](docs/config.md)
- [Chat commands](docs/commands.md)
- [ACP integration](docs/acp.md)

## Architecture

- `internal/core` — `Platform` / `Agent` / `Engine` abstractions, slash-command dispatch, and session persistence.
- `internal/platform/weixin` — iLink long-poll inbound + media outbound; QR-code login.
- `internal/agent` — factory and local ACP launchers for `omp`, `claude`, and `codex`.
- `internal/acp` — generic [Agent Client Protocol](https://agentclientprotocol.com/) client: JSON-RPC over stdio, session lifecycle, and tool-call handling.
- `cmd/omp-im` — entry point.

## Weixin setup

### QR-code login (recommended)

Leave `platforms[0].options.token` empty and run:

```bash
omp-im weixin login
```

Scan the terminal QR code with WeChat. The login state is saved to `~/.omp-im/weixin/default/session.json` and reused on restart.

### Token login

If you already have an iLink bot Bearer token, set `platforms[0].options.token`.

### Logout

```bash
omp-im weixin logout
```

### Access control

Set `allow_from` to a comma-separated list of Weixin user IDs to restrict senders. `"*"` or empty allows everyone.

## Chat commands

- `/agent` / `/agent <name>` — show or switch agent.
- `/proj` / `/proj <name>` — show or switch project (working directory).
- `/p` — show current agent, project, turn status, and token usage.
- `/esc` — cancel the current reply.
- `/new` — start a fresh session.
- `/help`, `/?` — show help.

See [docs/commands.md](docs/commands.md) for details.

## Session persistence across restarts

`omp-im` persists agent session IDs to `~/.omp-im/sessions.json`. On restart, it attempts to resume previous ACP sessions via `session/resume` or `session/load` if the agent supports it. If not, the conversation starts fresh. Explicit `/new` or switching agent/project clears the persisted session ID.

## Development

If the default Go module proxy is slow or unreachable, use a domestic mirror:

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

## Current scope

- Text messages: ✅
- Multi-turn sessions via ACP: ✅
- Images from Weixin to agent: ✅
- Tool execution (auto-approved with built-in `omp`): ✅
- Sending images/files back to Weixin: ✅
- Multi-agent switching: ✅
- Per-project working directories: ✅
- Weixin QR-code login: ✅
- Session persistence across restarts: ✅
