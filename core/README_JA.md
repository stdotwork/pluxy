# CLI Proxy API

[English](README.md) | [中文](README_CN.md) | 日本語

CLIProxyAPI は、CLI向けのOpenAI/Gemini/Claude/Codex/Grok互換APIインターフェースを提供するプロキシサーバーです。

ローカル環境や複数のCLIアカウントを通じて、OpenAI（Responses含む）、Gemini（Interactions含む）、またはClaude互換のクライアントやSDKから、以下のプロバイダーにアクセスできます。

<table>
<tbody>
    <tr>
        <th align="center" width="100">プロバイダー</th>
        <th align="center">説明</th>
    </tr>
    <tr>
        <td align="center"><a href="https://www.kimi.com/code/"><img src="./assets/logo/kimi.svg" alt="Kimi" width="28" height="28" /></a></td>
        <td>Kimiシリーズモデル（Kimi K3.1 Pro、K3.1 Pro Highspeed、K3、K2.8 Previewなど）。<a href="https://platform.kimi.ai/docs/guide/kimi-k3-quickstart">Kimi K3</a>は、Moonshot AIの前世代のフラッグシップモデルであり、世界初のオープンな3兆パラメータ級モデルです。2.8兆のパラメータ、ネイティブな視覚機能、100万トークンのコンテキストウィンドウを備え、長期間にわたるコーディング、知識作業、推論向けに構築されています。CLIProxyAPIはOAuthまたは互換APIインターフェース経由でKimiをサポートします。<strong>Kimi Code プラン</strong>（<a href="https://www.kimi.com/code">中文站</a> | <a href="https://www.kimi.ai/code">Global</a>）を試すか、Kimi Open Platform（<a href="https://platform.kimi.com">中文站</a> | <a href="https://platform.kimi.ai">Global</a>）で<strong>APIキー</strong>を取得してください。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://developers.openai.com/api/docs/models"><img src="./assets/logo/openai.svg" alt="OpenAI" width="28" height="28" /></a></td>
        <td>OpenAI GPT-6シリーズモデル（GPT-6 Astra、GPT-6.1 Sol、GPT-6.1 Terra、GPT-6 Luna）。Codex OAuth経由でも利用できます。Astraは複雑な推論とコーディング、Solは性能とコストのバランス、Terraは視覚的推論とコンピューター操作、Lunaは大量の明確なタスクに適しています。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://www.anthropic.com/claude"><img src="./assets/logo/claude.svg" alt="Anthropic" width="28" height="28" /></a></td>
        <td>Anthropic Claudeシリーズモデル（Claude Fable 5.1、Claude Opus 5.5、Claude Sonnet 5.5、Claude Haiku 5.5）。Fable 5.1は長期のコーディングや知識作業向けで、Opus 5.5は低いコストで高度なエージェント型コーディングに対応します。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://antigravity.google/"><img src="./assets/logo/antigravity.svg" alt="Antigravity" width="28" height="28" /></a></td>
        <td>Google GeminiシリーズにはGemini 4 ArgonやGemini 3.8 Flashがあります。CLIProxyAPIはGemini API、AI Studio、Vertex AI、Gemini CLI、Antigravityのアカウントに対応し、利用できるモデルは経路によって異なります。Gemini 3.8 Flashは、長期のソフトウェア開発やエージェントのワークフロー向けのGoogleの最新Flashモデルです。Antigravityの<a href="https://antigravity.google/docs/models/">公式モデル提供状況</a>では、Claude Sonnet 5.5（thinking）とClaude Opus 5.5（thinking）はGoogle AI Pro（試用版を除く）、Google AI Ultra、Enterpriseで利用可能とされています。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://docs.x.ai/developers/grok-4-7"><img src="./assets/logo/xai.svg" alt="xAI" width="28" height="28" /></a></td>
        <td>xAI Grokシリーズモデル（Grok 4.7、Grok 4.7 Build Fastなど）。Grok 4.7は、コーディング、エージェントタスク、知識作業向けのSpaceXAIの最新モデルです。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://dev.meta.ai/docs/overview"><img src="./assets/logo/meta.svg" alt="Meta" width="28" height="28" /></a></td>
        <td>Meta Museシリーズモデル（Muse Wonder Full、Muse Wonder、Muse Spark 1.3など）。CLIProxyAPIはMetaログインによるMuse CodeアカウントとMeta Model APIキーに対応し、Muse Spark 1.3をコーディングやエージェントのワークフローに利用できます。MetaはMuse WonderをMeta Model APIとMuse Code向けに、Muse Wonder FullをMuse Codeのサブスクリプション利用者向けに発表しました。</td>
    </tr>
    <tr>
        <td align="center"><a href="https://devin.ai/cli">Devin</a></td>
        <td>Devinのモデル（SWE-2、GPT-6 Astra、Claude Fable 5.1など）。<code>--devin-login</code>でDevinアカウントに接続し、そのアカウントで利用可能なモデルにリクエストを送れます。</td>
    </tr>
</tbody>
</table>

## 概要

- CLIモデル向けのOpenAI/Gemini/Claude/Grok互換APIエンドポイント
- OAuthログインによるOpenAI Codexサポート（GPTモデル）
- OAuthログインによるClaude Codeサポート
- OAuthログインによるGrok Buildサポート
- ストリーミング、非ストリーミング、および対応環境でのWebSocketレスポンス
- 関数呼び出し/ツールのサポート
- マルチモーダル入力サポート（テキストと画像）
- ラウンドロビン負荷分散による複数アカウント対応（Gemini、OpenAI、Claude、Grok）
- シンプルなCLI認証フロー（Gemini、OpenAI、Claude、Grok）
- Generative Language APIキーのサポート
- AI Studioビルドのマルチアカウント負荷分散
- Claude Codeのマルチアカウント負荷分散
- OpenAI Codexのマルチアカウント負荷分散
- Grok Buildのマルチアカウント負荷分散
- 設定によるOpenAI互換アップストリームプロバイダー（例：OpenRouter）
- プロキシ埋め込み用の再利用可能なGo SDK（`docs/sdk-usage.md`を参照）

## はじめに

CLIProxyAPIガイド：[https://help.router-for.me/](https://help.router-for.me/)

## 管理API

[MANAGEMENT_API.md](https://help.router-for.me/management/api)を参照

## SDKドキュメント

- 使い方：[docs/sdk-usage.md](docs/sdk-usage.md)
- 上級（エグゼキューターとトランスレーター）：[docs/sdk-advanced.md](docs/sdk-advanced.md)
- アクセス：[docs/sdk-access.md](docs/sdk-access.md)
- ウォッチャー：[docs/sdk-watcher.md](docs/sdk-watcher.md)
- カスタムプロバイダーの例：`examples/custom-provider`

## コントリビューション

リポジトリの情報は [pluxy リポジトリのルート](../README.md)を参照してください。

## ライセンス

本プロジェクトはMITライセンスの下でライセンスされています - 詳細は[LICENSE](LICENSE)ファイルを参照してください。
