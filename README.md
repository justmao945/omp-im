# omp-im

IM connector for local AI agents. Currently supports Weixin (personal) via the iLink Bot protocol and the built-in `omp` ACP agent; CLAUDE Code / Codex CLI support is planned.

## Run

```bash
# Config is loaded from ~/.omp-im/config.json by default.
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

All working data is stored under `~/.omp-im`.

## Architecture

- `internal/core` — `Platform` / `Agent` / `Engine` abstractions, slash-command dispatch.
- `internal/platform/weixin` — iLink long-poll inbound + media outbound; QR-code login.
- `internal/agent` — factory for built-in agents (`omp`, future `claude` / `codex`).
- `internal/agent/omp` — ACP adapter: JSON-RPC over `omp acp` stdio.
- `cmd/omp-im` — entry point.

## Configuration

```json
{
  "agents": ["omp"],
  "projects": [
    { "name": "default", "work_dir": "/path/to/project" }
  ],
  "default": {
    "agent": "omp",
    "project": "default"
  },
  "platforms": [
    {
      "type": "weixin",
      "options": {
        "allow_from": "*"
      }
    }
  ]
}
```

- `agents` — list of built-in agent names to enable. Currently only `omp` is supported.
- `projects` — working directories passed to the agent when starting a session.
- `default` — global default agent and project for new sessions.
- `platforms` — IM platforms. Weixin supports both pre-configured token and QR-code login.

## Weixin setup

### QR-code login (recommended)

Leave `platforms[0].options.token` empty and run the login subcommand:

```bash
omp-im weixin login
```

iLink returns a login URL; `omp-im` renders it as an ASCII QR code in the terminal and also saves it as `~/.omp-im/weixin/default/login-qr.png`. Scan the terminal QR code with WeChat, confirm on your phone, and the login token is persisted to `~/.omp-im/weixin/default/session.json`. After that, start the server normally:

```bash
omp-im
```

Subsequent restarts reuse the saved session automatically.

### Token login

If you already have an iLink bot Bearer token, set `platforms[0].options.token` and the platform skips QR login.

### Logout

To remove the saved Weixin session and force re-login:

```bash
omp-im weixin logout
```

### Access control

Set `allow_from` to a comma-separated list of Weixin user IDs to restrict senders. `"*"` or empty allows everyone.

## Chat commands

Send these commands in any Weixin conversation:

- `/agent` — show current agent and available agents.
- `/agent <name>` — switch the current conversation to a different agent; the existing session is closed and restarted on the next message.
- `/proj` — show current project and available projects.
- `/proj <name>` — switch the current conversation to a different project (changes the agent's working directory).
- `/list` — list active sessions of the current agent, read from the agent itself, with project and status.
- `/help`, `/?` — show this help.

## Agent backend

`omp-im` speaks the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/) to the local `omp` CLI:

- One ACP session is created per conversation (`session/new`).
- Each incoming message is sent via `session/prompt`.
- Assistant text is streamed through `session/update` notifications and delivered back to Weixin.
- Tool permission requests are auto-approved when the built-in `omp` agent is used.
- Images attached to Weixin messages are forwarded as ACP image content blocks.
- Session state read from agent: ✅

## Development

This project depends on `github.com/skip2/go-qrcode` for terminal QR-code output. If the default Go module proxy is slow or unreachable, use a domestic mirror:

```bash
GOPROXY=https://goproxy.cn go mod download
GOPROXY=https://goproxy.cn go build ./...
```

Or set it globally:

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

## Current scope

- Text messages: ✅
- Multi-turn sessions via ACP: ✅
- Images from Weixin to omp: ✅
- Tool execution (auto-approved): ✅
- Sending images/files back to Weixin: ✅
- Multi-agent switching: ✅
- Per-project working directories: ✅
- Weixin QR-code login: ✅
- Session state read from agent: ✅
