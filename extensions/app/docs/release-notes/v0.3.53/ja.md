- アプリに関するご質問やご提案は、[QQ グループ](https://qm.qq.com/q/3queDaIG) または [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues) までお寄せください。できる限り速やかに対応します。

## 修正

- DeepSeek Harness の Cordis 設定の検出と更新に関する問題を修正しました。既存のネイティブ既定モデルやカスタム設定の上書きを防ぎ、従来の `settings.yaml` との互換性も維持します。
- プラグインの更新確認でバージョン比較が誤る問題を修正しました。安定版とプレリリース版の順序を正しく判定し、古いバージョンやビルドメタデータのみの違いによる誤った更新通知を防ぎます。
