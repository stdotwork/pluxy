//! Declared jobs, as the router runs them (`route.rs`; `../app-host/README.md`, "Jobs"): the
//! jobs under way, with host-issued IDs and their bounds, and each job's batches between its
//! producer's call and its consumer. Nothing here needs the app.
//!
//! A job's producer sends batches as `progress` frames of its call (`program::Feed`). At most
//! `WINDOW` may wait unacknowledged; a batch past it, one larger than `MAX_BATCH`, or one naming
//! anything but its value (a source, say) ends the job as failed, and what it had not delivered
//! is dropped. The pump (`pump`) delivers the batches in order, each only while both sides are
//! still the releases the job was granted for, acknowledges each to the producer once the
//! consumer took it, then delivers the job's one end: done (the producer's output, after every
//! batch), failed or cancelled. A consumer has at most `MAX_PER_CONSUMER` jobs at once and the
//! host `MAX_JOBS`; only a job's consumer cancels it, and a change to either package
//! (`Jobs::revoke`) cancels it.

use crate::program::{CallError, Feed};
use serde_json::{json, Value};
use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;

/// Batches a producer may have sent and not had acknowledged.
pub const WINDOW: u64 = 4;
/// The largest batch value, and the largest producer output, as JSON: its delivery, with the
/// host's envelope, stays within a call's input limit.
pub const MAX_BATCH: usize = 48 << 10;
/// Jobs one consumer may have at once.
pub const MAX_PER_CONSUMER: usize = 4;
/// Jobs the host runs at once.
pub const MAX_JOBS: usize = 16;
/// The fields a batch frame has. Any other, such as a source, is refused: the host names a
/// batch's source.
const BATCH_FIELDS: &[&str] = &["type", "id", "value"];

fn fail(code: &str, message: impl Into<String>) -> CallError {
    CallError {
        code: code.into(),
        message: message.into(),
        unknown: false,
    }
}

static IDS: AtomicU64 = AtomicU64::new(1);

#[derive(Default)]
struct State {
    /// Batches waiting for the pump, by their number.
    queue: VecDeque<(u64, Value)>,
    /// Batches received, and the last acknowledged.
    received: u64,
    acked: u64,
    /// Why the host or the consumer ended it, the first reason only.
    why: Option<CallError>,
    /// How the producer's call ended.
    done: Option<Result<Value, CallError>>,
}

type Ack = Box<dyn Fn(u64) + Send + Sync>;

/// A job under way.
pub struct Job {
    /// Host-issued: `j-<n>`.
    pub id: String,
    pub consumer: String,
    pub producer: String,
    pub kind: String,
    /// Set once it is ended: its producer's call and the delivery under way are cancelled.
    pub cancel: Arc<AtomicBool>,
    state: Mutex<State>,
    changed: Condvar,
    ack: Mutex<Option<Ack>>,
}

/// What the pump does next.
pub enum Next {
    /// Delivers batch number n with value.
    Batch(u64, Value),
    /// Delivers the end: `{"event": "done", "output"}` or `{"event": "failed" | "cancelled",
    /// "error"}`.
    End(Value),
}

impl Job {
    fn new(consumer: &str, producer: &str, kind: &str) -> Job {
        Job {
            id: format!("j-{}", IDS.fetch_add(1, Ordering::SeqCst)),
            consumer: consumer.into(),
            producer: producer.into(),
            kind: kind.into(),
            cancel: Arc::new(AtomicBool::new(false)),
            state: Mutex::new(State::default()),
            changed: Condvar::new(),
            ack: Mutex::new(None),
        }
    }

    /// Ends it for why, unless it has a reason already: its producer's call is cancelled, and
    /// what it has not delivered is dropped.
    pub fn end(&self, why: CallError) {
        let mut s = self.state.lock().unwrap();
        if s.why.is_none() {
            s.why = Some(why);
        }
        s.queue.clear();
        self.cancel.store(true, Ordering::SeqCst);
        self.changed.notify_all();
    }

    /// Its producer's call ended with r.
    pub fn finished(&self, r: Result<Value, CallError>) {
        self.state.lock().unwrap().done = Some(r);
        self.changed.notify_all();
    }

    /// The pump's next step, once there is one.
    pub fn next(&self) -> Next {
        let mut s = self.state.lock().unwrap();
        loop {
            if let Some(e) = &s.why {
                return Next::End(ending(e));
            }
            match &s.done {
                // Failed or cancelled: what it had not delivered is dropped.
                Some(Err(e)) => return Next::End(ending(e)),
                Some(Ok(out)) if s.queue.is_empty() => {
                    return Next::End(json!({"event": "done", "output": out}))
                }
                _ => {}
            }
            if let Some((n, v)) = s.queue.pop_front() {
                return Next::Batch(n, v);
            }
            s = self
                .changed
                .wait_timeout(s, Duration::from_millis(100))
                .unwrap()
                .0;
        }
    }

    /// Batch n was taken by the consumer: the producer may send one more.
    pub fn acknowledged(&self, n: u64) {
        self.state.lock().unwrap().acked = n;
        if let Some(ack) = self.ack.lock().unwrap().as_ref() {
            ack(n);
        }
    }
}

/// The end for e: cancelled when it was, failed otherwise.
fn ending(e: &CallError) -> Value {
    let event = if e.code == "cancelled" {
        "cancelled"
    } else {
        "failed"
    };
    json!({"event": event, "error": {"code": e.code, "message": e.message}})
}

impl Feed for Job {
    fn job(&self) -> Value {
        json!({"id": self.id, "kind": self.kind, "window": WINDOW})
    }

    fn batch(&self, frame: &Value) {
        if let Some(f) = frame
            .as_object()
            .and_then(|m| m.keys().find(|k| !BATCH_FIELDS.contains(&k.as_str())))
        {
            return self.end(fail(
                "permission",
                format!(
                    "{} sent a batch naming {f:?}: a batch is only its value, and the host names its source",
                    self.producer
                ),
            ));
        }
        let value = frame.get("value").cloned().unwrap_or(Value::Null);
        if serde_json::to_vec(&value).map_or(usize::MAX, |b| b.len()) > MAX_BATCH {
            return self.end(fail(
                "invalid_request",
                format!(
                    "{} sent a batch larger than {MAX_BATCH} bytes",
                    self.producer
                ),
            ));
        }
        let mut s = self.state.lock().unwrap();
        if s.why.is_some() {
            return;
        }
        if s.received - s.acked >= WINDOW {
            drop(s);
            return self.end(fail(
                "extension_protocol",
                format!(
                    "{} sent more than {WINDOW} batches that were not acknowledged",
                    self.producer
                ),
            ));
        }
        s.received += 1;
        let n = s.received;
        s.queue.push_back((n, value));
        self.changed.notify_all();
    }

    fn attach(&self, ack: Option<Ack>) {
        *self.ack.lock().unwrap() = ack;
    }
}

/// Delivers job's batches in order, each with source, through deliver (the envelope, and whether
/// it is the end), each only while valid (both sides as granted) holds; acknowledges each one
/// deliver took; then delivers its one end, while consumer_valid holds. A batch deliver did not
/// take, or a change valid sees, ends the job.
pub fn pump(
    job: &Job,
    source: &Value,
    mut deliver: impl FnMut(Value, bool) -> Result<(), CallError>,
    valid: impl Fn() -> Result<(), CallError>,
    consumer_valid: impl Fn() -> bool,
) {
    let mut batches = 0u64;
    loop {
        match job.next() {
            Next::Batch(n, value) => {
                if let Err(e) = valid() {
                    job.end(fail(
                        "cancelled",
                        format!("its registration ended: {}", e.message),
                    ));
                    continue;
                }
                let envelope = json!({"job": job.id, "kind": job.kind, "seq": n,
                    "event": "batch", "value": value, "source": source});
                match deliver(envelope, false) {
                    Ok(()) => {
                        batches += 1;
                        job.acknowledged(n);
                    }
                    Err(e) => job.end(fail(
                        &e.code,
                        format!("{} did not take batch {n}: {}", job.consumer, e.message),
                    )),
                }
            }
            Next::End(mut end) => {
                if end["event"] == "done" {
                    // Done only while both sides are as granted, and with an output that fits.
                    if let Err(e) = valid() {
                        end = ending(&fail(
                            "cancelled",
                            format!("its registration ended: {}", e.message),
                        ));
                    } else if serde_json::to_vec(&end["output"]).map_or(usize::MAX, |b| b.len())
                        > MAX_BATCH
                    {
                        end = ending(&fail(
                            "invalid_request",
                            format!(
                                "{}'s output is larger than {MAX_BATCH} bytes, which a delivery cannot carry",
                                job.producer
                            ),
                        ));
                    }
                }
                if consumer_valid() {
                    end["job"] = json!(job.id);
                    end["kind"] = json!(job.kind);
                    end["batches"] = json!(batches);
                    end["source"] = source.clone();
                    let _ = deliver(end, true);
                }
                return;
            }
        }
    }
}

/// The jobs under way.
#[derive(Default)]
pub struct Jobs {
    all: Mutex<Vec<Arc<Job>>>,
}

impl Jobs {
    /// A new job of kind, by consumer of producer, within the bounds.
    pub fn add(&self, consumer: &str, producer: &str, kind: &str) -> Result<Arc<Job>, CallError> {
        let mut all = self.all.lock().unwrap();
        if all.len() >= MAX_JOBS {
            return Err(fail(
                "unavailable",
                format!("the host runs {MAX_JOBS} jobs already"),
            ));
        }
        if all.iter().filter(|j| j.consumer == consumer).count() >= MAX_PER_CONSUMER {
            return Err(fail(
                "unavailable",
                format!("{consumer} has {MAX_PER_CONSUMER} jobs under way already"),
            ));
        }
        let job = Arc::new(Job::new(consumer, producer, kind));
        all.push(Arc::clone(&job));
        Ok(job)
    }

    /// A new job, as `add`, kept only if grant allows it: the job counts, and a revocation
    /// reaches it, from before grant decides. Grant is given the job's cancel flag, set by that
    /// revocation, for whatever it waits on (a confirmation) to stop waiting.
    pub fn start<G>(
        &self,
        consumer: &str,
        producer: &str,
        kind: &str,
        grant: impl FnOnce(&Arc<AtomicBool>) -> Result<G, CallError>,
    ) -> Result<(Arc<Job>, G), CallError> {
        let job = self.add(consumer, producer, kind)?;
        let r = grant(&job.cancel).and_then(|g| match job.state.lock().unwrap().why.clone() {
            // Revoked (or cancelled) while it was granted or confirmed.
            Some(why) => Err(fail(
                "cancelled",
                format!("it ended while it started: {}", why.message),
            )),
            None => Ok(g),
        });
        match r {
            Ok(g) => Ok((job, g)),
            Err(e) => {
                self.remove(&job);
                Err(e)
            }
        }
    }

    /// Job ended: it no longer counts.
    pub fn remove(&self, job: &Arc<Job>) {
        self.all.lock().unwrap().retain(|j| !Arc::ptr_eq(j, job));
    }

    /// Consumer cancels its job id.
    pub fn cancel(&self, consumer: &str, id: &str) -> Result<(), CallError> {
        let job = self
            .all
            .lock()
            .unwrap()
            .iter()
            .find(|j| j.id == id && j.consumer == consumer)
            .cloned()
            .ok_or_else(|| {
                fail(
                    "permission",
                    format!("{consumer} has no job {id} under way"),
                )
            })?;
        job.end(fail("cancelled", format!("{consumer} cancelled it")));
        Ok(())
    }

    /// Package changed (disabled, updated, rolled back, swapped or removed): every job it
    /// consumes or produces is cancelled, and nothing more of it is delivered.
    pub fn revoke(&self, package: &str) {
        let all: Vec<Arc<Job>> = self.all.lock().unwrap().clone();
        for j in all {
            if j.consumer == package || j.producer == package {
                j.end(fail(
                    "cancelled",
                    format!("{package} changed: its registration ended"),
                ));
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn batch(v: Value) -> Value {
        json!({"type": "progress", "id": "c-1", "value": v})
    }

    /// Runs the pump of job with deliver recording each envelope; gives them.
    fn delivered(job: &Job, valid: bool) -> Vec<Value> {
        let got = Mutex::new(Vec::new());
        pump(
            job,
            &json!({"package": "p"}),
            |e, _| {
                got.lock().unwrap().push(e);
                Ok(())
            },
            || {
                if valid {
                    Ok(())
                } else {
                    Err(fail("permission", "p changed"))
                }
            },
            || true,
        );
        got.into_inner().unwrap()
    }

    #[test]
    fn batches_are_delivered_in_order_acknowledged_then_the_end() {
        let job = Job::new("c", "p", "c/items");
        let acks = Arc::new(Mutex::new(Vec::new()));
        let a = Arc::clone(&acks);
        job.attach(Some(Box::new(move |n| a.lock().unwrap().push(n))));
        for n in 1..=3 {
            job.batch(&batch(json!({"n": n})));
        }
        job.finished(Ok(json!({"sent": 3})));
        let got = delivered(&job, true);
        assert_eq!(got.len(), 4, "{got:?}");
        for (i, e) in got[..3].iter().enumerate() {
            assert_eq!(e["seq"], i as u64 + 1);
            assert_eq!(e["value"]["n"], i as u64 + 1);
            assert_eq!(e["source"]["package"], "p");
        }
        assert_eq!(got[3]["event"], "done");
        assert_eq!(got[3]["batches"], 3);
        assert_eq!(*acks.lock().unwrap(), vec![1, 2, 3]);
    }

    #[test]
    fn a_batch_past_the_window_fails_the_job_and_nothing_more_is_delivered() {
        // The control: the window's worth is taken.
        let job = Job::new("c", "p", "c/items");
        for n in 1..=WINDOW {
            job.batch(&batch(json!({"n": n})));
        }
        assert!(!job.cancel.load(Ordering::SeqCst));
        // One more, none acknowledged: the job fails, its batches dropped.
        job.batch(&batch(json!({"n": 5})));
        assert!(job.cancel.load(Ordering::SeqCst));
        let got = delivered(&job, true);
        assert_eq!(got.len(), 1, "{got:?}");
        assert_eq!(got[0]["event"], "failed");
        assert!(got[0]["error"]["message"]
            .as_str()
            .unwrap()
            .contains("not acknowledged"));
    }

    #[test]
    fn a_batch_naming_a_source_or_too_large_fails_the_job() {
        let job = Job::new("c", "p", "c/items");
        let mut forged = batch(json!({"n": 1}));
        forged["source"] = json!({"package": "q"});
        job.batch(&forged);
        assert!(
            job.cancel.load(Ordering::SeqCst),
            "a forged batch ends its job"
        );
        let got = delivered(&job, true);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0]["error"]["code"], "permission");
        let job = Job::new("c", "p", "c/items");
        job.batch(&batch(json!({"pad": "x".repeat(MAX_BATCH)})));
        assert!(
            job.cancel.load(Ordering::SeqCst),
            "a batch too large ends its job"
        );
        let got = delivered(&job, true);
        assert_eq!(got.len(), 1);
        assert!(got[0]["error"]["message"]
            .as_str()
            .unwrap()
            .contains("larger"));
    }

    #[test]
    fn a_changed_side_cancels_the_job_before_its_next_batch() {
        let job = Job::new("c", "p", "c/items");
        job.batch(&batch(json!({"n": 1})));
        job.finished(Ok(json!({})));
        let got = delivered(&job, false);
        assert_eq!(got.len(), 1, "{got:?}");
        assert_eq!(got[0]["event"], "cancelled");
        assert_eq!(got[0]["batches"], 0);
    }

    #[test]
    fn a_done_end_needs_both_sides_and_an_output_that_fits() {
        // The producer changed after its last batch: the end is cancelled, not done.
        let job = Job::new("c", "p", "c/items");
        job.finished(Ok(json!({"sent": 0})));
        let got = delivered(&job, false);
        assert_eq!(got.len(), 1, "{got:?}");
        assert_eq!(got[0]["event"], "cancelled", "{got:?}");
        // An output too large to deliver fails the job, saying so.
        let job = Job::new("c", "p", "c/items");
        job.finished(Ok(json!({"pad": "x".repeat(MAX_BATCH)})));
        let got = delivered(&job, true);
        assert_eq!(got.len(), 1, "{got:?}");
        assert_eq!(got[0]["event"], "failed", "{got:?}");
        assert!(got[0]["error"]["message"]
            .as_str()
            .unwrap()
            .contains("larger"));
        // The control: a small output, both sides as granted, is delivered as done.
        let job = Job::new("c", "p", "c/items");
        job.finished(Ok(json!({"sent": 0})));
        assert_eq!(delivered(&job, true)[0]["event"], "done");
    }

    #[test]
    fn a_revocation_while_a_job_starts_refuses_it() {
        let jobs = Jobs::default();
        // The control: granted, it starts and counts.
        let (job, ()) = jobs.start("c", "p", "c/items", |_| Ok(())).unwrap();
        assert!(!job.cancel.load(Ordering::SeqCst));
        jobs.remove(&job);
        // Revoked while it was being granted (or confirmed): refused, and it no longer counts.
        let e = jobs
            .start("c", "p", "c/items", |_| {
                jobs.revoke("p");
                Ok(())
            })
            .err()
            .unwrap();
        assert_eq!(e.code, "cancelled", "{e:?}");
        // A grant that refuses: refused with its error, and it no longer counts.
        let e = jobs
            .start("c", "p", "c/items", |_| -> Result<(), CallError> {
                Err(fail("permission", "no"))
            })
            .err()
            .unwrap();
        assert_eq!(e.code, "permission");
        for _ in 0..MAX_PER_CONSUMER {
            jobs.add("c", "p", "c/items").unwrap();
        }
    }

    #[test]
    fn a_grant_waiting_on_a_confirmation_is_told_of_a_revocation_by_the_jobs_cancel_flag() {
        let jobs = Jobs::default();
        let e = jobs
            .start("c", "p", "c/items", |cancel| {
                assert!(!cancel.load(Ordering::SeqCst));
                // A change to the producer while the confirmation is open.
                jobs.revoke("p");
                assert!(
                    cancel.load(Ordering::SeqCst),
                    "the flag the grant waits on was not set by the revocation"
                );
                Ok(())
            })
            .err()
            .unwrap();
        assert_eq!(e.code, "cancelled", "{e:?}");
    }

    #[test]
    fn jobs_are_bounded_and_only_their_consumer_cancels_them() {
        let jobs = Jobs::default();
        let mine: Vec<_> = (0..MAX_PER_CONSUMER)
            .map(|_| jobs.add("c", "p", "c/items").unwrap())
            .collect();
        let e = jobs.add("c", "p", "c/items").err().unwrap();
        assert_eq!(e.code, "unavailable");
        // Another consumer still may.
        let other = jobs.add("d", "p", "d/items").unwrap();
        assert_ne!(mine[0].id, mine[1].id);
        assert_eq!(
            jobs.cancel("d", &mine[0].id).unwrap_err().code,
            "permission"
        );
        assert!(!mine[0].cancel.load(Ordering::SeqCst));
        jobs.cancel("c", &mine[0].id).unwrap();
        assert!(mine[0].cancel.load(Ordering::SeqCst));
        // A change to the producer cancels every job of it.
        jobs.revoke("p");
        assert!(mine.iter().all(|j| j.cancel.load(Ordering::SeqCst)));
        assert!(other.cancel.load(Ordering::SeqCst));
        jobs.remove(&mine[0]);
        assert!(jobs.add("c", "p", "c/items").is_ok());
    }
}
