- 关于软件，如果您有任何问题或建议，请通过 [QQ群](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 反馈，我们会尽快处理。

## 修复

- 修复 DeepSeek Harness 的 Cordis 配置识别及更新问题，避免覆盖原生默认模型和自定义配置，并保持对旧版 `settings.yaml` 的兼容。
- 修复插件更新检查中的版本比较错误，正确识别正式版与预发布版的先后顺序，避免因版本较旧或仅构建元数据不同而误报更新。
