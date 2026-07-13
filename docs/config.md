# Configuration

`omp-im` reads JSON configuration from `~/.omp-im/config.json` by default. Use `-config <path>` to override.

## Example

```json
{
  "agents": ["omp", "claude", "codex"],
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
        "token": "",
        "allow_from": "*"
      }
    }
  ],
  "session_store": "~/.omp-im/sessions.json"
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `agents` | `[]string` | Built-in agent names to enable: `omp`, `claude`, and `codex`. Claude and Codex require their matching ACP adapters to be installed. |
| `projects` | `[]ProjectConfig` | Named working directories passed to the agent. |
| `default.agent` | `string` | Default agent for new conversations. |
| `default.project` | `string` | Default project for new conversations. |
| `platforms` | `[]PlatformConfig` | IM platforms. `weixin` and `wecom` are supported. |
| `session_store` | `string` | Optional path to persist agent session IDs. Defaults to `~/.omp-im/sessions.json`. |

### ProjectConfig

```json
{ "name": "default", "work_dir": "/path/to/project" }
```

- `name` — unique project identifier.
- `work_dir` — absolute or relative path used as the agent's working directory. `omp-im` creates this directory on startup if it does not exist.

### Weixin options

| Option | Type | Description |
|--------|------|-------------|
| `token` | `string` | iLink bot Bearer token. If empty, QR-code login is required. |
| `base_url` | `string` | Optional iLink gateway base URL. Defaults to `https://ilinkai.weixin.qq.com`. |
| `cdn_base_url` | `string` | Optional CDN base URL for media. Defaults to `https://novac2c.cdn.weixin.qq.com/c2c`. |
| `allow_from` | `string` | Comma-separated list of allowed Weixin user IDs. `"*"` or empty allows everyone. |
| `account_id` | `string` | Account label used in the default state directory. Defaults to `default`. |
| `state_dir` | `string` | Override the default Weixin state directory. |
| `long_poll_timeout_ms` | `int` | Long-poll timeout in milliseconds. Defaults to `35000`. |
| `proxy` | `string` | Optional HTTP proxy for the iLink gateway. |
| `route_tag` | `string` | Optional route tag passed to the iLink API. |

### WeCom options

| Option | Type | Description |
|--------|------|-------------|
| `bot_id` | `string` | **Required.** WeCom AI bot ID. |
| `secret` | `string` | **Required.** WeCom AI bot long-connection secret. |
| `websocket_url` | `string` | Optional gateway URL. Defaults to `wss://openws.work.weixin.qq.com`. |
| `allow_from` | `string` | Comma-separated list of allowed sender user IDs for direct messages. `"*"` or empty allows everyone. |
| `group_allow_from` | `string` | Comma-separated list of allowed group chat IDs. `"*"` or empty allows all groups. |

Sessions are isolated by chat: each group chat uses its own `session_key` (`wecom:<chatid>`), and each direct message user also gets a separate session.

## QR-code login

If `token` is empty, run:

```bash
omp-im weixin login
```

Scan the terminal QR code with WeChat. The login state is saved to `~/.omp-im/weixin/<account_id>/session.json`.
