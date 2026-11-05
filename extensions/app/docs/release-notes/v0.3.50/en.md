- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Added

- Added manual OAuth credential refresh in credential details and a batch management dialog. Select filtered credentials to refresh, enable, disable, or delete them, with individual operation results.
- Added server-wide refresh for all eligible credentials, independent of the current filter.
- Added a collapsed Request policies section to credential settings for per-credential retries and error handling rules, with an option to restore inherited settings.
- Added an xAI credential setting to switch between the official API and Grok CLI chat-proxy.

## Improved

- Added a problem filter to the credential list and extended search to include notes and model prefixes.
- Unified plugin quota recognition across credential management and quota pages, including quota windows, reset times, plans, and provider-supplied summaries such as balances.
- Added individual and bulk plugin quota refresh, compatibility with older cores that omit plugin IDs, and shared concurrent plugin lookups. Built-in providers retain priority for their existing quota queries.
- Preserved the existing credential list layout, placing new actions in details, settings, and the batch management dialog.

## Fixed

- Fixed plugin credentials being excluded from the dedicated quota page. Gemini and other credentials with plugin-provided quota support can now appear and fetch quotas.
- Included credential indexes in enable/disable requests to disambiguate credentials with matching filenames.
- Extended credential refresh request timeouts to reduce premature failures during bulk refresh.

## Usage Notes

- Refreshing credentials updates authentication credentials; it does not reset quotas. Refreshing all credentials may take several minutes.
- Ordinary Gemini OAuth login does not itself provide quota queries. Availability depends on quota support from the backend and plugins.
