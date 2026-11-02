- 關於軟體，如果您有任何問題或建議，請透過 [QQ群組](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 回饋，我們會盡快處理。

## 修復

- 修復 DeepSeek Harness 的 Cordis 設定識別及更新問題，避免覆寫原生預設模型和自訂設定，並保持對舊版 `settings.yaml` 的相容性。
- 修復外掛更新檢查中的版本比較錯誤，正確識別正式版與預發行版的先後順序，避免因版本較舊或僅建置中繼資料不同而誤報更新。
