# omp-im

IM connector for local AI agents. Currently supports Weixin (personal) via the iLink Bot protocol and the `omp` ACP agent; CLAUDE Code / Codex CLI support is planned.

## Run

```bash
cp config.example.json config.json
# 1. Configure at least one agent under agents.
# 2. Configure one or more projects with work_dir.
# 3. For Weixin, leave platforms[0].options.token empty to login via QR code.
go run ./cmd/omp-im -config config.json
```

## Architecture

- `internal/core` — `Platform` / `Agent` / `Engine` abstractions, slash-command dispatch, session registry.
- `internal/platform/weixin` — iLink long-poll inbound + media outbound; QR-code login.
- `internal/agent/omp` — ACP adapter: JSON-RPC over `omp acp` stdio.
- `cmd/omp-im` — entry point.

## Configuration

```json
{
  "agents": [
    {
      "name": "omp",
      "type": "omp",
      "command": "omp",
      "args": ["acp"],
      "auto_approve_tools": true
    }
  ],
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
        "state_dir": "./data/weixin/default",
        "allow_from": "*"
      }
    }
  ]
}
```

- `agents` — one or more agent backends. New sessions use the default agent.
- `projects` — working directories passed to the agent when starting a session.
- `default` — global default agent and project for new sessions.
- `platforms` — IM platforms. Weixin supports both pre-configured token and QR-code login.

## Weixin setup

### QR-code login (recommended)

Leave `platforms[0].options.token` empty. On first start, the terminal prints a QR code URL and saves a PNG to `state_dir/login-qr.png`. Scan the code with WeChat, confirm on your phone, and the login token is persisted to `state_dir/session.json`. Subsequent restarts reuse the saved session automatically.

### Token login

If you already have an iLink bot Bearer token, set `platforms[0].options.token` and the platform skips QR login.

### Access control

Set `allow_from` to a comma-separated list of Weixin user IDs to restrict senders. `"*"` or empty allows everyone.

## Chat commands

Send these commands in any Weixin conversation:

- `/agent` — show current agent and available agents.
- `/agent <name>` — switch the current conversation to a different agent; the existing session is closed and restarted on the next message.
- `/proj` — show current project and available projects.
- `/proj <name>` — switch the current conversation to a different project (changes the agent's working directory).
- `/list` — list active sessions belonging to the current agent, with project and status.

## Agent backend

`omp-im` speaks the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/) to the local `omp` CLI:

- One ACP session is created per conversation (`session/new`).
- Each incoming message is sent via `session/prompt`.
- Assistant text is streamed through `session/update` notifications and delivered back to Weixin.
- Tool permission requests are auto-approved when `auto_approve_tools = true`.
- Images attached to Weixin messages are forwarded as ACP image content blocks.
- Files/images created by the agent during a turn are sent back to Weixin automatically.

## Current scope

- Text messages: ✅
- Multi-turn sessions via ACP: ✅
- Images from Weixin to omp: ✅
- Tool execution (auto-approved): ✅
- Sending images/files back to Weixin: ✅
- Multi-agent switching: ✅
- Per-project working directories: ✅
- Weixin QR-code login: ✅
