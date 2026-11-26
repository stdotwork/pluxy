//! Release notes, for a webview of their own (`notes-<n>`) that no capability names. A feed's
//! notes are Markdown; they become HTML here, with raw HTML left out (as the old app's notes
//! did), images shown as their alternative text, and only absolute https links kept as links. The document
//! has no script and its policy allows none, and it loads nothing but its own style sheet.
//!
//! Following a link never navigates that webview: native code opens the address in the
//! person's browser, and only an address the notes it rendered link to, at most once a second.
//! The document can run no script, so only a person activating a link navigates it.
//! Nothing in this module needs Tauri.

use crate::release::{https_url, Product};
use pulldown_cmark::{html, Event, Options, Parser, Tag, TagEnd};
use std::collections::HashMap;
use std::time::{Duration, Instant};
use url::Url;

pub const SCHEME: &str = "pluxy-notes";
/// Notes webviews open at once: one each for the app's and core's release.
pub const MAX_OPEN: usize = 2;
const STYLE: &str = include_str!("../../page/notes.css");
pub const CSP: &str = "default-src 'none'; style-src 'self'; img-src 'none'; script-src 'none'; \
                       connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'; \
                       frame-ancestors 'none'";

/// The Markdown as HTML, and the addresses it links to.
pub fn render(markdown: &str) -> (String, Vec<String>) {
    let mut links = Vec::new();
    // For each link open: whether it was kept, so its end is kept or dropped the same way.
    let mut open: Vec<bool> = Vec::new();
    let events = Parser::new_ext(
        markdown,
        Options::ENABLE_TABLES | Options::ENABLE_STRIKETHROUGH,
    )
    .filter_map(|e| match e {
        // Raw HTML is left out, comments included; text between inline tags stays.
        Event::Html(_) | Event::InlineHtml(_) => None,
        Event::Start(Tag::Link {
            link_type,
            dest_url,
            title,
            id,
        }) => match https_url(&dest_url).and_then(|u| navigated(&u).map(|n| (u, n))) {
            Ok((u, n)) => {
                open.push(true);
                links.push(n);
                Some(Event::Start(Tag::Link {
                    link_type,
                    dest_url: u.into(),
                    title,
                    id,
                }))
            }
            _ => {
                open.push(false);
                None
            }
        },
        Event::End(TagEnd::Link) => open.pop().unwrap_or(false).then_some(e),
        // An image is its alternative text: the events between start and end.
        Event::Start(Tag::Image { .. }) | Event::End(TagEnd::Image) => None,
        other => Some(other),
    });
    let mut out = String::new();
    html::push_html(&mut out, events);
    (out, links)
}

/// The address a browser navigates to for a link to u: the renderer percent-encodes some
/// characters (`[ ] { } | \` and backquote among them) in what it writes, so a link is
/// matched in that form. An IPv6 host's brackets do not survive that: such a link is dropped.
fn navigated(u: &str) -> Result<String, String> {
    let mut href = String::new();
    pulldown_cmark_escape::escape_href(&mut href, u).map_err(|e| e.to_string())?;
    https_url(&href.replace("&#x27;", "'").replace("&amp;", "&"))
}

/// The window's theme, for the notes' style sheet: each value as the theme sets it.
#[derive(Clone, Debug, Default, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Style {
    pub text: String,
    pub muted: String,
    pub surface: String,
    pub border: String,
    pub accent: String,
    pub font: String,
    pub mono: String,
    pub size: String,
}

/// The same rule the kit's themes follow (`ui/kit/theme/contract.ts`), and no control characters.
fn style_value(v: &str) -> Option<&str> {
    let lower = v.to_ascii_lowercase();
    (!v.is_empty()
        && v.len() <= 200
        && !v.chars().any(|c| c.is_control() || ";{}<>\\@".contains(c))
        && !lower.contains("/*")
        && !lower.contains("url("))
    .then_some(v)
}

impl Style {
    fn css(&self) -> String {
        let mut s = String::from(":root {\n");
        for (name, v) in [
            ("text", &self.text),
            ("muted", &self.muted),
            ("surface", &self.surface),
            ("border", &self.border),
            ("accent", &self.accent),
            ("font", &self.font),
            ("mono", &self.mono),
            ("size", &self.size),
        ] {
            if let Some(v) = style_value(v.trim()) {
                s.push_str(&format!("  --{name}: {v};\n"));
            }
        }
        s.push_str("}\n");
        s.push_str(STYLE);
        s
    }
}

pub struct Doc {
    pub product: Product,
    html: String,
    css: String,
    links: Vec<String>,
}

impl Doc {
    pub fn new(product: Product, markdown: &str, style: &Style) -> Doc {
        let (body, links) = render(markdown);
        Doc {
            product,
            html: format!(
                "<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\
                 <meta name=\"referrer\" content=\"no-referrer\"><title>Release notes</title>\
                 <link rel=\"stylesheet\" href=\"/notes.css\"></head><body><main>{body}</main></body></html>\n"
            ),
            css: style.css(),
            links,
        }
    }
}

pub struct Served {
    pub status: u16,
    pub kind: &'static str,
    pub body: Vec<u8>,
}

#[derive(Default)]
pub struct Notes {
    next: u64,
    open: HashMap<String, Doc>,
    last_external: Option<Instant>,
    #[cfg(feature = "native-test")]
    pub refused: usize,
    /// Downloads refused and context menus not shown, for the native test.
    #[cfg(feature = "native-test")]
    pub downloads: usize,
    #[cfg(feature = "native-test")]
    pub menus: usize,
}

impl Notes {
    pub fn bind(&mut self, doc: Doc) -> Result<String, String> {
        if self.open.len() >= MAX_OPEN {
            return Err("too many release notes are open".into());
        }
        self.next = self.next.checked_add(1).ok_or("notes labels exhausted")?;
        let label = format!("notes-{}", self.next);
        self.open.insert(label.clone(), doc);
        Ok(label)
    }
    pub fn has(&self, label: &str) -> bool {
        self.open.contains_key(label)
    }
    pub fn unbind(&mut self, label: &str) {
        self.open.remove(label);
    }
    pub fn labels(&self) -> Vec<String> {
        self.open.keys().cloned().collect()
    }
    pub fn clear(&mut self) {
        self.open.clear();
    }

    /// What the notes scheme serves to the webview labelled label: its own document only.
    pub fn serve(&self, label: &str, method: &str, path: &str) -> Served {
        let not_found = || Served {
            status: 404,
            kind: "text/plain",
            body: Vec::new(),
        };
        let Some(doc) = self.open.get(label) else {
            return not_found();
        };
        match (method, path) {
            ("GET", "/") => Served {
                status: 200,
                kind: "text/html; charset=utf-8",
                body: doc.html.clone().into_bytes(),
            },
            ("GET", "/notes.css") => Served {
                status: 200,
                kind: "text/css; charset=utf-8",
                body: doc.css.clone().into_bytes(),
            },
            ("GET", _) => not_found(),
            _ => Served {
                status: 405,
                kind: "text/plain",
                body: Vec::new(),
            },
        }
    }

    /// The address to open in the browser for a navigation of label's webview to u: only an
    /// https address its notes link to, and at most one a second.
    pub fn external(&mut self, label: &str, u: &Url) -> Option<String> {
        let allowed = self.open.get(label).and_then(|d| {
            let s = u.as_str();
            d.links.iter().find(|l| l.as_str() == s).cloned()
        });
        let allowed = allowed.filter(|_| self.rate());
        #[cfg(feature = "native-test")]
        if allowed.is_none() {
            self.refused += 1;
        }
        allowed
    }

    /// One external address a second, for notes links and "View full release notes" alike.
    pub fn rate(&mut self) -> bool {
        let now = Instant::now();
        if self
            .last_external
            .is_some_and(|t| now.duration_since(t) < Duration::from_secs(1))
        {
            return false;
        }
        self.last_external = Some(now);
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hostile_markdown_renders_without_html() {
        let md = "# Title\n\n**bold** <script>alert(1)</script> <img src=x onerror=alert(1)>\n\n\
                  <iframe src=\"https://evil.example/\"></iframe>\n\n\
                  ![tracker](https://evil.example/pixel.png) [js](javascript:alert(1)) \
                  [plain](http://example.com/) [rel](notes.html) [ok](https://example.com/a?b=1&c=2 \"t\")\n\n\
                  <https://example.com/auto>\n\n| a | b |\n|---|---|\n| 1 | 2 |\n";
        let (html, links) = render(md);
        // Independent of the renderer: the tags it may write, and nothing a feed wrote.
        assert!(html.contains("<h1>Title</h1>"), "{html}");
        assert!(html.contains("<strong>bold</strong>"));
        assert!(html.contains("<table>"));
        for bad in [
            "script",
            "<img",
            "onerror",
            "iframe",
            "evil",
            "javascript:",
            "href=\"http:",
            "notes.html\"",
            "pixel.png",
        ] {
            assert!(!html.contains(bad), "{bad} in {html}");
        }
        assert!(html.contains("tracker"), "an image shows its text");
        assert!(
            html.contains("<a href=\"https://example.com/a?b=1&amp;c=2\" title=\"t\">ok</a>"),
            "{html}"
        );
        assert_eq!(
            links,
            ["https://example.com/a?b=1&c=2", "https://example.com/auto"]
        );
    }

    /// The addresses a browser navigates to for the links in html: each href as written,
    /// with its HTML escapes undone, parsed as a URL.
    fn hrefs(html: &str) -> Vec<Url> {
        html.split("href=\"")
            .skip(1)
            .map(|rest| {
                let raw = &rest[..rest.find('"').unwrap()];
                Url::parse(&raw.replace("&#x27;", "'").replace("&amp;", "&")).unwrap()
            })
            .collect()
    }

    #[test]
    fn every_rendered_link_opens_as_it_is_navigated() {
        let md = "[a](https://example.com/a[1]?q=x|y) [b](<https://example.com/{b}\\c`d>) \
                  [c](https://example.com/it's?x=1&y=2) [v6](https://[::1]/notes)";
        let (html, _) = render(md);
        let navigated = hrefs(&html);
        assert_eq!(navigated.len(), 3, "{html}");
        for u in navigated {
            let mut n = Notes::default();
            let label = n
                .bind(Doc::new(Product::App, md, &Style::default()))
                .unwrap();
            assert_eq!(
                n.external(&label, &u).as_deref(),
                Some(u.as_str()),
                "{html}"
            );
        }
        // An IPv6 host cannot survive the renderer's escaping of its brackets: no link.
        assert!(!html.contains("::1"), "{html}");
    }

    #[test]
    fn only_rendered_links_open_once_a_second() {
        let mut n = Notes::default();
        let label = n
            .bind(Doc::new(
                Product::App,
                "[a](https://example.com/a)",
                &Style::default(),
            ))
            .unwrap();
        let other = Url::parse("https://example.com/b").unwrap();
        let a = Url::parse("https://example.com/a").unwrap();
        assert_eq!(n.external(&label, &other), None);
        assert_eq!(n.external("notes-99", &a), None);
        assert_eq!(
            n.external(&label, &a).as_deref(),
            Some("https://example.com/a")
        );
        assert_eq!(n.external(&label, &a), None, "a second within the second");
        assert_eq!(n.serve(&label, "GET", "/").status, 200);
        assert_eq!(n.serve("page-1", "GET", "/").status, 404);
        assert_eq!(n.serve(&label, "POST", "/").status, 405);
        assert_eq!(n.serve(&label, "GET", "/x.js").status, 404);
        n.bind(Doc::new(Product::Core, "", &Style::default()))
            .unwrap();
        assert!(n
            .bind(Doc::new(Product::Core, "", &Style::default()))
            .is_err());
    }

    #[test]
    fn style_values_cannot_leave_their_declaration() {
        let s = Style {
            text: "#111".into(),
            muted: "red; } body { display: none".into(),
            surface: "url(https://evil.example/)".into(),
            font: "system-ui, \"Segoe UI\"".into(),
            ..Style::default()
        };
        let css = s.css();
        assert!(css.contains("--text: #111;"));
        assert!(css.contains("--font: system-ui, \"Segoe UI\";"));
        assert!(!css.contains("display: none"));
        assert!(!css.contains("evil"));
    }
}
