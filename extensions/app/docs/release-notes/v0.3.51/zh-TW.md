- 關於軟體，如果您有任何問題或建議，請透過 [QQ群組](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 回饋，我們會盡快處理。

## 優化

- 適配新版 Claude Desktop 的閘道設定，明確使用靜態憑證與指定模型清單，避免舊的驗證方式、自動設定或工作階段服務設定覆寫 CPA 連線。
- 更新 Claude Desktop 設定時正確切換至 CPA 本機設定，並支援在還原備份時恢復原有的設定選擇。
- 補充 Claude Desktop 對 Fable、Mythos 模型系列的識別。

## 修復

- 修復 xAI、Meta、Vertex 和 Interactions API Key 通道的 Claude Desktop 模型別名處理不完整的問題，避免將 API Key 模型錯誤對應至 OAuth 通道，導致模型無法使用。
- 修復重複儲存設定時，仍在使用的模型別名被清除的問題。同名模型存在於多個提供者時，保留有效的原有路由，避免意外切換提供者。
- 修復被停用或排除的 API Key 模型可能透過殘留 OAuth 對應被錯誤接受的問題。
- 完善 Claude Desktop 設定狀態檢查，識別驗證方式、設定來源衝突及模型清單不完整的情況。
- 完善模型對應儲存時的憑證保護與失敗回復處理，保留提供者的金鑰與連線設定。

## 發布流程

- 恢復 Miaomiao 預覽版自動建置與發布：由 Roomen 提交、目標為 `miaomiao` 的非草稿 PR，在六個平台建置通過後自動合併，並建置合併後的提交、更新滾動預覽版。

## 使用說明

- 升級後，請在 CPA 中重新儲存 Claude Desktop 設定，並完全結束、重新開啟 Claude Desktop。
- 模型別名需要對應可用的原始模型。若原始模型已被停用或排除，請調整對應連線設定，或選擇其他可用模型。
