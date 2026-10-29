## 优化

- ZCode 通过 CPA 接入的模型默认启用图片输入，自动生成 `inputFormat.supportsImage: true`，包括没有视觉能力元数据的模型。已有手动配置会保留。
- 新安装或未明确设置开关时，插件功能默认开启；用户明确关闭插件的配置不会被覆盖。
- 更新 README 中的应用截图，展示当前版本的界面和客户端配置页面。

## 使用提示

- 更新后请重新应用 ZCode 配置并重启 ZCode，才能使用默认的图片输入设置。
- 图片输入设置只允许客户端发送图片，实际识图能力仍取决于所选模型和上游服务。

## 修复

- 修复插件默认状态与配置页面默认值不一致的问题。

## 部分历史更新

- v0.3.57 已包含 Pi 模型目录同步、DeepSeek Harness 默认图片输入和 ZCode 思考级别配置修复。

- 关于软件，如果您有任何问题或建议，请通过 [QQ群](https://qm.qq.com/q/3queDaIG) 或 [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) 反馈，我们会尽快处理。
