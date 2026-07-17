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
      "name": "default",
      "options": {
        "token": "",
        "allow_from": "*"
      }
    }
  ],
  "session_store": "~/.omp-im/sessions.db"
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `agents` | `[]string` | Built-in agent names to enable: `omp`, `claude`, and `codex`. Claude and Codex require their matching ACP adapters to be installed. |
| `projects` | `[]ProjectConfig` | Named working directories passed to the agent. |
| `default.agent` | `string` | Default agent for new conversations. |
| `default.project` | `string` | Default project for new conversations. |
| `platforms` | `[]PlatformConfig` | IM platforms: `weixin` or the test-only `http` platform. |
| `session_store` | `string` | Optional bbolt path for persisted agent session IDs. Defaults to `~/.omp-im/sessions.db`. |
| `display` | `object` | Stream rendering and footer settings. See [DisplayConfig](#displayconfig). Toggle either at runtime with `/display`. |

### ProjectConfig

```json
{ "name": "default", "work_dir": "/path/to/project" }
```

- `name` — unique project identifier.
- `work_dir` — absolute or relative path used as the agent's working directory. `omp-im` creates this directory on startup if it does not exist.

### DisplayConfig

```json
{ "mode": "full", "footer": true }
```

- `mode` — stream rendering: `""` (default, body text only) or `full` (show thinking + tool activity inline). Set at runtime with `/display mode full|simple`.
- `footer` — append a turn-summary footer (⏱️ elapsed · 🧠 context%) to replies. Defaults to `true`; set to `false` to disable. Set at runtime with `/display footer on|off`.

Both settings are global and shared across all platforms. Omit the object entirely for the defaults (simplified mode, footer on).

### PlatformConfig

```json
{ "name": "default", "type": "weixin", "options": {} }
```

- `name` — optional identifier for this platform instance. For Weixin, it is used as the account name and state directory name.
- `type` — platform type: `weixin` or `http`.
- `options` — platform-specific options.

### Weixin options

| Option | Type | Description |
|--------|------|-------------|
| `token` | `string` | iLink bot Bearer token. If empty, QR-code login is required. |
| `base_url` | `string` | Optional iLink gateway base URL. Defaults to `https://ilinkai.weixin.qq.com`. |
| `cdn_base_url` | `string` | Optional CDN base URL for media. Defaults to `https://novac2c.cdn.weixin.qq.com/c2c`. |
| `allow_from` | `string` | Comma-separated list of allowed Weixin user IDs. `"*"` or empty allows everyone. |
| `account_id` | `string` | Legacy account label used in the state directory. Use the top-level `name` field instead when possible. |
| `state_dir` | `string` | Override the default Weixin state directory. |
| `proxy` | `string` | Optional HTTP proxy for the iLink gateway. |
| `route_tag` | `string` | Optional route tag passed to the iLink API. |

## QR-code login

If `token` is empty, run:

```bash
omp-im weixin login
```

Scan the terminal QR code with WeChat. The login state is saved to `~/.omp-im/weixin/<name>/session.json`.

## Multiple Weixin accounts

Add one platform entry per account, each with a unique `name`. The server runs all configured accounts simultaneously, and their state directories are isolated.

```json
{
  "platforms": [
    {
      "type": "weixin",
      "name": "work",
      "options": { "allow_from": "*" }
    },
    {
      "type": "weixin",
      "name": "personal",
      "options": { "allow_from": "*" }
    }
  ]
}
```

Log in to a specific account by name:

```bash
omp-im weixin login work
omp-im weixin login personal
```

If only one Weixin account is configured, the name may be omitted.
