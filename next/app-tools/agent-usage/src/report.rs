//! `report`: what the page shows, from the stored UTC hours, bucketed in the viewer's zone. The
//! page sends its UTC offsets over time (`offsets`, `[[from unix seconds, minutes east of UTC]]`,
//! ascending) and the time it asks at (`now`); days, weeks (from Monday), months and hours are
//! local to those offsets, as the old app's were to its own zone.
//!
//! shortcut: a zone whose offset is not a whole hour (such as +05:30) gets each UTC hour in the
//! local hour it starts in; exact half-hour buckets would need rows finer than an hour.

use crate::store::Store;
use pluxy_program::Error;
use pluxy_usage::{Tokens, MAX_HOUR};
use serde_json::{json, Value};
use std::collections::{BTreeSet, HashMap};

const HOUR: i64 = 3600;
const DAY: i64 = 86_400;
/// Keys a trend stacks before the rest become Other.
const TOP: usize = 6;
pub const OTHER: &str = "__other__";
/// The calendar shows at least this many days before today.
const CALENDAR_DAYS: i64 = 364;
/// The hours grid covers the last this many days, today included.
const HOURS_DAYS: i64 = 30;
/// "All" with more days than this defaults to weeks.
const WEEKS_AFTER: i64 = 92;

/// UTC offsets over time.
pub struct Zone(Vec<(i64, i64)>);

impl Zone {
    /// From `[[unix seconds, minutes]]`: 1 to 1,024 entries, ascending, each offset within ±18 h.
    pub fn from_json(v: &Value) -> Result<Zone, Error> {
        let bad = || Error::invalid_request("offsets is not a list of [unix seconds, minutes]");
        let a = v
            .as_array()
            .filter(|a| !a.is_empty() && a.len() <= 1024)
            .ok_or_else(bad)?;
        let mut out: Vec<(i64, i64)> = Vec::with_capacity(a.len());
        for e in a {
            let (Some(at), Some(min)) = (e[0].as_i64(), e[1].as_i64()) else {
                return Err(bad());
            };
            if !(-1080..=1080).contains(&min) || out.last().is_some_and(|l| l.0 >= at) {
                return Err(bad());
            }
            out.push((at, min * 60));
        }
        Ok(Zone(out))
    }

    fn offset(&self, utc: i64) -> i64 {
        let i = self.0.partition_point(|(at, _)| *at <= utc);
        self.0[i.saturating_sub(1)].1
    }

    /// Local seconds (unix-like, in the zone) of a UTC instant.
    pub fn local(&self, utc: i64) -> i64 {
        utc + self.offset(utc)
    }
}

fn day(local: i64) -> i64 {
    local.div_euclid(DAY)
}

/// Year, month and day of day number d (days since 1970-01-01).
fn civil(d: i64) -> (i64, i64, i64) {
    let z = d + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let dd = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    (yoe + era * 400 + i64::from(m <= 2), m, dd)
}

pub fn date(d: i64) -> String {
    let (y, m, dd) = civil(d);
    format!("{y:04}-{m:02}-{dd:02}")
}

/// Monday 0 to Sunday 6.
fn weekday(d: i64) -> i64 {
    (d + 3).rem_euclid(7)
}

fn monday(d: i64) -> i64 {
    d - weekday(d)
}

fn month(d: i64) -> i64 {
    let (y, m, _) = civil(d);
    y * 12 + m - 1
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Granularity {
    Hour,
    Day,
    Week,
    Month,
}

impl Granularity {
    fn name(self) -> &'static str {
        match self {
            Granularity::Hour => "hour",
            Granularity::Day => "day",
            Granularity::Week => "week",
            Granularity::Month => "month",
        }
    }

    fn parse(s: &str) -> Option<Granularity> {
        Some(match s {
            "hour" => Granularity::Hour,
            "day" => Granularity::Day,
            "week" => Granularity::Week,
            "month" => Granularity::Month,
            _ => return None,
        })
    }
}

/// The buckets of a window: their keys, and the bucket of a local time.
struct Buckets {
    g: Granularity,
    first: i64,
    keys: Vec<String>,
}

impl Buckets {
    fn new(g: Granularity, from: i64, to: i64) -> Buckets {
        let (first_day, last_day) = (day(from), day(to - 1));
        let (first, keys) = match g {
            Granularity::Hour => {
                let first = from.div_euclid(HOUR);
                let keys = (first..=(to - 1).div_euclid(HOUR))
                    .map(|h| {
                        format!(
                            "{}T{:02}",
                            date(day(h * HOUR)),
                            (h * HOUR).rem_euclid(DAY) / HOUR
                        )
                    })
                    .collect();
                (first, keys)
            }
            Granularity::Day => (first_day, (first_day..=last_day).map(date).collect()),
            Granularity::Week => {
                let first = monday(first_day);
                let keys = (first..=monday(last_day)).step_by(7).map(date).collect();
                (first, keys)
            }
            Granularity::Month => {
                let first = month(first_day);
                let keys = (first..=month(last_day))
                    .map(|m| format!("{:04}-{:02}", m.div_euclid(12), m.rem_euclid(12) + 1))
                    .collect();
                (first, keys)
            }
        };
        Buckets { g, first, keys }
    }

    fn of(&self, local: i64) -> usize {
        (match self.g {
            Granularity::Hour => local.div_euclid(HOUR) - self.first,
            Granularity::Day => day(local) - self.first,
            Granularity::Week => (monday(day(local)) - self.first) / 7,
            Granularity::Month => month(day(local)) - self.first,
        }) as usize
    }
}

/// Totals per key in each bucket, ranked: the top keys, then Other.
fn stacked(per_bucket: &[HashMap<String, u64>]) -> Value {
    let mut sums: HashMap<&str, u64> = HashMap::new();
    for b in per_bucket {
        for (k, v) in b {
            *sums.entry(k.as_str()).or_default() += v;
        }
    }
    let mut ranked: Vec<(&str, u64)> = sums.into_iter().filter(|(_, v)| *v > 0).collect();
    ranked.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(b.0)));
    let other = ranked.len() > TOP;
    let top: Vec<&str> = ranked.iter().take(TOP).map(|(k, _)| *k).collect();
    let mut keys: Vec<String> = top.iter().map(|k| k.to_string()).collect();
    if other {
        keys.push(OTHER.into());
    }
    let values: Vec<Vec<u64>> = per_bucket
        .iter()
        .map(|b| {
            let mut row: Vec<u64> = top
                .iter()
                .map(|k| b.get(*k).copied().unwrap_or(0))
                .collect();
            if other {
                row.push(
                    b.iter()
                        .filter(|(k, _)| !top.contains(&k.as_str()))
                        .map(|(_, v)| v)
                        .sum(),
                );
            }
            row
        })
        .collect();
    json!({"keys": keys, "values": values})
}

/// The report for input (see the README's `report`), from store, with the sources the host has
/// registered now (the others are archived) and the sync status.
pub fn report(
    store: &Store,
    input: &Value,
    registered: &[String],
    sync: Value,
) -> Result<Value, Error> {
    let internal = |e: String| Error::internal(e);
    let zone = Zone::from_json(&input["offsets"])?;
    let now = input["now"]
        .as_i64()
        .filter(|n| (0..=MAX_HOUR).contains(n))
        .ok_or_else(|| Error::invalid_request("now is not a time"))?;
    let range = input["range"].as_str().unwrap_or_default();
    let filter: Vec<String> = match &input["sources"] {
        Value::Null => Vec::new(),
        Value::Array(a) if a.len() <= 64 => a
            .iter()
            .filter_map(|s| s.as_str().map(String::from))
            .collect(),
        _ => {
            return Err(Error::invalid_request(
                "sources is not a list of source IDs",
            ))
        }
    };
    let passes = |s: &str| filter.is_empty() || filter.iter().any(|f| f == s);
    let now_local = zone.local(now);
    let today = day(now_local);
    let hour_end = (now_local.div_euclid(HOUR) + 1) * HOUR;
    let first_day = store
        .first_hour()
        .map_err(internal)?
        .map_or(today, |h| day(zone.local(h)).min(today));
    // The window and the previous period, in local seconds, [from, to).
    let days = |n: i64| ((today - n + 1) * DAY, (today + 1) * DAY);
    let (from, to, shift) = match range {
        "today" => (today * DAY, hour_end, Some(DAY)),
        "24h" => (hour_end - 24 * HOUR, hour_end, Some(24 * HOUR)),
        "7d" => (days(7).0, days(7).1, Some(7 * DAY)),
        "30d" => (days(30).0, days(30).1, Some(30 * DAY)),
        "all" => (first_day * DAY, (today + 1) * DAY, None),
        _ => {
            return Err(Error::invalid_request(
                "range is not today, 24h, 7d, 30d or all",
            ))
        }
    };
    let asked = match &input["granularity"] {
        Value::Null => None,
        Value::String(s) => Some(Granularity::parse(s).ok_or_else(|| {
            Error::invalid_request("granularity is not hour, day, week or month")
        })?),
        _ => return Err(Error::invalid_request("granularity is not a string")),
    };
    let granularity = match (asked, range) {
        (Some(Granularity::Hour), "all") => Granularity::Day,
        (Some(g), _) => g,
        (None, "today" | "24h") => Granularity::Hour,
        (None, "all") if day(to - 1) - day(from) + 1 > WEEKS_AFTER => Granularity::Week,
        (None, _) => Granularity::Day,
    };
    let buckets = Buckets::new(granularity, from, to);
    let n = buckets.keys.len();

    // Rows a little either side of the window and the previous period (offsets are within ±18 h).
    let earliest = from - shift.unwrap_or(0) - 2 * DAY;
    let rows = store.rows(earliest, to + 2 * DAY).map_err(internal)?;
    let mut totals = Tokens::default();
    let mut previous = Tokens::default();
    let mut series = vec![Tokens::default(); n];
    let mut by_source: Vec<HashMap<String, u64>> = vec![HashMap::new(); n];
    let mut by_model: Vec<HashMap<String, u64>> = vec![HashMap::new(); n];
    let mut models: HashMap<String, u64> = HashMap::new();
    let mut active: BTreeSet<i64> = BTreeSet::new();
    let mut source_totals: HashMap<String, (u64, BTreeSet<i64>)> = HashMap::new();
    for (source, hour, model, t) in &rows {
        let local = zone.local(*hour);
        if let Some(s) = shift {
            if (from - s..to - s).contains(&local) && passes(source) {
                previous.add(t);
            }
        }
        if !(from..to).contains(&local) {
            continue;
        }
        let st = source_totals.entry(source.clone()).or_default();
        st.0 += t.total;
        if t.total > 0 {
            st.1.insert(day(local));
        }
        if !passes(source) {
            continue;
        }
        let b = buckets.of(local);
        totals.add(t);
        series[b].add(t);
        *by_source[b].entry(source.clone()).or_default() += t.total;
        *by_model[b].entry(model.clone()).or_default() += t.total;
        *models.entry(model.clone()).or_default() += t.total;
        if t.total > 0 {
            active.insert(day(local));
        }
    }
    let mut models: Vec<(String, u64)> = models.into_iter().filter(|(_, v)| *v > 0).collect();
    models.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(&b.0)));

    // Every source with rows, and every one registered now.
    let known = store.sources().map_err(internal)?;
    let mut sources: Vec<Value> = known
        .iter()
        .filter(|s| s.rows > 0 || registered.contains(&s.id))
        .map(|s| {
            (
                s.id.clone(),
                if s.label.is_empty() {
                    s.id.clone()
                } else {
                    s.label.clone()
                },
            )
        })
        .chain(
            registered
                .iter()
                .filter(|r| !known.iter().any(|k| &k.id == *r))
                .map(|r| (r.clone(), r.clone())),
        )
        .map(|(id, label)| {
            let (total, days) = source_totals
                .get(&id)
                .map_or((0, 0), |(t, d)| (*t, d.len()));
            let status = if registered.contains(&id) {
                "active"
            } else {
                "archived"
            };
            json!({"id": id, "label": label, "status": status, "total": total, "active_days": days})
        })
        .collect();
    sources.sort_by(|a, b| {
        b["total"]
            .as_u64()
            .cmp(&a["total"].as_u64())
            .then(a["id"].as_str().cmp(&b["id"].as_str()))
    });

    // The calendar (from a Monday at least a year back, or the first row's week) and the hours
    // of the last 30 days, whatever the range, with the filter.
    let start = monday(first_day.min(today - CALENDAR_DAYS));
    let mut calendar = vec![0u64; (today - start + 1) as usize];
    let mut hours = vec![vec![0u64; 24]; 7];
    for (source, hour, total) in store.hourly_totals().map_err(internal)? {
        if !passes(&source) {
            continue;
        }
        let local = zone.local(hour);
        let d = day(local);
        if (start..=today).contains(&d) {
            calendar[(d - start) as usize] += total;
        }
        if (today - HOURS_DAYS + 1..=today).contains(&d) {
            hours[weekday(d) as usize][(local.rem_euclid(DAY) / HOUR) as usize] += total;
        }
    }

    Ok(json!({
        "range": range,
        "granularity": granularity.name(),
        "buckets": buckets.keys,
        "totals": totals.to_json(),
        "previous": shift.map(|_| previous.to_json()),
        "active_days": active.len(),
        "series": series.iter().map(Tokens::to_json).collect::<Vec<_>>(),
        "by_source": stacked(&by_source),
        "by_model": stacked(&by_model),
        "models": models.iter().map(|(m, t)| json!({"model": m, "total": t})).collect::<Vec<_>>(),
        "sources": sources,
        "calendar": {"start": date(start), "totals": calendar},
        "hours": hours,
        "sync": sync,
    }))
}
