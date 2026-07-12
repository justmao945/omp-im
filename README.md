# omp-im

IM connector for the [omp](https://github.com/justmao945/omp-im) agent. Currently supports Weixin (personal) via the ilink bot HTTP API; WeCom support is planned.

## Run

```bash
cp config.example.json config.json
# 1. Set your ilink Weixin token under platforms[0].options.token.
# 2. Optionally set agent.work_dir to the project directory.
go run ./cmd/omp-im -config config.json
```

## Architecture

- `internal/core` — minimal `Platform` / `Agent` / `Engine` abstractions.
- `internal/platform/weixin` — long-poll inbound + text outbound.
- `internal/agent/omp` — ACP adapter: JSON-RPC over `omp acp` stdio.
- `cmd/omp-im` — entry point.

## Weixin setup

1. Obtain an ilink bot Bearer token for your Weixin account.
2. Set `platforms[0].options.token` in `config.json`.
3. Optional: set `allow_from` to restrict which Weixin users can talk to the bot.
4. Start `omp-im` and send the bot a message from an allowed account to establish a `context_token`.

## Agent backend

`omp-im` speaks the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/) to the local `omp` CLI:

```json
{
  "agent": {
    "command": "omp",
    "args": ["acp"],
    "work_dir": "/path/to/project",
    "auto_approve_tools": true
  }
}
```

- One ACP session is created per Weixin conversation (`session/new`).
- Each incoming message is sent via `session/prompt`.
- Assistant text is streamed through `session/update` notifications and delivered back to Weixin.
- Tool permission requests are auto-approved when `auto_approve_tools = true`.
- Images attached to Weixin messages are forwarded as ACP image content blocks.
  Set `platforms[0].options.cdn_base_url` if your gateway uses a non-default CDN host.
- Files/images created by the agent during a turn are sent back to Weixin automatically.
  The ACP adapter collects paths from tool call results, reads the files, and the Weixin
  platform uploads them to the ilink CDN before sending the media message.

## Current scope

- Text messages: ✅
- Multi-turn sessions via ACP: ✅
- Images from Weixin to omp: ✅
- Tool execution (auto-approved): ✅
- Sending images/files back to Weixin: ✅
