//! The release feed: which app and core versions a feed offers on each channel, the last good
//! copy of it, and the feed's address. The feed's format is in `../README.md` ("Release feed").
//!
//! The window gets only what this module derives from a feed: versions, dates and whether a
//! release has notes. Release notes are rendered for their own webview (`notes.rs`); a notes
//! address is opened only by native code. There is no built-in feed address: until the person
//! sets one (a change that needs a native confirmation), the app checks nothing.
//! Nothing in this module needs Tauri.

use crate::managed::write_private;
use semver::Version;
use serde::{Deserialize, Serialize};
use std::cmp::Ordering;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use url::Url;

/// The feed format this app reads.
pub const FORMAT: u64 = 1;
/// The most of a feed the app reads.
pub const MAX_FEED: u64 = 1 << 20;
pub const MAX_RELEASES: usize = 256;
/// The most Markdown one release's notes may hold.
pub const MAX_NOTES: usize = 64 << 10;
pub const MAX_URL: usize = 2048;
const MAX_VERSION: usize = 64;
/// The most of release.json the app reads; it holds an address and a channel.
const MAX_SETTINGS: u64 = 64 << 10;
/// The most of the kept feed the app reads: a feed it accepted, written again.
const MAX_CACHE: u64 = 2 * MAX_FEED;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Product {
    App,
    Core,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Channel {
    /// The newest release on the stable channel.
    #[default]
    Stable,
    /// The newest release on either channel ("Preview (latest)").
    Preview,
}

/// One release, after validation. Fields a later format adds are not kept.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Release {
    pub product: Product,
    pub version: String,
    pub channel: Channel,
    pub published: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub notes: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes_url: Option<String>,
}

#[derive(Deserialize)]
struct RawFeed {
    format: u64,
    releases: Vec<Release>,
}

/// A feed that passed every check: a feed with any bad release is refused as a whole.
#[derive(Clone, Debug, PartialEq)]
pub struct Feed {
    releases: Vec<(Version, Release)>,
}

/// An https address with a host and no user name or password, at most MAX_URL bytes both as given
/// and as the URL parser writes it, as a feed or notes address must be. Returns the latter.
pub fn https_url(s: &str) -> Result<String, String> {
    if s.len() > MAX_URL {
        return Err(format!("the address is longer than {MAX_URL} characters"));
    }
    let u = Url::parse(s.trim()).map_err(|e| format!("not an address: {e}"))?;
    if u.scheme() != "https" {
        return Err("the address must start with https://".into());
    }
    if u.host_str().is_none_or(str::is_empty) {
        return Err("the address has no host".into());
    }
    if !u.username().is_empty() || u.password().is_some() {
        return Err("the address may not hold a user name or password".into());
    }
    // Normalising can percent-encode characters: what is kept must also fit.
    let u = u.to_string();
    if u.len() > MAX_URL {
        return Err(format!("the address is longer than {MAX_URL} characters"));
    }
    Ok(u)
}

/// RFC 3339 in UTC, such as `2026-11-01T12:00:00Z` (fractions of a second allowed), on a day
/// its month has. Leap seconds (second 60) are refused.
fn utc_time(s: &str) -> bool {
    let b = s.as_bytes();
    let digits =
        |r: std::ops::Range<usize>| b.get(r).is_some_and(|d| d.iter().all(u8::is_ascii_digit));
    if b.len() < 20 || b.len() > 35 || b[b.len() - 1] != b'Z' {
        return false;
    }
    let shape = digits(0..4)
        && b[4] == b'-'
        && digits(5..7)
        && b[7] == b'-'
        && digits(8..10)
        && b[10] == b'T'
        && digits(11..13)
        && b[13] == b':'
        && digits(14..16)
        && b[16] == b':'
        && digits(17..19);
    let rest = &b[19..b.len() - 1];
    let fraction = rest.is_empty()
        || (rest.len() >= 2 && rest[0] == b'.' && rest[1..].iter().all(u8::is_ascii_digit));
    if !shape || !fraction {
        return false;
    }
    let n = |r: std::ops::Range<usize>| s[r].parse::<u32>().unwrap_or(99);
    let (year, month) = (n(0..4), n(5..7));
    let leap = year % 4 == 0 && (year % 100 != 0 || year % 400 == 0);
    let days = match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return false,
    };
    (1..=days).contains(&n(8..10)) && n(11..13) < 24 && n(14..16) < 60 && n(17..19) < 60
}

pub fn parse_feed(bytes: &[u8]) -> Result<Feed, String> {
    if bytes.len() as u64 > MAX_FEED {
        return Err("the feed is larger than 1 MiB".into());
    }
    let raw: RawFeed =
        serde_json::from_slice(bytes).map_err(|e| format!("the feed is not valid: {e}"))?;
    if raw.format != FORMAT {
        return Err(format!(
            "the feed has format {}; this app reads format {FORMAT}",
            raw.format
        ));
    }
    if raw.releases.len() > MAX_RELEASES {
        return Err(format!("the feed lists more than {MAX_RELEASES} releases"));
    }
    let mut releases: Vec<(Version, Release)> = Vec::with_capacity(raw.releases.len());
    for mut r in raw.releases {
        let bad = |why: String| Err(format!("release {:?} {:?}: {why}", r.product, r.version));
        if r.version.len() > MAX_VERSION {
            return bad(format!(
                "the version is longer than {MAX_VERSION} characters"
            ));
        }
        let v = match Version::parse(&r.version) {
            Ok(v) => v,
            Err(e) => return bad(format!("not a semantic version: {e}")),
        };
        if r.channel == Channel::Stable && !v.pre.is_empty() {
            return bad("a stable release cannot have a pre-release version".into());
        }
        if !utc_time(&r.published) {
            return bad("published must be a UTC time such as 2026-11-01T12:00:00Z".into());
        }
        if r.notes.len() > MAX_NOTES {
            return bad("the notes are larger than 64 KiB".into());
        }
        if let Some(u) = &r.notes_url {
            match https_url(u) {
                Ok(u) => r.notes_url = Some(u),
                Err(e) => return bad(format!("notes_url: {e}")),
            }
        }
        if releases
            .iter()
            .any(|(w, o)| o.product == r.product && w.cmp_precedence(&v) == Ordering::Equal)
        {
            return bad("the feed lists this version twice".into());
        }
        releases.push((v, r));
    }
    Ok(Feed { releases })
}

impl Feed {
    /// The release a channel offers for product: the highest version on it.
    pub fn latest(&self, product: Product, channel: Channel) -> Option<&Release> {
        self.releases
            .iter()
            .filter(|(_, r)| {
                r.product == product
                    && (channel == Channel::Preview || r.channel == Channel::Stable)
            })
            .max_by(|(a, _), (b, _)| a.cmp_precedence(b))
            .map(|(_, r)| r)
    }

    fn to_json(&self) -> serde_json::Value {
        serde_json::json!({
            "format": FORMAT,
            "releases": self.releases.iter().map(|(_, r)| r).collect::<Vec<_>>(),
        })
    }
}

/// What the app keeps in its config folder, `release.json`, readable only by this user.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Settings {
    /// The feed's address. Unset: no feed, and the app checks nothing.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    #[serde(default)]
    pub channel: Channel,
}

/// The last feed read from source, in the app's cache folder (`release-feed.json`).
#[derive(Serialize, Deserialize)]
struct CacheFile {
    source: String,
    /// When it was read, in seconds since 1970.
    checked_at: u64,
    feed: serde_json::Value,
}

struct Cached {
    source: String,
    checked_at: u64,
    feed: Feed,
}

/// Where a check's feed comes from: its trusted certificates (the public roots unless set).
pub type Roots = Option<Arc<Vec<ureq::tls::Certificate<'static>>>>;

/// The feed at source, read over https with no redirects.
pub fn fetch(source: &str, roots: &Roots) -> Result<Vec<u8>, Problem> {
    use ureq::tls::{RootCerts, TlsConfig};
    let roots = match roots {
        Some(c) => RootCerts::Specific(c.clone()),
        None => RootCerts::WebPki,
    };
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .proxy(ureq::Proxy::try_from_env())
        .http_status_as_error(false)
        .https_only(true)
        .max_redirects(0)
        .timeout_connect(Some(Duration::from_secs(10)))
        .timeout_global(Some(Duration::from_secs(30)))
        .user_agent("pluxy-app")
        .tls_config(TlsConfig::builder().root_certs(roots).build())
        .build()
        .into();
    let mut resp = agent.get(source).call().map_err(|e| Problem {
        offline: true,
        message: format!("Could not reach the release feed: {e}"),
    })?;
    let status = resp.status().as_u16();
    if status != 200 {
        return Err(Problem {
            offline: false,
            message: format!("The release feed answered {status}."),
        });
    }
    resp.body_mut()
        .with_config()
        .limit(MAX_FEED + 1)
        .read_to_vec()
        .map_err(|e| Problem {
            offline: true,
            message: format!("Could not read the release feed: {e}"),
        })
        .and_then(|b| {
            if b.len() as u64 > MAX_FEED {
                Err(Problem {
                    offline: false,
                    message: "The release feed is larger than 1 MiB.".into(),
                })
            } else {
                Ok(b)
            }
        })
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Problem {
    /// The feed could not be reached at all, as when the computer is offline.
    pub offline: bool,
    pub message: String,
}

/// What the window may show about one product. No notes text and no addresses.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct ProductView {
    pub installed: Option<String>,
    pub latest: Option<LatestView>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct LatestView {
    pub version: String,
    pub channel: Channel,
    pub published: String,
    pub has_notes: bool,
    pub has_notes_url: bool,
    /// Whether it is newer than the installed version; unset when that cannot be told.
    pub newer: Option<bool>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct View {
    pub source: Option<String>,
    pub channel: Channel,
    pub app: ProductView,
    pub core: ProductView,
    /// When the feed shown was read, in seconds since 1970.
    pub checked_at: Option<u64>,
    /// Why the last check failed; the feed shown, if any, is the last good one.
    pub problem: Option<Problem>,
    /// A feed change waits for its native confirmation.
    pub changing_source: bool,
}

/// A feed change the person asked for, waiting for its native confirmation.
pub struct SourceChange {
    pub action: String,
    pub source: Option<String>,
    /// The settings revision it was asked against: a change in between makes it stale.
    pub revision: u64,
}

pub struct Releases {
    settings_path: PathBuf,
    cache_path: PathBuf,
    settings: Settings,
    /// Why release.json could not be read: changes are refused until it is fixed.
    broken: Option<String>,
    cached: Option<Cached>,
    problem: Option<Problem>,
    pub change: Option<SourceChange>,
    revision: u64,
}

/// The file at path when it holds at most limit bytes; never reads more than limit + 1.
fn read_limited(path: &Path, limit: u64) -> std::io::Result<Vec<u8>> {
    use std::io::Read;
    let mut b = Vec::new();
    fs::File::open(path)?.take(limit + 1).read_to_end(&mut b)?;
    if b.len() as u64 > limit {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("larger than {limit} bytes"),
        ));
    }
    Ok(b)
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

impl Releases {
    pub fn open(settings_path: PathBuf, cache_path: PathBuf) -> Releases {
        // A saved address passes the same checks as a new one, or the file counts as broken.
        let loaded = match read_limited(&settings_path, MAX_SETTINGS) {
            Ok(b) => serde_json::from_slice::<Settings>(&b)
                .map_err(|e| e.to_string())
                .and_then(|mut s| {
                    if let Some(src) = &s.source {
                        s.source = Some(https_url(src).map_err(|e| format!("source: {e}"))?);
                    }
                    Ok(s)
                }),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Settings::default()),
            Err(e) => Err(e.to_string()),
        };
        let (settings, broken) = match loaded {
            Ok(s) => (s, None),
            Err(e) => (
                Settings::default(),
                Some(format!("{}: {e}", settings_path.display())),
            ),
        };
        let mut r = Releases {
            settings_path,
            cache_path,
            settings,
            broken,
            cached: None,
            problem: None,
            change: None,
            revision: 0,
        };
        r.cached = r.load_cache();
        r
    }

    /// The cache, when it is for the current source and still passes every check.
    fn load_cache(&self) -> Option<Cached> {
        let source = self.settings.source.as_ref()?;
        let bytes = read_limited(&self.cache_path, MAX_CACHE).ok()?;
        let c: CacheFile = serde_json::from_slice(&bytes).ok()?;
        if &c.source != source {
            return None;
        }
        let feed = parse_feed(&serde_json::to_vec(&c.feed).ok()?).ok()?;
        Some(Cached {
            source: c.source,
            checked_at: c.checked_at,
            feed,
        })
    }

    pub fn settings(&self) -> &Settings {
        &self.settings
    }

    pub fn revision(&self) -> u64 {
        self.revision
    }

    fn save(&mut self, s: Settings) -> Result<(), String> {
        if let Some(b) = &self.broken {
            return Err(format!(
                "the release settings could not be read, so they are not changed: {b}"
            ));
        }
        if let Some(dir) = self.settings_path.parent() {
            fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let text = serde_json::to_vec_pretty(&s).expect("settings are JSON");
        write_private(&self.settings_path, &text)
            .map_err(|e| format!("saving {}: {e}", self.settings_path.display()))?;
        self.settings = s;
        self.revision += 1;
        Ok(())
    }

    pub fn set_channel(&mut self, channel: Channel) -> Result<(), String> {
        let s = Settings {
            channel,
            ..self.settings.clone()
        };
        self.save(s)
    }

    /// Checks a requested feed address before it is offered for confirmation.
    pub fn check_source(&self, source: Option<&str>) -> Result<Option<String>, String> {
        if let Some(b) = &self.broken {
            return Err(format!("the release settings could not be read: {b}"));
        }
        if self.change.is_some() {
            return Err("a feed change is already waiting for confirmation".into());
        }
        let source = source.map(https_url).transpose()?;
        if source == self.settings.source {
            return Err("that is already the release feed".into());
        }
        Ok(source)
    }

    /// Applies a confirmed feed change. The old feed's copy and problem go with the old source.
    pub fn apply_source(&mut self, source: Option<String>, revision: u64) -> Result<(), String> {
        if revision != self.revision {
            return Err("the release settings changed while the feed change waited".into());
        }
        let s = Settings {
            source,
            ..self.settings.clone()
        };
        self.save(s)?;
        self.cached = None;
        self.problem = None;
        let _ = fs::remove_file(&self.cache_path);
        Ok(())
    }

    /// Takes a feed read for source, or the reason it could not be read. A bad feed, or a
    /// problem, leaves the last good copy in place.
    pub fn checked(&mut self, source: &str, read: Result<Vec<u8>, Problem>) {
        if self.settings.source.as_deref() != Some(source) {
            return; // the feed changed while this check ran
        }
        let feed = read.and_then(|b| {
            parse_feed(&b).map_err(|e| Problem {
                offline: false,
                message: format!("The release feed was refused: {e}"),
            })
        });
        match feed {
            Ok(feed) => {
                let checked_at = now();
                let file = CacheFile {
                    source: source.to_string(),
                    checked_at,
                    feed: feed.to_json(),
                };
                if let Some(dir) = self.cache_path.parent() {
                    let _ = fs::create_dir_all(dir);
                }
                let saved = serde_json::to_vec(&file)
                    .map_err(|e| e.to_string())
                    .and_then(|b| write_private(&self.cache_path, &b).map_err(|e| e.to_string()));
                if let Err(e) = saved {
                    eprintln!("pluxy-app: could not keep the release feed: {e}");
                }
                self.cached = Some(Cached {
                    source: source.to_string(),
                    checked_at,
                    feed,
                });
                self.problem = None;
            }
            Err(p) => self.problem = Some(p),
        }
    }

    /// The release the current channel offers for product, from the last good feed.
    pub fn latest(&self, product: Product) -> Option<&Release> {
        let c = self.cached.as_ref()?;
        if Some(&c.source) != self.settings.source.as_ref() {
            return None;
        }
        c.feed.latest(product, self.settings.channel)
    }

    pub fn view(&self, app_version: &str, core_version: Option<String>) -> View {
        let product = |p: Product, installed: Option<String>| {
            let latest = self.latest(p).map(|r| LatestView {
                version: r.version.clone(),
                channel: r.channel,
                published: r.published.clone(),
                has_notes: !r.notes.trim().is_empty(),
                has_notes_url: r.notes_url.is_some(),
                newer: installed
                    .as_deref()
                    .and_then(|i| Version::parse(i.trim_start_matches('v')).ok())
                    .and_then(|i| {
                        Version::parse(&r.version)
                            .ok()
                            .map(|l| l.cmp_precedence(&i) == Ordering::Greater)
                    }),
            });
            ProductView { installed, latest }
        };
        View {
            source: self.settings.source.clone(),
            channel: self.settings.channel,
            app: product(Product::App, Some(app_version.to_string())),
            core: product(Product::Core, core_version),
            checked_at: self.cached.as_ref().map(|c| c.checked_at),
            problem: self.problem.clone().or_else(|| {
                self.broken.as_ref().map(|b| Problem {
                    offline: false,
                    message: format!(
                        "The release settings could not be read: {b}. Fix or delete that file, \
                         then restart the app; until then no feed is used and nothing is saved."
                    ),
                })
            }),
            changing_source: self.change.is_some(),
        }
    }
}

/// The version core reports in its status, if it is a short printable text.
pub fn core_version(status: Option<&serde_json::Value>) -> Option<String> {
    let v = status?.get("version")?.as_str()?;
    (!v.is_empty() && v.len() <= MAX_VERSION && v.bytes().all(|b| b.is_ascii_graphic()))
        .then(|| v.to_string())
}

pub fn settings_path(config: &Path) -> PathBuf {
    config.join("release.json")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed(releases: serde_json::Value) -> Result<Feed, String> {
        parse_feed(
            &serde_json::to_vec(&serde_json::json!({"format": 1, "releases": releases})).unwrap(),
        )
    }

    fn rel(product: &str, version: &str, channel: &str) -> serde_json::Value {
        serde_json::json!({"product": product, "version": version, "channel": channel, "published": "2026-11-01T12:00:00Z"})
    }

    #[test]
    fn each_channel_offers_its_highest_version() {
        let f = feed(serde_json::json!([
            rel("app", "0.9.0", "stable"),
            rel("app", "0.10.0", "stable"),
            rel("app", "0.11.0-preview.2", "preview"),
            rel("app", "0.11.0-preview.10", "preview"),
            rel("core", "1.2.3", "stable"),
        ]))
        .unwrap();
        // Versions compare as versions, not text: 0.10.0 is above 0.9.0, preview.10 above .2.
        assert_eq!(
            f.latest(Product::App, Channel::Stable).unwrap().version,
            "0.10.0"
        );
        assert_eq!(
            f.latest(Product::App, Channel::Preview).unwrap().version,
            "0.11.0-preview.10"
        );
        // Preview (latest) includes stable releases: core has no preview, so its stable shows.
        assert_eq!(
            f.latest(Product::Core, Channel::Preview).unwrap().version,
            "1.2.3"
        );
        let only_preview = feed(serde_json::json!([rel("app", "1.0.0-rc.1", "preview")])).unwrap();
        assert!(only_preview.latest(Product::App, Channel::Stable).is_none());
    }

    #[test]
    fn a_feed_with_any_bad_release_is_refused() {
        let cases = [
            (serde_json::json!({"format": 2, "releases": []}), "format 2"),
            (
                serde_json::json!({"format": 1, "releases": [rel("app", "v1.0.0", "stable")]}),
                "semantic version",
            ),
            (
                serde_json::json!({"format": 1, "releases": [rel("app", "1.0.0-rc.1", "stable")]}),
                "pre-release",
            ),
            (
                serde_json::json!({"format": 1, "releases": [rel("app", "1.0.0", "beta")]}),
                "not valid",
            ),
            (
                serde_json::json!({"format": 1, "releases": [rel("tool", "1.0.0", "stable")]}),
                "not valid",
            ),
            (
                serde_json::json!({"format": 1, "releases": [rel("app", "1.0.0", "stable"), rel("app", "1.0.0+b", "stable")]}),
                "twice",
            ),
            (
                serde_json::json!({"format": 1, "releases": [{"product": "app", "version": "1.0.0", "channel": "stable", "published": "yesterday"}]}),
                "UTC time",
            ),
            (
                serde_json::json!({"format": 1, "releases": [{"product": "app", "version": "1.0.0", "channel": "stable", "published": "2026-11-01T12:00:00Z", "notes_url": "http://example.com/"}]}),
                "https://",
            ),
            (
                serde_json::json!({"format": 1, "releases": [{"product": "app", "version": "1.0.0", "channel": "stable", "published": "2026-11-01T12:00:00Z", "notes_url": "https://u:p@example.com/"}]}),
                "user name",
            ),
            (
                serde_json::json!({"format": 1, "releases": [{"product": "app", "version": "1.0.0", "channel": "stable", "published": "2026-11-01T12:00:00Z", "notes": "x".repeat(MAX_NOTES + 1)}]}),
                "64 KiB",
            ),
        ];
        for (json, why) in cases {
            let e = parse_feed(&serde_json::to_vec(&json).unwrap()).unwrap_err();
            assert!(e.contains(why), "{json}: {e}");
        }
        assert!(parse_feed(&vec![b' '; MAX_FEED as usize + 1])
            .unwrap_err()
            .contains("1 MiB"));
        // Fields a later format adds are ignored, so later slices can add theirs.
        let mut extra = rel("app", "1.0.0", "stable");
        extra["artifacts"] = serde_json::json!({"linux-x86_64": {}});
        assert!(feed(serde_json::json!([extra])).is_ok());
    }

    #[test]
    fn addresses_keep_the_length_limit_after_normalising() {
        // 2,020 bytes as given; each é becomes %C3%A9, so 6,020 once normalised.
        let long = format!("https://example.com/{}", "é".repeat(1000));
        assert!(long.len() <= MAX_URL);
        assert!(https_url(&long).unwrap_err().contains("longer"));
        // What https_url returns, it accepts again: a kept feed reloads after a restart.
        let ok = format!("https://example.com/{}", "é".repeat(300));
        let normal = https_url(&ok).unwrap();
        assert_eq!(https_url(&normal).unwrap(), normal);
    }

    #[test]
    fn utc_times() {
        for ok in [
            "2026-11-01T12:00:00Z",
            "2026-02-28T23:59:59.123Z",
            "2024-02-29T00:00:00Z",
            "2000-02-29T00:00:00Z",
            "2026-04-30T00:00:00Z",
        ] {
            assert!(utc_time(ok), "{ok}");
        }
        for bad in [
            // Days a month does not have: February outside leap years (1900 is not one).
            "2026-02-29T00:00:00Z",
            "1900-02-29T00:00:00Z",
            "2026-04-31T00:00:00Z",
            "2026-02-31T00:00:00Z",
            // Leap seconds are refused.
            "2026-12-31T23:59:60Z",
            "2026-11-01T12:00:00+01:00",
            "2026-13-01T12:00:00Z",
            "2026-11-01 12:00:00Z",
            "2026-11-01T12:00:00.Z",
            "",
        ] {
            assert!(!utc_time(bad), "{bad}");
        }
    }

    fn dir() -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "pluxy-release-test-{}",
            crate::managed::random_key()
        ));
        fs::create_dir_all(&d).unwrap();
        d
    }

    const FEED: &str = r#"{"format":1,"releases":[{"product":"app","version":"0.2.0","channel":"stable","published":"2026-11-01T12:00:00Z","notes":"Hi"},{"product":"app","version":"0.3.0-preview.1","channel":"preview","published":"2026-11-02T12:00:00Z"}]}"#;

    #[test]
    fn the_last_good_feed_survives_a_failed_check_and_a_restart() {
        let d = dir();
        let open = || {
            Releases::open(
                d.join("release.json"),
                d.join("cache").join("release-feed.json"),
            )
        };
        let mut r = open();
        assert_eq!(r.view("0.1.0", None).source, None);
        let src = r
            .check_source(Some("https://feed.example/feed.json"))
            .unwrap();
        r.apply_source(src.clone(), r.revision()).unwrap();
        let src = src.unwrap();
        r.checked(&src, Ok(FEED.as_bytes().to_vec()));
        let v = r.view("0.1.0", Some("dev".into()));
        assert_eq!(v.app.latest.as_ref().unwrap().version, "0.2.0");
        assert_eq!(v.app.latest.as_ref().unwrap().newer, Some(true));
        assert_eq!(v.core.latest, None);
        assert_eq!(v.core.installed.as_deref(), Some("dev"));
        let at = v.checked_at.unwrap();
        r.checked(
            &src,
            Err(Problem {
                offline: true,
                message: "offline".into(),
            }),
        );
        let v = r.view("0.1.0", None);
        assert_eq!(v.checked_at, Some(at));
        assert!(v.problem.unwrap().offline);
        assert_eq!(v.app.latest.unwrap().version, "0.2.0");
        r.checked(&src, Ok(b"{\"format\":1,\"releases\":[{}]}".to_vec()));
        assert_eq!(r.view("0.1.0", None).app.latest.unwrap().version, "0.2.0");
        r.set_channel(Channel::Preview).unwrap();
        // After a restart: the same copy, the channel kept.
        let r2 = open();
        let v = r2.view("0.3.0", None);
        assert_eq!(v.channel, Channel::Preview);
        assert_eq!(v.checked_at, Some(at));
        assert_eq!(v.app.latest.as_ref().unwrap().version, "0.3.0-preview.1");
        assert_eq!(v.app.latest.unwrap().newer, Some(false));
        // A different feed drops the copy.
        let mut r3 = open();
        let other = r3
            .check_source(Some("https://other.example/f.json"))
            .unwrap();
        r3.apply_source(other, r3.revision()).unwrap();
        assert_eq!(r3.view("0.1.0", None).checked_at, None);
        assert_eq!(open().view("0.1.0", None).checked_at, None);
        fs::remove_dir_all(d).unwrap();
    }

    #[test]
    fn feed_changes_are_checked() {
        let d = dir();
        let mut r = Releases::open(d.join("release.json"), d.join("release-feed.json"));
        assert!(r.check_source(Some("http://feed.example/")).is_err());
        assert!(r.check_source(None).unwrap_err().contains("already"));
        let s = r.check_source(Some("https://feed.example/")).unwrap();
        let rev = r.revision();
        r.set_channel(Channel::Preview).unwrap();
        assert!(r.apply_source(s, rev).unwrap_err().contains("changed"));
        fs::write(d.join("release.json"), b"{broken").unwrap();
        let mut b = Releases::open(d.join("release.json"), d.join("release-feed.json"));
        assert!(b.set_channel(Channel::Stable).is_err());
        assert_eq!(fs::read(d.join("release.json")).unwrap(), b"{broken");
        assert!(b.view("0.1.0", None).problem.is_some());
        fs::remove_dir_all(d).unwrap();
    }

    #[test]
    fn settings_that_cannot_be_used_are_kept_and_shown() {
        let d = dir();
        let path = d.join("release.json");
        // Each loads as broken: the file stays as it is, changes are refused, and the window is
        // told what to do.
        let padded = format!(
            "{{\"channel\": \"stable\"{}}}",
            " ".repeat(MAX_SETTINGS as usize)
        );
        for (bytes, why) in [
            (padded.into_bytes(), "larger than"),
            (
                br#"{"source": "http://feed.example/feed.json"}"#.to_vec(),
                "https://",
            ),
            (
                br#"{"source": "https://u:p@feed.example/"}"#.to_vec(),
                "user name",
            ),
        ] {
            fs::write(&path, &bytes).unwrap();
            let mut r = Releases::open(path.clone(), d.join("release-feed.json"));
            assert_eq!(r.settings().source, None);
            assert!(r.set_channel(Channel::Preview).is_err());
            assert_eq!(fs::read(&path).unwrap(), bytes);
            let p = r.view("0.1.0", None).problem.unwrap();
            assert!(p.message.contains(why), "{}", p.message);
            assert!(p.message.contains("Fix or delete"), "{}", p.message);
        }
        // A kept feed larger than the cache limit is not read.
        fs::write(&path, br#"{"source": "https://feed.example/feed.json"}"#).unwrap();
        let cache = d.join("release-feed.json");
        fs::write(&cache, vec![b' '; MAX_CACHE as usize + 1]).unwrap();
        let r = Releases::open(path.clone(), cache);
        assert_eq!(
            r.settings().source.as_deref(),
            Some("https://feed.example/feed.json")
        );
        assert_eq!(r.view("0.1.0", None).checked_at, None);
        fs::remove_dir_all(d).unwrap();
    }

    #[test]
    fn core_version_is_short_printable_text() {
        assert_eq!(
            core_version(Some(&serde_json::json!({"version": "1.2.3"}))).as_deref(),
            Some("1.2.3")
        );
        assert_eq!(
            core_version(Some(&serde_json::json!({"version": "<b>\n"}))),
            None
        );
        assert_eq!(core_version(Some(&serde_json::json!({"version": 3}))), None);
        assert_eq!(core_version(None), None);
    }
}
