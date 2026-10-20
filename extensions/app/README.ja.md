<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh-CN.md">简体中文</a> |
  <strong>日本語</strong>
</p>

<p align="center">
  <img src="src/assets/logo.jpg" width="112" alt="EasyCLIProxyAPI Logo">
</p>

<h1 align="center">EasyCLIProxyAPI</h1>

<p align="center">
  CLIProxyAPI のポータブルデスクトップコンソール。<br>
  私たちの目標は、トークンを free（無料ではなく、自由という意味）にすることです。
</p>

## 概要

EasyCLIProxyAPI は、[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
をベースにしたグラフィカルなデスクトップ管理ツールです。コアのライフサイクル管理、OAuth 認証、
API プロバイダー統合、プロトコル変換、認証情報管理、クォータ確認、使用履歴、モデルエイリアス、
エージェントクライアント設定を一つの画面にまとめます。

本アプリケーションは Tauri、React、Rust で構築されています。対応する CLIProxyAPI コアの
アーカイブを同梱できるため、初回セットアップやオフラインインストールも簡単です。

## 機能紹介

### ホーム画面とローカル API エンドポイント

![ホーム画面とローカル API エンドポイント](docs/screenshots/jp/1.png)

ホーム画面では、ローカルプロキシの稼働状況とすぐに使えるローカル API エンドポイントを確認できます。

- CLIProxyAPI コアの起動、停止、再起動、状態更新。
- インストール状態、実行状態、プロセス ID、コアバージョン、アプリバージョンの確認。
- OpenAI、Claude、Gemini 互換の API エンドポイントをそのままコピー。
- ローカル接続状態の確認。

コアのインストール、バージョン比較、オフラインインストールは **バージョン管理** ページで行えます。GitHub 公式、GitCode、GitHub ミラープロキシを切り替えたり、カスタム HTTPS ミラープレフィックスを追加したりできます。更新に失敗した場合は自動的に別の利用可能なソースへ切り替わります。

### OAuth アカウント認証

![OAuth アカウント認証](docs/screenshots/jp/2.png)

OAuth 画面では、対応プロバイダーのブラウザー認証をまとめて管理できます。

- Codex OAuth
- Claude OAuth
- Antigravity OAuth
- Kimi OAuth
- xAI OAuth

EasyCLIProxyAPI はブラウザーで認証ページを開きます。自動リダイレクトが利用できない場合は、
コールバックを手動で完了することもできます。

### API 接続とプロバイダー統合

![API 接続とプロバイダー統合](docs/screenshots/jp/3.png)

API 接続画面では、プロトコルまたはプロバイダーごとに上流 API の認証情報と接続先を管理できます。

- Codex
- OpenAI 互換プロバイダー
- DeepSeek
- Claude
- Gemini

複数の接続設定を追加し、既存設定の検索、プロバイダー状態の更新、ヘルスチェックを行えます。すべての接続は、
統一されたローカル CLIProxyAPI エンドポイントから利用できます。リクエストとレスポンスは、
OpenAI、Claude、Gemini、およびその他の互換形式の間で変換できます。

### プラグイン管理

**プラグイン**画面では、コアプラグインの有効化・無効化、削除、型に応じた設定編集ができます。
ストアからのインストール・更新・バージョン指定、追加ストアと環境変数を使う認証の設定、
有効なプラグインが提供するページの表示と OAuth アカウント認証に対応します。有効なプラグインのページは、サイドバーの「バージョン管理」の下にある「プラグイン」のサブメニューとして表示され、スペースに余裕があれば自動で展開され、手動で折りたたむこともできます。プラグインページはコンテンツ領域全体を占めるネイティブ WebView で開き、右下に戻る・再読み込み・ブラウザーで開くの小さなボタンを備えます（グリップのドラッグで移動、ダブルクリックでリセット）。新しいウィンドウのリンクはシステムブラウザーで開きます。サードパーティ製のインストールには
ソースの確認と確認文字列の入力が必要です。非対応コアには案内を表示し、再起動が必要な操作後は
再起動ボタンを表示します。
**ローカルプラグイン**の更新ボタンはインストール済みプラグインの更新を確認し、カードに更新アクションを表示します。
公開 GitHub リリースは設定済みの GitHub トークン、またはアプリの `GITHUB_TOKEN` 環境変数を使って直接確認します。認証が必要な項目はコアの認証処理を維持します。
**プラグインストア**の更新は一覧の再取得のみを行います。バージョンを指定しない場合、インストール時に最新リリースを確認します。

### 使用履歴と Token 統計

![使用履歴と Token 統計](docs/screenshots/jp/4.png)

使用履歴画面では、ローカルで発生したリクエストと Token 消費を確認できます。

- リクエスト総数、Token 総量、成功率、TPS、キャッシュヒット率、推定コストを確認。
- 時間、モデル、プロバイダー、ソース、キー、結果でデータを絞り込み。
- リクエストと Token の推移、入力・出力・推論・キャッシュの使用量を確認。
- リクエスト詳細、分析画面、価格統計を閲覧。
- CPA のリアルタイム使用量購読と永続ローカル inbox で収集し、購読できない場合は HTTP に自動フォールバック。
- 起動時に旧使用履歴データベースを一度だけ移行し、移行前のバックアップを `usage-records/backups` に保存。

### エージェントクライアント設定

![エージェントクライアント設定](docs/screenshots/jp/5.png)

エージェント画面は、インストール済みのデスクトップクライアントと CLI クライアントを検出し、
ローカルプロキシへの接続を支援します。対応クライアントは次のとおりです。

- Claude Code
- Claude Desktop
- Codex
- OpenCode
- OpenClaw
- Hermes Agent
- Pi（CLIProxyAPI provider 拡張機能）
- ZCode
- WorkBuddy / WorkBuddy AI
- Antigravity CLI
- Kimi Code
- Grok Build
- Oh My Pi

対応クライアントでは、利用可能なモデルカタログの同期、デフォルトモデルの選択、管理設定を適用する前の
元設定のバックアップ、以前の設定への復元、利用可能なデスクトップまたは CLI エントリーポイントの起動ができます。

Antigravity CLI は CPA の Gemini 互換 API に接続します。CLI は CPA から起動すると、そのプロセスに接続先と API キーが渡されます。`agy` を直接実行する場合は環境変数を自分で設定してください。

Codex の **セッション管理 → コンテキスト編集（実験的）** は、セッションを選択した後にそのメッセージを読み込みます。
一覧とページ切り替えでは SQLite、セッション索引、ディレクトリのメタデータだけを参照し、rollout を開いたりメッセージを先読みしたりしません。
未登録のセッションは標準の rollout ファイル名から検出し、不足する作業ディレクトリや provider は編集画面で読み込みます。
メタデータは初期状態で折りたたみ、メッセージ分類フィルターは維持します。保存前に対象セッションを閉じてください。保存は実際のローカル履歴を変更し、バックアップを作成します。
会話 ID を含まない名前に変更され、データベースにもパスがないファイルは、本文を走査して検出しません。

## その他の機能

- コア設定、API Key、リモート管理用認証情報、ルーティング戦略の管理。
- クライアント向けモデルエイリアスを作成し、プロバイダーモデルや推論レベルへマッピング。
- 認証ファイルのアップロード、ダウンロード、確認、管理。
- プロバイダーのクォータとアカウント利用状態を確認。
- macOS メニューバーまたは Windows システムトレイからバックグラウンド動作を継続。

## カスタムテーマ

テーマ 1 つにつき 1 つの `.json` ファイルを、アプリデータディレクトリの `themes` フォルダに置きます。Windows と Linux では実行ファイルと同じディレクトリ、macOS アプリでは `~/Library/Application Support/com.cpa.gui/themes` です（`.app` 以外から実行した場合は実行ファイルと同じディレクトリ）。テーマメニュー下部の **テーマフォルダを開く** でフォルダを作成して開き、**再読み込み** で再起動せずに変更を反映できます。カスタムテーマはファイル名順に組み込みテーマの後へ並びます。読み込むのは最大 64 ファイル、1 ファイル 64 KB までです。無効なファイルはスキップされ、そのファイル名と上限を超えたファイル数がテーマメニューに表示されます。

```json
{
  "id": "harbor-dark",
  "name": { "en": "Harbor", "zh-CN": "港湾", "ja": "ハーバー" },
  "description": "Deep teal surfaces with a sea-glass accent",
  "appearance": "dark",
  "windowBackground": "#0E1A1F",
  "windowMaterial": "translucent",
  "tokens": {
    "workspaceBackground": "#0E1A1F",
    "surface": "#14242B",
    "textPrimary": "#E6F1F2",
    "border": "#2A434C",
    "accent": "#6FD3C1",
    "accentContrast": "#0E1A1F",
    "sidebarBackground": "#0B151A",
    "windowMaterialWorkspaceOpacity": "72%",
    "windowMaterialSidebarOpacity": "40%"
  }
}
```

- `id`（必須）：英小文字・数字・`-` で、先頭は英字。組み込みテーマ（`light`, `dark`, `warm-sand-light`, `graphite-dark`, `midnight-dark`, `sakura-light`）や他のカスタムテーマと重複できません。
- `name`（必須）と `description`：文字列、または `en`・`zh-CN`・`zh-TW`・`ja` をキーとするオブジェクト。
- `appearance`（必須）：`light` または `dark`。テーマが表示されるグループを決めます。
- `windowBackground`（必須）：`#RRGGBB`。ページ描画前にネイティブウィンドウに表示される色です。
- `tokens`（必須、空オブジェクト可）：色 token は `#RRGGBB` または `#RRGGBBAA`。省略した色は既定のライトまたはダークの値になります。以下の不透明度 token はパーセントで指定します。未知または削除済みの token キーは無視されます。
- `backdrop`：任意のグラデーション背景。構造は組み込みのミッドナイトブルー、桜ピンクと同じです。
- `windowMaterial`：`solid`（既定）または `translucent`。半透明テーマではサイドバーとワークスペースの背後にネイティブのウィンドウマテリアルが表示されます。macOS 26 以降は Liquid Glass、それ以前の macOS は vibrancy、Windows 11 は Mica、Windows 10 1809 以降は Acrylic です。Linux と古い Windows ではマテリアルがなく、不透明テーマとまったく同じ表示になるため、`windowBackground` とトークンは省略せずに指定してください。マテリアル表示中は `backdrop` は使われません。
- `tokens` の任意の不透明度キー：`windowMaterialWorkspaceOpacity`（ワークスペース、既定 `"72%"`）、`windowMaterialSidebarOpacity`（サイドバー、既定 `"40%"`）。値は `"0%"`（完全に透明）から `"100%"`（不透明）のパーセント文字列で、`"37.5%"` のような小数も使えます。対応する OS で `windowMaterial` が `translucent` の場合のみ有効です。ルート、body、シェルはネイティブマテリアルを表示するため透明になります。不正な値を含むファイルはスキップされます。マテリアルはテーマの設定であり、独立した設定項目はありません。組み込みテーマは不透明のままです。
- `$schema` は記述可能で無視されます。それ以外のフィールドがあるとファイルは無効になります。

利用できる token キー：`workspaceBackground`, `surface`, `softSurface`, `hoverBackground`, `textPrimary`, `textSecondary`, `textTertiary`, `textMuted`, `border`, `borderSubtle`, `borderStrong`, `borderHover`, `panelShadow`, `accent`, `accentStrong`, `accentSoft`, `accentContrast`, `accentShadow`, `primaryButtonBackground`, `primaryButtonHoverBackground`, `primaryButtonText`, `primaryButtonBorder`, `primaryButtonHoverBorder`, `controlActiveBackground`, `controlActiveThumb`, `focusRing`, `sidebarBackground`, `sidebarText`, `sidebarTextStrong`, `sidebarTextMuted`, `sidebarBorder`, `sidebarHover`, `sidebarSelected`, `sidebarSelectedBorder`, `sidebarSelectedShadow`, `popoverBackground`, `overlaySoftSurface`, `overlayHoverBackground`, `scrim`, `controlThumb`, `windowMaterialWorkspaceOpacity`, `windowMaterialSidebarOpacity`。

## クイックスタート

1. [GitHub Releases](https://github.com/router-for-me/EasyCLIProxyAPI/releases/latest)
   から、お使いの OS に対応するパッケージをダウンロードします。
2. Windows または Linux のアーカイブを展開します。macOS では DMG を開きます。
3. EasyCLIProxyAPI を起動します。
4. **バージョン管理** 画面を開き、同梱版または最新版の CLIProxyAPI コアをインストールします。
5. **ホーム** に戻ってコアを起動し、必要なローカル API エンドポイントをコピーするか、OAuth/API プロバイダーを設定します。

## アップグレード

すべての Windows リリースで、完全版 ZIP と旧クライアント互換の `update` ZIP を同時に公開します。まだ移行できていない旧クライアントでもアプリ内更新を利用でき、新しいクライアントでは内蔵 core も更新できる完全版パッケージを使用します。

現在の Windows、Linux、macOS リリースはアプリ内自動更新に対応しています。Linux では実行時データを保持したままポータブルアプリのファイルを置き換え、macOS では署名済みアプリバンドル全体を置き換えます。新しいバージョンの起動確認に失敗した場合は自動的にロールバックします。インストール先は現在のユーザーが書き込める必要があります。

既存の Linux および macOS インストールでは、クロスプラットフォーム自動更新マーカーを含むリリースへ一度手動で更新する必要があります。そのリリースを起動した後は、以降の更新をアプリ内で実行できます。

v0.2.5 以前を使用している場合は、一度だけ手動で移行してください。EasyCLIProxyAPI を終了し、使用中のアーキテクチャに対応する最新版の完全版 Windows ZIP をダウンロードして、その最上位ディレクトリの内容を既存のインストール先へコピーし、同名ファイルを上書きします。既存のディレクトリを先に削除しないでください。`config.toml`、`oauth`、`cpa-core/config.yaml` などのユーザーデータは保持されます。新しいバージョンを起動した後は、以降のリリースでアプリ内自動更新を利用できます。

## 対応プラットフォーム

GitHub Actions では、次のリリースパッケージをビルドします。

| OS | アーキテクチャ | 形式 |
| --- | --- | --- |
| Windows | amd64、aarch64 | ZIP |
| macOS | amd64、aarch64 | DMG |
| Linux | amd64、aarch64 | TAR.GZ |

## 関連プロジェクト

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — 本アプリケーションが管理するプロキシコアです。
