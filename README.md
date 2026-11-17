# pluxy

pluxy (plug + proxy) is a monorepo combining Soul4723's forks of router-for-me's
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) core,
[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) desktop app, and
[Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) web panel.
The core fork has since been replaced by the new core in `next/`, and the panel fork by the web
panel in `next/ui`.

| Folder | Component |
| --- | --- |
| `extensions/app/` | Desktop app |
| `next/` | New core and extension protocol (in progress) |

See [AGENTS.md](AGENTS.md) for how to build and test each folder and how changes are made.

`extensions/app/` keeps its upstream MIT `LICENSE`. The MIT notices of the upstream projects this
repository once held (the old core and old web panel) or still carries (the old desktop app and the
Pi adapter in `next/app-tools/pi-setup/adapter/`), and the files `next/` keeps or adapts from them,
are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The root license is not decided yet.
