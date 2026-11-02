- 关于软件，如果您有任何问题或建议，请通过 [QQ群](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 反馈，我们会尽快处理。

## 优化

- 适配新版 Claude Desktop 的网关配置，明确使用静态凭证和指定模型列表，避免旧的认证方式、自动配置或会话服务设置覆盖 CPA 连接。
- 更新 Claude Desktop 配置时正确切换到 CPA 本地配置，并支持在恢复备份时还原原有配置选择。
- 补充 Claude Desktop 对 Fable、Mythos 模型系列的识别。

## 修复

- 修复 xAI、Meta、Vertex 和 Interactions API Key 通道的 Claude Desktop 模型别名处理不完整的问题，避免将 API Key 模型错误映射到 OAuth 通道，导致模型不可用。
- 修复重复保存配置时，仍在使用的模型别名被清理的问题。同名模型存在于多个提供方时，保留有效的原有路由，避免意外切换提供方。
- 修复被禁用或排除的 API Key 模型可能通过残留 OAuth 映射被错误接受的问题。
- 完善 Claude Desktop 配置状态检查，识别认证方式、配置来源冲突及模型列表不完整的情况。
- 完善模型映射保存时的凭证保护与失败回滚处理，保留提供方的密钥和连接设置。

## 发布流程

- 恢复 Miaomiao 预览版自动构建与发布：由 Roomen 提交、目标为 `miaomiao` 的非草稿 PR，在六个平台构建通过后自动合并，并构建合并后的提交、更新滚动预览版。

## 使用说明

- 升级后，请在 CPA 中重新保存 Claude Desktop 配置，并完全退出、重新打开 Claude Desktop。
- 模型别名需要对应可用的原始模型。若原始模型已被停用或排除，请调整对应接入配置，或选择其他可用模型。

