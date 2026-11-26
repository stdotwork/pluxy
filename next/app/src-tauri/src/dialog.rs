//! What a native dialog on Windows or macOS shows and does, for `dialog_windows.rs` and
//! `dialog_macos.rs`: key entry, the rotated key's window, the confirmation, its settings and the
//! launch notice are each one `Spec`. (Linux builds the same dialogs with GTK directly.)
//!
//! Every dialog is shown from the main thread and returns at once; its answers arrive in
//! `Spec::on_button` on the main thread. Nothing here blocks the main thread: a nested modal loop
//! inside a `run_on_main_thread` task would hold every later task until it ended.
use std::time::Duration;
use zeroize::Zeroizing;

/// A button's or a row button's number, given by the dialog's maker.
pub(crate) type Id = u32;

/// What a button press does to its dialog.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum After {
    /// The dialog closes.
    Close,
    /// The dialog stays (Copy, Revoke).
    Stay,
}

/// What a press handler may read and change.
pub(crate) trait Controls {
    /// Whether the check box is ticked.
    fn checked(&self) -> bool;
    /// The hidden entry's text, which the entry then no longer holds.
    fn take_secret(&mut self) -> Zeroizing<String>;
    /// Gives button id the label (GTK mnemonic form) and turns it off when enabled is false.
    fn set_button(&mut self, id: Id, label: &str, enabled: bool);
}

/// Called on a button press, and with `Spec::cancel` when the dialog is closed, Escape is pressed,
/// its lifetime ends or its watch says so.
pub(crate) type OnButton = Box<dyn FnMut(&mut dyn Controls, Id) -> After>;

pub(crate) struct Spec {
    pub title: String,
    /// Text at the top, wrapped.
    pub text: Option<String>,
    /// A long text, read-only and selectable, in a scrolled area.
    pub review: Option<String>,
    /// One value, read-only, selectable, in a fixed-width font (the rotated key): cleared from
    /// memory when the dialog's spec goes.
    pub value: Option<zeroize::Zeroizing<String>>,
    /// A hidden entry, which gets the focus.
    pub secret: bool,
    /// A check box, unticked, with this label (GTK mnemonic form).
    pub check: Option<String>,
    /// Rows of a text and a button (native settings): (text, button id, button label).
    pub rows: Vec<(String, Id, String)>,
    /// The buttons at the bottom, left to right: (id, label in GTK mnemonic form).
    pub buttons: Vec<(Id, String)>,
    /// The button Return presses.
    pub default: Option<Id>,
    /// The button Escape and the window's close box press; none: they just close it.
    pub cancel: Option<Id>,
    /// The button that gets the focus (otherwise the hidden entry, or the first button).
    pub focus: Option<Id>,
    /// A button kept off until a second after the dialog shows.
    pub delay: Option<Id>,
    /// After this long, `cancel` is pressed.
    pub lifetime: Option<Duration>,
    /// Asked every 250 ms; true presses `cancel`.
    pub watch: Option<Box<dyn FnMut() -> bool>>,
    /// The smallest size of the review or rows area, in points (logical pixels at 100 %).
    pub area: (f64, f64),
    pub on_button: OnButton,
    /// Told once the dialog shows, with whether `delay`'s button was off then (native test).
    pub on_shown: Option<Box<dyn FnOnce(bool)>>,
}

impl Spec {
    pub(crate) fn new(title: &str, on_button: OnButton) -> Spec {
        Spec {
            title: title.into(),
            text: None,
            review: None,
            value: None,
            secret: false,
            check: None,
            rows: Vec::new(),
            buttons: Vec::new(),
            default: None,
            cancel: None,
            focus: None,
            delay: None,
            lifetime: None,
            watch: None,
            area: (0.0, 0.0),
            on_button,
            on_shown: None,
        }
    }
}
