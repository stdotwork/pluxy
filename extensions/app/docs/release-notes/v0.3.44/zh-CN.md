- 关于软件，如果您有任何问题或建议，请通过 [QQ群](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 反馈，我们会尽快处理。

## 修复

- 修复 Windows 下检测 VSCode Copilot 安装状态和版本时，可能意外启动 VS Code、恢复多个窗口，并在检测超时后强制关闭这些进程的问题，避免重复检测导致编辑器反复启动和关闭。
- 检测现在使用对应安装目录的命令行入口，并区分稳定版与 Insiders 版；找不到匹配的命令行入口时不再尝试启动编辑器，版本信息将显示为未知。
