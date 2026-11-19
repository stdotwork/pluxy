//! The processes the app starts and still holds, and the only things it does to them: see whether
//! the one it started has exited, ask it to stop, and end it with everything it started.
//! `program.rs` (tools), `spawn.rs` (managed runtimes and detached launches) and `managed.rs` (the
//! app's own core) start their processes through what this module gives for each platform.
//!
//! Every function here takes the child the host holds, never a process ID from anywhere else: on
//! Linux and macOS an unreaped child (its process ID and its group's number are not another
//! process's while it is a zombie), on Windows its process handle and the job it was started in.
//! A process the app did not start, or no longer holds (a detached launch, a runtime an earlier
//! run of the app left), is never signalled: nothing here can name it. Nothing else in the app
//! signals or terminates a process (`owned_tests.rs` checks the sources for it).

#[cfg(unix)]
mod unix;
#[cfg(unix)]
pub(crate) use unix::*;

#[cfg(windows)]
pub(crate) mod windows;
#[cfg(windows)]
pub(crate) use windows::{
    kill_group, leader, read_some, term_group, term_process, Child, ChildStderr, ChildStdin,
    ChildStdout, Pin, Successor,
};

#[cfg(test)]
#[path = "../owned_tests.rs"]
mod tests;

/// The state of a process the host started, seen without reaping it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Leader {
    Running,
    /// Exited, and not reaped yet: its process ID (and on unix its group's number) is still its
    /// own, so the rest of its group can still be ended safely.
    Exited,
    /// Not this host's child to wait for any more: reaped by someone else. Unix only: on Windows
    /// the host's handle keeps its child's record.
    #[cfg_attr(windows, allow(dead_code))]
    Gone,
}
