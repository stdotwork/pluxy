//! The router's rules that need no app (`route.rs` applies them): a tool's call's chain, the wait
//! for a native confirmation, and the order of a page's call against its consent's revocation.

use crate::program::{CallError, Cancel, Hop};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// The longest chain: its requester and four tools.
pub const MAX_CHAIN: usize = 5;

fn fail(code: &str, message: impl Into<String>) -> CallError {
    CallError {
        code: code.into(),
        message: message.into(),
        unknown: false,
    }
}

/// The chain of caller's call of target, for a call caller serves in chain: refused when target
/// is already in it (a call back into its own chain) or when it would pass `MAX_CHAIN`.
pub fn extend(chain: &[Hop], caller: &Hop, target: &str) -> Result<Vec<Hop>, CallError> {
    let id = caller.package().unwrap_or("main window");
    let mut chain = chain.to_vec();
    chain.push(caller.clone());
    if chain.iter().any(|h| h.package() == Some(target)) {
        let path: Vec<&str> = chain
            .iter()
            .map(|h| h.package().unwrap_or("main window"))
            .collect();
        return Err(fail(
            "permission",
            format!(
                "{id}'s call of {target} would go back into its own chain ({})",
                path.join(", ")
            ),
        ));
    }
    if chain.len() >= MAX_CHAIN {
        return Err(fail(
            "permission",
            format!("{id}'s call of {target} would make a chain longer than {MAX_CHAIN}"),
        ));
    }
    Ok(chain)
}

/// Waits, at most within, for the outcome take gives of a native confirmation. Once cancel is
/// set (the call was cancelled, its page closed or its consent revoked), withdraw takes the
/// confirmation back and the wait ends.
pub fn wait<T>(
    mut take: impl FnMut() -> Option<T>,
    cancel: &Cancel,
    within: Duration,
    withdraw: impl FnOnce(),
) -> Result<T, String> {
    let deadline = Instant::now() + within;
    loop {
        if let Some(r) = take() {
            return Ok(r);
        }
        if cancel.is_set() {
            withdraw();
            return Err("the call was cancelled while its native confirmation was open".into());
        }
        if Instant::now() > deadline {
            return Err("the native confirmation expired".into());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

struct Inflight {
    caller: String,
    label: String,
    flag: Arc<AtomicBool>,
}

/// The page calls in flight.
#[derive(Default)]
pub struct Routes {
    inflight: Mutex<Vec<Inflight>>,
}

impl Routes {
    /// Cancels package caller's page calls: its import consent was revoked.
    pub fn revoked(&self, caller: &str) {
        for i in self.inflight.lock().unwrap().iter() {
            if i.caller == caller {
                i.flag.store(true, Ordering::SeqCst);
            }
        }
    }

    /// Cancels page label's calls: it closed.
    pub fn page_closed(&self, label: &str) {
        for i in self.inflight.lock().unwrap().iter() {
            if i.label == label {
                i.flag.store(true, Ordering::SeqCst);
            }
        }
    }

    fn register(&self, caller: &str, label: &str) -> Arc<AtomicBool> {
        let flag = Arc::new(AtomicBool::new(false));
        self.inflight.lock().unwrap().push(Inflight {
            caller: caller.into(),
            label: label.into(),
            flag: Arc::clone(&flag),
        });
        flag
    }

    fn unregister(&self, flag: &Arc<AtomicBool>) {
        self.inflight
            .lock()
            .unwrap()
            .retain(|i| !Arc::ptr_eq(&i.flag, flag));
    }
}

/// Page label's call for package caller of target: grant decides it, run makes it with a cancel
/// set that a revocation of caller's consent or the page's closing sets. It is registered before
/// it is granted, so a revocation at any point after the grant reaches it: one before it runs
/// stops it, and one while it runs refuses its answer.
pub fn page_call<G, R>(
    routes: &Routes,
    caller: &str,
    label: &str,
    target: &str,
    grant: impl FnOnce() -> Result<G, CallError>,
    run: impl FnOnce(G, Cancel) -> Result<R, CallError>,
) -> Result<R, CallError> {
    let flag = routes.register(caller, label);
    let r = grant().and_then(|g| {
        if flag.load(Ordering::SeqCst) {
            return Err(fail(
                "permission",
                format!("{caller}'s call of {target} was revoked"),
            ));
        }
        run(g, Cancel::default().with(&flag))
    });
    routes.unregister(&flag);
    // Revoked, closed or changed while it ran: nothing it answered is delivered.
    if flag.load(Ordering::SeqCst) {
        return Err(fail(
            "permission",
            format!("{caller}'s call of {target} was revoked while it ran"),
        ));
    }
    r
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tool(id: &str) -> Hop {
        Hop::Package {
            id: id.into(),
            sha256: "ab".repeat(32),
            generation: 1,
        }
    }

    #[test]
    fn a_call_back_into_its_chain_or_past_the_longest_chain_is_refused() {
        let main = vec![Hop::Main];
        // The control: relay's call of the target, for the window.
        let c = extend(&main, &tool("pluxy/relay"), "pluxy/target").unwrap();
        assert_eq!(c, vec![Hop::Main, tool("pluxy/relay")]);
        // A call of itself, or of a tool earlier in the chain, at any depth.
        let e = extend(&main, &tool("pluxy/relay"), "pluxy/relay").unwrap_err();
        assert!(
            e.code == "permission" && e.message.contains("own chain"),
            "{e}"
        );
        let deep = vec![Hop::Main, tool("pluxy/a"), tool("pluxy/b")];
        let e = extend(&deep, &tool("pluxy/c"), "pluxy/a").unwrap_err();
        assert!(e.message.contains("pluxy/a, pluxy/b, pluxy/c"), "{e}");
        // A page that started the chain counts as its package.
        let page = vec![Hop::Page {
            id: "pluxy/guide".into(),
            sha256: "cd".repeat(32),
            page: "page".into(),
        }];
        assert!(extend(&page, &tool("pluxy/relay"), "pluxy/guide").is_err());
        // Four tools at most: the requester, three callers, then the fourth tool.
        let three = vec![Hop::Main, tool("pluxy/a"), tool("pluxy/b")];
        assert_eq!(
            extend(&three, &tool("pluxy/c"), "pluxy/d").unwrap().len(),
            4
        );
        let four = vec![Hop::Main, tool("pluxy/a"), tool("pluxy/b"), tool("pluxy/c")];
        let e = extend(&four, &tool("pluxy/d"), "pluxy/e").unwrap_err();
        assert!(e.message.contains("longer than 5"), "{e}");
    }

    #[test]
    fn a_cancel_ends_the_wait_and_withdraws_the_confirmation() {
        // The control: an answer that comes is taken.
        let mut polls = 0;
        let r = wait(
            || {
                polls += 1;
                (polls == 3).then_some(7)
            },
            &Cancel::default(),
            Duration::from_secs(5),
            || panic!("withdrawn without a cancel"),
        );
        assert_eq!(r, Ok(7));
        // A cancel while no answer comes ends the wait at once, withdrawing the confirmation.
        let flag = Arc::new(AtomicBool::new(false));
        let cancel = Cancel::default().with(&flag);
        let set = Arc::clone(&flag);
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(150));
            set.store(true, Ordering::SeqCst);
        });
        let withdrawn = AtomicBool::new(false);
        let started = Instant::now();
        let r: Result<(), String> = wait(
            || None,
            &cancel,
            Duration::from_secs(5),
            || withdrawn.store(true, Ordering::SeqCst),
        );
        assert!(started.elapsed() < Duration::from_secs(2), "{r:?}");
        assert!(r.unwrap_err().contains("cancelled"));
        assert!(withdrawn.load(Ordering::SeqCst));
    }

    #[test]
    fn a_revocation_right_after_the_grant_stops_the_page_call() {
        let routes = Routes::default();
        // The control: granted, it runs and answers.
        let r = page_call(
            &routes,
            "pluxy/guide",
            "page-1",
            "pluxy/target",
            || Ok(()),
            |_, _| Ok("answer"),
        );
        assert_eq!(r.unwrap(), "answer");
        // Revoked after the grant but before it ran: it does not run.
        let ran = AtomicBool::new(false);
        let r = page_call(
            &routes,
            "pluxy/guide",
            "page-1",
            "pluxy/target",
            || {
                routes.revoked("pluxy/guide");
                Ok(())
            },
            |_, _| {
                ran.store(true, Ordering::SeqCst);
                Ok("answer")
            },
        );
        assert_eq!(r.unwrap_err().code, "permission");
        assert!(!ran.load(Ordering::SeqCst), "the revoked call ran");
        // Revoked while it runs: it sees the cancel, and its answer is refused.
        let r = page_call(
            &routes,
            "pluxy/guide",
            "page-1",
            "pluxy/target",
            || Ok(()),
            |_, cancel| {
                routes.revoked("pluxy/guide");
                assert!(cancel.is_set());
                Ok("answer")
            },
        );
        assert_eq!(r.unwrap_err().code, "permission");
        // Another package's revocation is not this one's.
        let r = page_call(
            &routes,
            "pluxy/guide",
            "page-1",
            "pluxy/target",
            || Ok(()),
            |_, _| {
                routes.revoked("pluxy/other");
                Ok("answer")
            },
        );
        assert_eq!(r.unwrap(), "answer");
    }
}
