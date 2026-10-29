## 改善

- 透過 CPA 接入 ZCode 的模型預設啟用圖片輸入，自動產生 `inputFormat.supportsImage: true`，包含沒有視覺能力中繼資料的模型。既有手動設定會保留。
- 新安裝或未明確設定開關時，外掛功能預設啟用；使用者明確關閉的外掛設定不會被覆蓋。
- 更新 README 應用程式截圖，展示目前版本的介面與用戶端設定頁面。

## 使用提示

- 更新後請重新套用 ZCode 設定並重新啟動 ZCode，才能使用預設的圖片輸入設定。
- 圖片輸入設定只允許用戶端傳送圖片，實際辨識能力仍取決於所選模型與上游服務。

## 修復

- 修復外掛預設狀態與設定頁面預設值不一致的問題。

## 部分歷史更新

- v0.3.57 已包含 Pi 模型目錄同步、DeepSeek Harness 預設圖片輸入與 ZCode 思考級別設定修復。

- 關於軟體，如果您有任何問題或建議，請透過 [QQ群組](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 回饋，我們會盡快處理。
