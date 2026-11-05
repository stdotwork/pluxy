# UI terms

The desktop app (`extensions/app`) and the web panel (`extensions/panel`) show the same things:
quota cards, limits, resets, auth files, cooldowns and usage. This page lists the words both use for
them, so every provider and every locale says the same thing the same way. When you add or change a
UI string, use these terms. If a term here is wrong, change it here first, then in both UIs.

Status: first version. Terms marked "owner to confirm" are a recommendation that the project owner
has not confirmed yet. The non-English terms other than Chinese still need a native speaker's check.

## English

### Quota windows

A quota is the whole allowance a credential has with a provider. A limit is one window of that
quota. Credits are a money-like balance.

| Concept | Term | Do not use |
|---|---|---|
| Rolling 5-hour window | `5-hour limit` | `5h limit`, `5 hours limit` |
| 24-hour window | `Daily limit` | `24-hour limit` |
| 7-day window | `Weekly limit` | `7-day limit`, `7 days limit`, `1w limit` |
| About 30-day window | `Monthly limit` | `Monthly total quota` |
| Kimi's monthly coding window | `Monthly coding limit` | `Monthly coding quota` |
| Any other window length | `{n}-hour limit`, `{n}-minute limit`, `{n}-day limit`, `{n}-second limit` | `{n} hours limit`, `{n}h limit` |
| A limit for one scope | `{scope} weekly limit`, for example `Code review weekly limit` | `Code review Weekly limit` |
| Claude's Fable window | `Weekly Fable limit` | `Weekly Fable 5 limit` |
| xAI billing balance | `Monthly credits` | `Monthly included quota` |

A window of exactly 5 hours, 24 hours, 7 days or 28 to 31 days always gets the named label above,
whichever provider reports it and however the provider words it.

### Usage and resets

| Concept | Term | Do not use |
|---|---|---|
| Share of a window left | `{n}% remaining` | a bare `{n}%` on a full card |
| Spending or credits used | `Used {used} / {limit}`, `{n}% used` | `Spent {spent} / Limit {limit}` |
| When a window starts again | `Resets {time}`, `Resets in {duration}` | `Refreshes in`, `Renews` |
| A reset time that has passed | `Reset available` | `Refresh available` |
| Reload data from the server | `Refresh` | `Query`, `Load` |
| Spend a manual reset | `Reset quota` | |
| A manual reset you can spend (Codex, Claude) | `manual reset`, `Manual resets` for the count | `reset credit`, `reset grant`, `Banked resets` |
| End of a subscription | `Subscription ends {time}` | `Renewal time`, `Subscription expires` |
| End of a consumable | `Expires {time}` | |
| Short durations | `{n}s`, `{n}m`, `{n}h`, `{n}d {n}h` | `{n} min`, `{n} hr` |

### Credentials and status

| Concept | Term | Do not use |
|---|---|---|
| The page and nav entry for stored login files | `Auth Files` (owner to confirm) | `Authentication Files`, `Credentials` |
| One such file, in body text | `auth file` | `credential file`, `OAuth file` |
| The record the proxy routes requests through | `credential` | |
| The upstream identity | `account` | |
| Credential can take requests / cannot / turned off | `Available` / `Unavailable` / `Disabled` (owner to confirm) | `Active`, `Ready` |
| Temporary local block after errors (noun) | `cooldown` | `cooling` |
| The same, as a state | `cooling down` | `cooling` |
| Clear that block | `Clear cooldown` | |
| Sign-in action | `Sign in` (owner to confirm; not yet applied) | `Log in`, `Login` |

The panel's health labels (`Healthy`, `Warning`, `Disabled`) are a separate scale and stay as they are.

### Provider names

Use one display name per provider, from one shared list in each UI:
`Claude`, `Codex`, `Gemini`, `Antigravity`, `Devin`, `Kimi`, `xAI`, `Muse (Meta)`, `Vertex AI`,
`AI Studio`, `OpenAI Compatible`. Use `Grok` only for xAI's product, CLI or plan names (`Grok Build`,
`SuperGrok`), not for the provider (owner to confirm). The Claude login card says `Claude OAuth`,
not `Anthropic OAuth`.

### Casing

Sentence case for quota and credential strings: `Weekly limit`, `Clear cooldown`. Apply it when a
string is touched for another reason; there is no repo-wide casing pass.

## Other languages

The app has en, zh-CN, zh-TW and ja; its zh-TW text is converted from zh-CN by
`extensions/app/src/i18n/traditional.ts`, so a zh-CN change reaches zh-TW only if the converter maps
it. The panel has en, zh-CN, zh-TW, ko, ru, tr and vi.

| Concept | zh-CN | zh-TW | ja | ko | ru | tr | vi |
|---|---|---|---|---|---|---|---|
| Quota | 额度 | 額度 (owner to confirm; panel zh-TW uses 配額 today) | クォータ | 할당량 | квота | kota | quota |
| Limit | 限额 | 限額 | 制限 | 한도 | лимит | sınır | giới hạn |
| Weekly limit | 周限额 | 週限額 | 週間制限 | 주간 한도 | Недельный лимит | Haftalık Sınır | Giới hạn tuần |
| Monthly limit | 月限额 | 月限額 | 月間制限 | 월간 한도 | Месячный лимит | Aylık Sınır | Giới hạn tháng |
| Resets | 重置 | 重置 or 重設 (owner to decide) | リセット | 초기화 | сброс | sıfırlanır | đặt lại |
| Refresh (reload data) | 刷新 | 重新整理 | 更新 | 새로 고침 | обновить | yenile | làm mới |
| Auth file | 认证文件 | 驗證檔案 | 認証ファイル | 인증 파일 | файл аутентификации | kimlik dosyası | file xác thực |
| Credential | 凭证 | 憑證 | 認証情報 | 자격 증명 | учётные данные | kimlik bilgisi | thông tin xác thực |
| Account | 账号 | 帳號 | アカウント | 계정 | аккаунт | hesap | tài khoản |
| Cooldown | 冷却 | 冷卻 | クールダウン | 쿨다운 | кулдаун | soğuma | thời gian chờ |
| Available | 可用 | 可用 | 利用可能 | 사용 가능 | Доступны | Kullanılabilir | Khả dụng |
| Disabled | 已停用 | 已停用 | 無効 | 비활성화됨 | Отключены | Devre Dışı | Đã tắt |
| Manual resets | 主动重置次数 | 主動重置次數 | 手動リセット回数 | 수동 초기화 횟수 | Ручные сбросы | Elle Sıfırlamalar | Lượt đặt lại thủ công |

zh-CN uses 周 and zh-TW uses 週. Labels for other window lengths are whole phrases per unit in every
locale (for example `{count} 天限额`), never a number glued to an English unit.
