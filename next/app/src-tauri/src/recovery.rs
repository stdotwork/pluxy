//! A bounded rolling recovery budget. Time is supplied by the owner, so tests need no sleeps.
use std::collections::VecDeque;
use std::time::{Duration, Instant};

const DELAYS: [Duration; 3] = [
    Duration::from_secs(2),
    Duration::from_secs(5),
    Duration::from_secs(15),
];
const WINDOW: Duration = Duration::from_secs(300);

#[derive(Default)]
pub struct Recovery {
    generation: u64,
    armed: bool,
    attempts: VecDeque<Instant>,
    due: Option<Instant>,
    detail: Option<String>,
}

impl Recovery {
    /// An explicit action (Start, Stop, Restart, a mode change) invalidates any scheduled work
    /// and clears the budget: only automatic recoveries count against it.
    pub fn cancel(&mut self) {
        self.reset();
        self.armed = false;
        self.attempts.clear();
    }

    /// A core started. The budget stays, so a crash loop through successful restarts still stops.
    pub fn running(&mut self) {
        self.reset();
        self.armed = true;
    }

    fn reset(&mut self) {
        self.generation = self.generation.wrapping_add(1);
        self.due = None;
        self.detail = None;
    }

    pub fn schedule(&mut self, now: Instant, detail: String) {
        self.attempts.retain(|at| now.duration_since(*at) < WINDOW);
        self.due = DELAYS.get(self.attempts.len()).map(|delay| now + *delay);
        self.detail = Some(detail);
        if self.due.is_none() {
            self.armed = false;
        }
    }

    pub fn armed(&self) -> bool {
        self.armed
    }

    pub fn plan(&self) -> Option<(u64, Instant)> {
        self.due.map(|due| (self.generation, due))
    }

    /// Called only under the same connection lock as manual operations, after the wait.
    pub fn begin(&mut self, generation: u64, now: Instant) -> bool {
        if !self.armed || generation != self.generation || !self.due.is_some_and(|due| due <= now) {
            return false;
        }
        self.due = None;
        self.attempts.push_back(now);
        true
    }

    pub fn status(&self) -> Option<(&'static str, String)> {
        self.detail.as_ref().map(|detail| {
            if self.due.is_some() {
                ("recovering", format!("Waiting to restart core: {detail}"))
            } else {
                ("paused", format!("Core recovery paused after three attempts within five minutes. Start core to try again. {detail}"))
            }
        })
    }
}

/// Each timer captures a generation. The connection rechecks it under the manual-operation
/// lock after waking, so Stop, a mode change, or a newer Start always wins.
pub(crate) fn schedule(app: tauri::AppHandle, plan: Option<(u64, Instant)>) {
    let Some(mut plan) = plan else { return };
    std::thread::spawn(move || loop {
        std::thread::sleep(plan.1.saturating_duration_since(Instant::now()));
        use tauri::{Emitter, Manager};
        let st = app.state::<crate::App>();
        if st.quitting.load(std::sync::atomic::Ordering::SeqCst) {
            return;
        }
        let mut c = st.conn.lock().unwrap();
        let next = c.recover_core(plan.0);
        let view = c.view();
        drop(c);
        crate::show_status(&app, &view);
        let _ = app.emit_to(
            tauri::EventTarget::webview(crate::MAIN),
            "pluxy-connection",
            (),
        );
        match next {
            Some(next) => plan = next,
            None => return,
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delays_and_budget_survive_successful_restarts() {
        let mut r = Recovery::default();
        let mut now = Instant::now();
        r.running();
        for seconds in [2, 5, 15] {
            r.schedule(now, "crashed".into());
            let (generation, due) = r.plan().unwrap();
            assert_eq!(due.duration_since(now), Duration::from_secs(seconds));
            assert!(!r.begin(generation, due - Duration::from_millis(1)));
            assert!(r.begin(generation, due));
            now = due;
            r.running();
        }
        r.schedule(now, "loop".into());
        assert!(r.plan().is_none());
        assert_eq!(r.status().unwrap().0, "paused");
        r.running();
        r.schedule(now + WINDOW, "later".into());
        assert_eq!(r.plan().unwrap().1, now + WINDOW + Duration::from_secs(2));
    }

    #[test]
    fn an_explicit_action_clears_the_budget() {
        let mut r = Recovery::default();
        let mut now = Instant::now();
        r.running();
        for _ in 0..3 {
            r.schedule(now, "crashed".into());
            let (generation, due) = r.plan().unwrap();
            assert!(r.begin(generation, due));
            now = due;
            r.running();
        }
        // Explicit Stop then Start (or Restart, or a mode change): the next crash is retried
        // after the first delay again, not paused.
        r.cancel();
        r.running();
        r.schedule(now, "crashed after Start".into());
        assert_eq!(r.plan().unwrap().1, now + Duration::from_secs(2));
        assert_eq!(r.status().unwrap().0, "recovering");
    }

    #[test]
    fn stop_and_new_generation_win_over_scheduled_recovery() {
        let now = Instant::now();
        let mut r = Recovery::default();
        r.running();
        r.schedule(now, "crashed".into());
        let (generation, due) = r.plan().unwrap();
        r.cancel();
        assert!(!r.begin(generation, due));
        assert!(r.status().is_none());
        r.running();
        r.schedule(now, "another core crashed".into());
        assert!(!r.begin(generation, due));
        let (generation, due) = r.plan().unwrap();
        assert!(r.begin(generation, due));
        assert!(!r.begin(generation, due));
    }

    #[test]
    fn failed_attempts_spend_the_same_budget() {
        let mut now = Instant::now();
        let mut r = Recovery::default();
        r.running();
        for _ in 0..3 {
            r.schedule(now, "start failed".into());
            let (generation, due) = r.plan().unwrap();
            assert!(r.begin(generation, due));
            now = due;
        }
        r.schedule(now, "start failed".into());
        assert!(!r.armed());
        assert!(r.plan().is_none());
        assert_eq!(r.status().unwrap().0, "paused");
    }
}
