- 關於軟體，如果您有任何問題或建議，請透過 [QQ群組](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 回饋，我們會盡快處理。

## 修復

- 修復 Windows 下偵測 VSCode Copilot 安裝狀態及版本時，可能意外啟動 VS Code、還原多個視窗，並在偵測逾時後強制關閉這些處理程序的問題，避免重複偵測導致編輯器反覆啟動和關閉。
- 偵測現在使用對應安裝目錄的命令列入口，並區分穩定版與 Insiders 版；找不到相符的命令列入口時不再嘗試啟動編輯器，版本資訊將顯示為未知。
