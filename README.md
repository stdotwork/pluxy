# pluxy

pluxy (plug + proxy) is a monorepo combining Soul4723's forks of router-for-me's
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) core,
[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) desktop app, and
[Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) web panel.
The panel fork has since been replaced by the web panel in `next/ui`.

| Folder | Component |
| --- | --- |
| `core/` | Proxy core |
| `extensions/app/` | Desktop app |
| `next/` | New core and extension protocol (in progress) |

See [AGENTS.md](AGENTS.md) for how to build and test each folder and how changes are made.

Each folder keeps its upstream MIT `LICENSE`. The notice of the removed old web panel is in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The root license is not decided yet.
