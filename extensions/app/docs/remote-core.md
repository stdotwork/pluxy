# Remote core

Remote mode attaches this GUI to an **already-running CLIProxyAPI v8** server.
It does not install, launch, stop, restart, or update that server.

## Connect

1. On the server, enable remote management (`remote-management.allow-remote`)
   and set a management key. Use the equivalent management settings for your
   core version, and ensure the management port is reachable.
2. Stop the GUI-managed local core before switching targets.
3. Open **Core connection** from the gear button on Home's Runtime Control
   card (the same panel is under **Advanced Features → App preferences**),
   choose **Remote core**, and enter the management URL (for example
   `https://core.example:8317`) and its plaintext management key. The URL is an
   origin only: no path, query, credentials, or fragment.
4. **Test connection** authenticates against `/v8/management/config`, checks
   that the configuration is v8, and reports `X-CPA-VERSION` when supplied.
   Testing does not save. **Save & connect** stores the connection in the GUI's
   existing `config.toml` and reloads the window. Closing the dialog or pressing
   Escape discards unsaved edits.

HTTPS validates the server certificate. Redirects are not followed. HTTP is
allowed for trusted networks (for example a private VPN), but sends the
management key and traffic without transport encryption. Do not expose the
management API over an untrusted network. The saved key is plaintext, like
other GUI configuration secrets.

## Available remotely

Remote mode keeps the same pages, navigation and cards as Local; only the core
they talk to changes. Values come from the remote core, or show "—" with a
reason; this computer's local data is never shown as the remote core's.

- **Home:** credential, provider-key and model counts come from the remote core.
  Model Health checks the remote core's models through its client API
  (management origin + `/v1`) with the first client key configured on the remote
  core; the management key is never used as a client key. Runtime Control shows
  the remote host, connection status, core version and port. Connect to CPA shows
  the remote client base URL and the remote core's first client key.
- **Advanced Features:** every category shows the remote core's current settings
  (read from its `config.yaml` through the management API), read-only.
- **Management-API pages:** credentials, OAuth (with manual callback submission
  where needed), Vertex import, API providers with model discovery, plugin
  management, and plugin pages served by the remote core.

## Not available remotely yet

These stay visible in place but are disabled, with a short reason:

- Start, Stop and Restart, and Start Core on Launch: the core is managed on the
  remote host.
- Agent Configuration, Beginner Mode and Codex tools: machine and agent
  configuration stays local for now.
- Version Management: it installs a local core binary.
- Usage, and the Home success rate: reading usage from a remote core would consume
  its usage queue, so no usage is collected in Remote mode.
- Local folder buttons (credentials, logs) and local plugin file installation.
- Editing any core setting: remote configuration is shown read-only.

Silent Launch is hidden and ignored in Remote mode: it keeps a locally hosted core
in the tray, and there is none.

Select **Local core** and choose **Use local core** to return to the existing
local core configuration. Start Core on Launch and Silent Launch keep their
saved values for Local.
Change the active connection through the panel; edits to connection fields in
`config.toml` are only picked up on the next application launch.
