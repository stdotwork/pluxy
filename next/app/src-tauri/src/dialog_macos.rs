//! Native dialogs on macOS (`dialog.rs` says what they show): AppKit panels made here, never an
//! `NSAlert` run modally. A modal run loop inside a Tauri main-thread task would hold every later
//! task (tao's `app_state.rs` returns early while its handler runs) until the dialog closed.
//!
//! A dialog with an owner is a sheet on it, AppKit's way to be modal to one window. Others are
//! panels of their own: a sheet needs a visible window, and sheets on one window queue, so a
//! second confirmation's lifetime would run while it cannot be seen.
use crate::dialog::{After, Controls, Id, Spec};
use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::{AnyObject, ProtocolObject};
use objc2::{define_class, msg_send, sel, DefinedClass, MainThreadMarker, MainThreadOnly};
use objc2_app_kit::{
    NSApplication, NSBackingStoreType, NSButton, NSControlStateValueOn, NSEvent,
    NSEventModifierFlags, NSEventType, NSFont, NSModalResponse, NSPanel, NSPasteboard,
    NSPasteboardTypeString, NSResponder, NSScrollView, NSSecureTextField, NSTextField, NSTextView,
    NSView, NSWindow, NSWindowDelegate, NSWindowStyleMask,
};
use objc2_foundation::{
    NSObjectProtocol, NSPoint, NSRect, NSRunLoop, NSRunLoopCommonModes, NSSize, NSString, NSTimer,
};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;
use zeroize::Zeroizing;

/// The ivars of a dialog panel: its number in `OPEN`.
pub(crate) struct Ivars {
    serial: Cell<u64>,
}

define_class!(
    // SAFETY: NSPanel has no subclassing requirements; DialogPanel does not implement Drop.
    #[unsafe(super(NSPanel, NSWindow, NSResponder, objc2_foundation::NSObject))]
    #[thread_kind = MainThreadOnly]
    #[ivars = Ivars]
    pub(crate) struct DialogPanel;

    impl DialogPanel {
        /// Every button's action: its tag is the button's id.
        #[unsafe(method(pluxyPress:))]
        fn pluxy_press(&self, sender: &NSButton) {
            let serial = self.ivars().serial.get();
            #[cfg(feature = "native-test")]
            inspect::PRESSED.with(|p| p.set(p.get() + 1));
            press(serial, sender.tag() as Id);
        }

        /// Escape, when it reaches the window through the responder chain.
        #[unsafe(method(cancelOperation:))]
        fn cancel_operation(&self, _sender: Option<&AnyObject>) {
            cancel(self.ivars().serial.get());
        }

        /// Escape as a key equivalent, before an entry or text view takes it: the cancel answer,
        /// as a Cancel button's Escape equivalent gives in an alert.
        #[unsafe(method(performKeyEquivalent:))]
        fn perform_key_equivalent(&self, event: &NSEvent) -> bool {
            let plain = !event.modifierFlags().intersects(
                NSEventModifierFlags::Command
                    | NSEventModifierFlags::Control
                    | NSEventModifierFlags::Option,
            );
            let escape = event.r#type() == NSEventType::KeyDown
                && plain
                && event
                    .charactersIgnoringModifiers()
                    .is_some_and(|c| c.to_string() == "\u{1b}");
            // No early return: the macro's method returns its last expression.
            if escape {
                cancel(self.ivars().serial.get());
                true
            } else {
                unsafe { msg_send![super(self), performKeyEquivalent: event] }
            }
        }

        #[unsafe(method(canBecomeKeyWindow))]
        fn can_become_key_window(&self) -> bool {
            true
        }
    }

    unsafe impl NSObjectProtocol for DialogPanel {}

    // The panel is its own delegate (`show`).
    unsafe impl NSWindowDelegate for DialogPanel {
        /// The close box: the cancel answer, never a plain close.
        #[unsafe(method(windowShouldClose:))]
        fn window_should_close(&self, _sender: &NSWindow) -> bool {
            cancel(self.ivars().serial.get());
            false
        }

        /// The panel closes by some other way than `finish` (the system closing it, say): the
        /// cancel answer, so whoever waits on the dialog hears it.
        #[unsafe(method(windowWillClose:))]
        fn window_will_close(&self, _notification: &AnyObject) {
            ended(self.ivars().serial.get());
        }
    }
);

define_class!(
    // SAFETY: NSView has no subclassing requirements; FlippedView does not implement Drop.
    #[unsafe(super(NSView, NSResponder, objc2_foundation::NSObject))]
    #[thread_kind = MainThreadOnly]
    struct FlippedView;

    impl FlippedView {
        #[unsafe(method(isFlipped))]
        fn is_flipped(&self) -> bool {
            true
        }
    }

    unsafe impl NSObjectProtocol for FlippedView {}
);

impl DialogPanel {
    fn new(mtm: MainThreadMarker, serial: u64, size: NSSize, closable: bool) -> Retained<Self> {
        let this = Self::alloc(mtm).set_ivars(Ivars {
            serial: Cell::new(serial),
        });
        let mut style = NSWindowStyleMask::Titled;
        if closable {
            style |= NSWindowStyleMask::Closable;
        }
        let rect = NSRect::new(NSPoint::new(0.0, 0.0), size);
        unsafe {
            msg_send![super(this), initWithContentRect: rect, styleMask: style, backing: NSBackingStoreType::Buffered, defer: false]
        }
    }
}

struct State {
    panel: Retained<DialogPanel>,
    owner: Option<Retained<NSWindow>>,
    on_button: Option<crate::dialog::OnButton>,
    watch: Option<Box<dyn FnMut() -> bool>>,
    cancel: Option<Id>,
    buttons: Vec<(Id, Retained<NSButton>)>,
    check: Option<Retained<NSButton>>,
    secret: Option<Retained<NSSecureTextField>>,
    /// Every text shown, check box and button titles included, for the native test (only there:
    /// one is the rotated key).
    #[cfg(feature = "native-test")]
    texts: Vec<String>,
    timers: Vec<Retained<NSTimer>>,
    done: bool,
    /// The panel is closing, or its sheet has ended, already: `finish` leaves it be.
    ended: bool,
}

thread_local! {
    static OPEN: RefCell<HashMap<u64, Rc<RefCell<State>>>> = RefCell::new(HashMap::new());
    static SERIAL: Cell<u64> = const { Cell::new(0) };
}

/// An open dialog: its number.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) struct Handle(u64);

fn get(serial: u64) -> Option<Rc<RefCell<State>>> {
    OPEN.with(|o| o.borrow().get(&serial).cloned())
}

fn ns(s: &str) -> Retained<NSString> {
    NSString::from_str(s)
}

const MARGIN: f64 = 20.0;
const LINE: f64 = 17.0;
const GAP: f64 = 10.0;
/// The tallest the top text gets, in points, before it scrolls.
const MAX_TEXT: f64 = 260.0;

/// The height text takes at width points (a character is about 7 points wide), at least a line.
fn height(text: &str, width: f64) -> f64 {
    let per = ((width / 7.0) as usize).max(1);
    let lines: usize = text
        .split('\n')
        .map(|l| l.chars().count().max(1).div_ceil(per))
        .sum();
    lines.max(1) as f64 * LINE + 4.0
}

fn label(mtm: MainThreadMarker, text: &str, frame: NSRect) -> Retained<NSTextField> {
    let l = NSTextField::wrappingLabelWithString(&ns(text), mtm);
    l.setSelectable(true);
    l.setFrame(frame);
    l
}

/// A read-only, selectable, scrolled text of height h at y in a view of width w.
fn scrolled(mtm: MainThreadMarker, text: &str, frame: NSRect) -> Retained<NSScrollView> {
    let scroll = NSScrollView::initWithFrame(NSScrollView::alloc(mtm), frame);
    scroll.setHasVerticalScroller(true);
    scroll.setBorderType(objc2_app_kit::NSBorderType::BezelBorder);
    let size = scroll.contentSize();
    let view = NSTextView::initWithFrame(
        NSTextView::alloc(mtm),
        NSRect::new(NSPoint::new(0.0, 0.0), size),
    );
    view.setEditable(false);
    view.setSelectable(true);
    view.setVerticallyResizable(true);
    view.setString(&ns(text));
    scroll.setDocumentView(Some(&view));
    scroll
}

fn button(
    mtm: MainThreadMarker,
    panel: &DialogPanel,
    title: &str,
    id: Id,
    frame: NSRect,
) -> Retained<NSButton> {
    let target: &AnyObject = panel.as_ref();
    let b = unsafe {
        NSButton::buttonWithTitle_target_action(
            &ns(&crate::confirmation::text::plain(title)),
            Some(target),
            Some(sel!(pluxyPress:)),
            mtm,
        )
    };
    b.setTag(id as isize);
    b.setFrame(frame);
    b
}

/// Shows spec, as a sheet on owner when one is given; on the main thread.
pub(crate) fn show(
    mtm: MainThreadMarker,
    mut spec: Spec,
    owner: Option<Retained<NSWindow>>,
) -> Result<Handle, String> {
    let serial = SERIAL.with(|s| {
        s.set(s.get() + 1);
        s.get()
    });
    let wide = spec.review.is_some() || !spec.rows.is_empty();
    // The bottom buttons, each as wide as its label needs, and the dialog wide enough for them.
    let widths: Vec<f64> = spec
        .buttons
        .iter()
        .map(|(_, title)| crate::confirmation::text::button_width_mac(title))
        .collect();
    let row = widths.iter().sum::<f64>() + 8.0 * widths.len().saturating_sub(1) as f64;
    let width = (if wide { spec.area.0.max(640.0) } else { 420.0 }).max(row + 2.0 * MARGIN);
    let inner = width - 2.0 * MARGIN;
    // Laid out top down in a flipped view.
    let mut y = MARGIN;
    let mut parts: Vec<(Retained<NSView>, bool)> = Vec::new();
    #[cfg(feature = "native-test")]
    let mut texts = Vec::new();
    let mut state_secret = None;
    let mut state_check = None;
    let mut buttons: Vec<(Id, Retained<NSButton>)> = Vec::new();
    let panel = DialogPanel::new(mtm, serial, NSSize::new(width, 100.0), owner.is_none());
    if let Some(t) = &spec.text {
        let h = height(t, inner);
        // Text taller than `MAX_TEXT` (a long confirmation or notice) scrolls in an area that
        // tall, so the buttons below it stay on the screen.
        if h > MAX_TEXT {
            let s = scrolled(
                mtm,
                t,
                NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, MAX_TEXT)),
            );
            parts.push((Retained::into_super(s), false));
        } else {
            let l = label(
                mtm,
                t,
                NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, h)),
            );
            parts.push((Retained::into_super(Retained::into_super(l)), false));
        }
        let h = h.min(MAX_TEXT);
        #[cfg(feature = "native-test")]
        texts.push(t.clone());
        y += h + GAP;
    }
    if let Some(t) = &spec.review {
        let h = spec.area.1.max(200.0);
        let s = scrolled(
            mtm,
            t,
            NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, h)),
        );
        parts.push((Retained::into_super(s), false));
        #[cfg(feature = "native-test")]
        texts.push(t.clone());
        y += h + GAP;
    }
    if let Some(v) = &spec.value {
        let l = NSTextField::labelWithString(&ns(v), mtm);
        l.setSelectable(true);
        if let Some(f) = NSFont::userFixedPitchFontOfSize(13.0) {
            l.setFont(Some(&f));
        }
        l.setFrame(NSRect::new(
            NSPoint::new(MARGIN, y),
            NSSize::new(inner, 22.0),
        ));
        parts.push((Retained::into_super(Retained::into_super(l)), false));
        #[cfg(feature = "native-test")]
        texts.push(v.to_string());
        y += 22.0 + GAP;
    }
    if spec.secret {
        let f = NSSecureTextField::initWithFrame(
            NSSecureTextField::alloc(mtm),
            NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, 24.0)),
        );
        parts.push((
            Retained::into_super(Retained::into_super(Retained::into_super(f.clone()))),
            true,
        ));
        state_secret = Some(f);
        y += 24.0 + GAP;
    }
    if let Some(c) = &spec.check {
        let target: &AnyObject = panel.as_ref();
        let h = height(c, inner - 24.0).max(20.0);
        let b = unsafe {
            NSButton::checkboxWithTitle_target_action(
                &ns(&crate::confirmation::text::plain(c)),
                Some(target),
                None,
                mtm,
            )
        };
        b.setFrame(NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, h)));
        parts.push((Retained::into_super(Retained::into_super(b.clone())), false));
        #[cfg(feature = "native-test")]
        texts.push(crate::confirmation::text::plain(c));
        state_check = Some(b);
        y += h + GAP;
    }
    if !spec.rows.is_empty() {
        // The rows scroll inside an area of their own, as in the GTK settings.
        let row_h = 120.0;
        let doc_h = spec.rows.len() as f64 * (row_h + 32.0 + 2.0 * GAP);
        let area_h = spec.area.1.max(300.0).min(doc_h.max(row_h));
        let outer = NSScrollView::initWithFrame(
            NSScrollView::alloc(mtm),
            NSRect::new(NSPoint::new(MARGIN, y), NSSize::new(inner, area_h)),
        );
        outer.setHasVerticalScroller(true);
        let doc: Retained<FlippedView> = unsafe {
            msg_send![FlippedView::alloc(mtm), initWithFrame: NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(inner - 20.0, doc_h))]
        };
        let mut ry = 0.0;
        for (text, id, title) in &spec.rows {
            let s = scrolled(
                mtm,
                text,
                NSRect::new(NSPoint::new(0.0, ry), NSSize::new(inner - 24.0, row_h)),
            );
            doc.addSubview(&s);
            #[cfg(feature = "native-test")]
            texts.push(text.clone());
            ry += row_h + GAP;
            let b = button(
                mtm,
                &panel,
                title,
                *id,
                NSRect::new(NSPoint::new(0.0, ry), NSSize::new(240.0, 32.0)),
            );
            doc.addSubview(&b);
            #[cfg(feature = "native-test")]
            texts.push(crate::confirmation::text::plain(title));
            buttons.push((*id, b));
            ry += 32.0 + GAP;
        }
        outer.setDocumentView(Some(&doc));
        parts.push((Retained::into_super(outer), false));
        y += area_h + GAP;
    }
    // The buttons, right-aligned, in their given order.
    let mut x = width - MARGIN - row;
    for ((id, title), bw) in spec.buttons.iter().zip(&widths) {
        let b = button(
            mtm,
            &panel,
            title,
            *id,
            NSRect::new(NSPoint::new(x, y), NSSize::new(*bw, 32.0)),
        );
        if spec.default == Some(*id) {
            b.setKeyEquivalent(&ns("\r"));
        }
        if spec.delay == Some(*id) {
            b.setEnabled(false);
        }
        parts.push((Retained::into_super(Retained::into_super(b.clone())), false));
        #[cfg(feature = "native-test")]
        texts.push(crate::confirmation::text::plain(title));
        buttons.push((*id, b));
        x += bw + 8.0;
    }
    if !spec.buttons.is_empty() {
        y += 32.0;
    }
    y += MARGIN;
    let content: Retained<FlippedView> = unsafe {
        msg_send![FlippedView::alloc(mtm), initWithFrame: NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(width, y))]
    };
    for (v, _) in &parts {
        content.addSubview(v);
    }
    panel.setContentSize(NSSize::new(width, y));
    panel.setContentView(Some(&content));
    panel.setTitle(&ns(&spec.title));
    unsafe { panel.setReleasedWhenClosed(false) };
    panel.setHidesOnDeactivate(false);
    let focus: Option<Retained<NSView>> = spec
        .focus
        .and_then(|id| buttons.iter().find(|b| b.0 == id))
        .map(|b| Retained::into_super(Retained::into_super(b.1.clone())))
        .or_else(|| {
            state_secret
                .clone()
                .map(|f| Retained::into_super(Retained::into_super(Retained::into_super(f))))
        });
    #[cfg(feature = "native-test")]
    inspect::remember_keys(serial, &spec);
    let state = Rc::new(RefCell::new(State {
        panel: panel.clone(),
        owner: owner.clone(),
        on_button: Some(std::mem::replace(
            &mut spec.on_button,
            Box::new(|_, _| After::Close),
        )),
        watch: spec.watch.take(),
        cancel: spec.cancel,
        buttons,
        check: state_check,
        secret: state_secret,
        #[cfg(feature = "native-test")]
        texts,
        timers: Vec::new(),
        done: false,
        ended: false,
    }));
    OPEN.with(|o| o.borrow_mut().insert(serial, state.clone()));
    panel.setDelegate(Some(ProtocolObject::from_ref(&*panel)));
    match &owner {
        // A sheet ends through `finish`, or when its window goes: then the cancel answer.
        Some(o) => {
            let done = RcBlock::new(move |_: NSModalResponse| ended(serial));
            o.beginSheet_completionHandler(&panel, Some(&done));
        }
        None => {
            // Above the app's ordinary windows, as a modal panel would be.
            panel.setLevel(8); // kCGModalPanelWindowLevel
            panel.center();
            panel.makeKeyAndOrderFront(None);
        }
    }
    #[allow(deprecated)]
    NSApplication::sharedApplication(mtm).activateIgnoringOtherApps(true);
    if let Some(f) = focus {
        panel.makeFirstResponder(Some(&f));
    }
    // Timers fire in every run loop mode AppKit uses (menus, sheets), not only the default one.
    let schedule = |secs: f64, repeats: bool, f: Box<dyn Fn()>| {
        let block = RcBlock::new(move |_t: std::ptr::NonNull<NSTimer>| f());
        let t = unsafe { NSTimer::timerWithTimeInterval_repeats_block(secs, repeats, &block) };
        unsafe { NSRunLoop::mainRunLoop().addTimer_forMode(&t, NSRunLoopCommonModes) };
        t
    };
    let mut timers = Vec::new();
    if let Some(id) = spec.delay {
        let off = state
            .borrow()
            .buttons
            .iter()
            .find(|b| b.0 == id)
            .is_some_and(|b| !b.1.isEnabled());
        if let Some(f) = spec.on_shown.take() {
            f(off);
        }
        timers.push(schedule(
            1.0,
            false,
            Box::new(move || {
                if let Some(st) = get(serial) {
                    if let Some(b) = st.borrow().buttons.iter().find(|b| b.0 == id) {
                        b.1.setEnabled(true);
                    }
                }
            }),
        ));
    } else if let Some(f) = spec.on_shown.take() {
        f(false);
    }
    if let Some(life) = spec.lifetime {
        timers.push(schedule(
            life.as_secs_f64(),
            false,
            Box::new(move || cancel(serial)),
        ));
    }
    if state.borrow().watch.is_some() {
        timers.push(schedule(
            0.25,
            true,
            Box::new(move || {
                let watch = get(serial).and_then(|s| s.borrow_mut().watch.take());
                if let Some(mut w) = watch {
                    let gone = w();
                    if let Some(st) = get(serial) {
                        st.borrow_mut().watch = Some(w);
                    }
                    if gone {
                        cancel(serial);
                    }
                }
            }),
        ));
    }
    state.borrow_mut().timers = timers;
    Ok(Handle(serial))
}

/// Closes dialog h without an answer, if it is still open.
pub(crate) fn close(h: Handle) {
    finish(h.0);
}

struct Ctl(Rc<RefCell<State>>);

impl Controls for Ctl {
    fn checked(&self) -> bool {
        self.0
            .borrow()
            .check
            .as_ref()
            .is_some_and(|c| c.state() == NSControlStateValueOn)
    }
    fn take_secret(&mut self) -> Zeroizing<String> {
        let Some(f) = self.0.borrow().secret.clone() else {
            return Zeroizing::new(String::new());
        };
        let text = Zeroizing::new(f.stringValue().to_string());
        f.setStringValue(&ns(""));
        text
    }
    fn set_button(&mut self, id: Id, label: &str, enabled: bool) {
        if let Some(b) = self.0.borrow().buttons.iter().find(|b| b.0 == id) {
            b.1.setTitle(&ns(&crate::confirmation::text::plain(label)));
            b.1.setEnabled(enabled);
        }
    }
}

fn press(serial: u64, id: Id) {
    let Some(st) = get(serial) else {
        return;
    };
    let handler = {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.on_button.take()
    };
    let Some(mut handler) = handler else {
        return;
    };
    let after = handler(&mut Ctl(st.clone()), id);
    st.borrow_mut().on_button = Some(handler);
    if after == After::Close {
        finish(serial);
    }
}

fn cancel(serial: u64) {
    let id = get(serial).and_then(|s| s.borrow().cancel);
    match id {
        Some(id) => press(serial, id),
        None => finish(serial),
    }
}

/// The panel closed, or its sheet ended, other than through `finish`: the cancel answer, so
/// whoever waits on the dialog hears it, and the dialog's state goes.
fn ended(serial: u64) {
    let Some(st) = get(serial) else {
        return;
    };
    {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.ended = true;
    }
    cancel(serial);
    // The cancel answer may keep a dialog open; this one is gone all the same.
    finish(serial);
}

fn finish(serial: u64) {
    let Some(st) = OPEN.with(|o| o.borrow_mut().remove(&serial)) else {
        return;
    };
    let (panel, owner, timers, ended) = {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.done = true;
        (
            s.panel.clone(),
            s.owner.take(),
            std::mem::take(&mut s.timers),
            s.ended,
        )
    };
    for t in timers {
        t.invalidate();
    }
    if ended {
        return;
    }
    match owner {
        Some(o) => o.endSheet(&panel),
        None => panel.orderOut(None),
    }
    panel.close();
}

/// Puts text on the general pasteboard, with the type clipboard managers take to mean it is
/// concealed (a password) and keep out of their history.
pub(crate) fn copy_concealed(text: &str) {
    let pb = NSPasteboard::generalPasteboard();
    pb.clearContents();
    unsafe {
        pb.setString_forType(&ns(text), NSPasteboardTypeString);
        pb.setString_forType(&ns(""), &ns("org.nspasteboard.ConcealedType"));
    }
}

/// What the native test reads and does in open dialogs, on the main thread.
#[cfg(feature = "native-test")]
pub(crate) mod inspect {
    use super::*;
    use objc2_app_kit::NSEventMask;

    thread_local! {
        /// How many times a dialog button's action has run.
        pub(super) static PRESSED: Cell<u64> = const { Cell::new(0) };
    }

    /// The open, shown dialogs titled title.
    pub(crate) fn all(title: &str) -> Vec<u64> {
        OPEN.with(|o| {
            o.borrow()
                .iter()
                .filter(|(_, s)| {
                    let s = s.borrow();
                    s.panel.title().to_string() == title && s.panel.isVisible()
                })
                .map(|(k, _)| *k)
                .collect()
        })
    }

    /// Clicks dialog d's close box (`performClose:`); whether d was open.
    pub(crate) fn perform_close(d: u64) -> bool {
        let Some(panel) = get(d).map(|s| s.borrow().panel.clone()) else {
            return false;
        };
        panel.performClose(None);
        true
    }

    /// How many characters dialog d's hidden entry holds.
    pub(crate) fn secret_len(d: u64) -> Option<usize> {
        let f = get(d)?.borrow().secret.clone()?;
        Some(f.stringValue().length())
    }

    /// Whether the button of dialog d's cancel answer has the keyboard focus. Without full
    /// keyboard access (the default) a button cannot take the focus on macOS; Return then goes to
    /// the default button, so the cancel answer being the default with no other button focused
    /// counts.
    pub(crate) fn cancel_focused(d: u64) -> bool {
        let Some(st) = get(d) else {
            return false;
        };
        let s = st.borrow();
        let Some(cancel) = s.cancel.and_then(|id| s.buttons.iter().find(|b| b.0 == id)) else {
            return false;
        };
        let first = s.panel.firstResponder();
        let is = |b: &NSButton| {
            first.as_ref().is_some_and(|r| {
                let r: *const NSResponder = &**r;
                let b: *const NSResponder = b.as_ref();
                std::ptr::eq(r, b)
            })
        };
        is(&cancel.1)
            || (cancel.1.keyEquivalent().to_string() == "\r" && !s.buttons.iter().any(|b| is(&b.1)))
    }

    pub(crate) fn texts(d: u64) -> Vec<String> {
        get(d).map(|s| s.borrow().texts.clone()).unwrap_or_default()
    }

    /// Whether the dialog's entry is an `NSSecureTextField` (it is made as one).
    pub(crate) fn secret_hidden(d: u64) -> bool {
        get(d).is_some_and(|s| {
            s.borrow()
                .secret
                .as_ref()
                .is_some_and(|f| f.isKindOfClass(<NSSecureTextField as objc2::ClassType>::class()))
        })
    }

    /// The clipboard's text, and whether it is marked concealed.
    pub(crate) fn clipboard() -> (Option<String>, bool) {
        let pb = NSPasteboard::generalPasteboard();
        let text = unsafe { pb.stringForType(NSPasteboardTypeString) }.map(|s| s.to_string());
        let concealed = pb
            .stringForType(&ns("org.nspasteboard.ConcealedType"))
            .is_some();
        (text, concealed)
    }

    /// Makes dialog d the key window; whether it is.
    fn key(mtm: MainThreadMarker, d: &DialogPanel) -> bool {
        #[allow(deprecated)]
        NSApplication::sharedApplication(mtm).activateIgnoringOtherApps(true);
        d.makeKeyWindow();
        d.isKeyWindow()
    }

    /// Delivers event e as AppKit delivers input: through the application when d is the key
    /// window; otherwise to d directly, a key down offered as a key equivalent first, as the
    /// application would. Returns which.
    fn deliver(mtm: MainThreadMarker, d: &DialogPanel, e: &NSEvent) -> &'static str {
        if key(mtm, d) {
            NSApplication::sharedApplication(mtm).sendEvent(e);
            "application"
        } else {
            if e.r#type() != NSEventType::KeyDown || !d.performKeyEquivalent(e) {
                d.sendEvent(e);
            }
            "window"
        }
    }

    /// Presses key code with characters (and modifiers) in dialog d: a key down, then a key up.
    pub(crate) fn key_press(
        mtm: MainThreadMarker,
        serial: u64,
        code: u16,
        chars: &str,
    ) -> Option<&'static str> {
        let d = get(serial)?.borrow().panel.clone();
        let mut how = "";
        for kind in [NSEventType::KeyDown, NSEventType::KeyUp] {
            let e = NSEvent::keyEventWithType_location_modifierFlags_timestamp_windowNumber_context_characters_charactersIgnoringModifiers_isARepeat_keyCode(
                kind,
                NSPoint::new(0.0, 0.0),
                NSEventModifierFlags::empty(),
                0.0,
                d.windowNumber(),
                None,
                &ns(chars),
                &ns(chars),
                false,
                code,
            )?;
            how = deliver(mtm, &d, &e);
        }
        Some(how)
    }

    /// Clicks button id (or the check box when id is None) of dialog d with a mouse down and up at
    /// its centre.
    pub(crate) fn click(
        mtm: MainThreadMarker,
        serial: u64,
        id: Option<Id>,
    ) -> Option<&'static str> {
        let (d, view): (Retained<DialogPanel>, Retained<NSButton>) = {
            let st = get(serial)?;
            let s = st.borrow();
            let v = match id {
                Some(id) => s.buttons.iter().find(|b| b.0 == id)?.1.clone(),
                None => s.check.clone()?,
            };
            (s.panel.clone(), v)
        };
        let f = view.bounds();
        let centre = view.convertPoint_toView(
            NSPoint::new(
                f.origin.x + f.size.width / 2.0,
                f.origin.y + f.size.height / 2.0,
            ),
            None,
        );
        let up = NSEvent::mouseEventWithType_location_modifierFlags_timestamp_windowNumber_context_eventNumber_clickCount_pressure(
            NSEventType::LeftMouseUp,
            centre,
            NSEventModifierFlags::empty(),
            0.0,
            d.windowNumber(),
            None,
            0,
            1,
            0.0,
        )?;
        let down = NSEvent::mouseEventWithType_location_modifierFlags_timestamp_windowNumber_context_eventNumber_clickCount_pressure(
            NSEventType::LeftMouseDown,
            centre,
            NSEventModifierFlags::empty(),
            0.0,
            d.windowNumber(),
            None,
            0,
            1,
            1.0,
        )?;
        let before = (PRESSED.with(Cell::get), view.state());
        // The button's tracking loop takes the mouse up from the queue.
        let app = NSApplication::sharedApplication(mtm);
        app.postEvent_atStart(&up, true);
        let way = deliver(mtm, &d, &down);
        if (PRESSED.with(Cell::get), view.state()) != before {
            return Some(way);
        }
        // The window took the mouse down without passing it to the control (as a window that is
        // not active does with a first click): the mouse up left waiting goes, and the control is
        // clicked through AppKit's own `performClick:`, which runs the same action. Counted apart.
        app.discardEventsMatchingMask_beforeEvent(NSEventMask::LeftMouseUp, None);
        // SAFETY: `performClick:` sends the control's action with the control itself as the
        // sender, which `pluxyPress:` takes as the `NSButton` it is; the check box has no action.
        unsafe { view.performClick(None) };
        Some("performClick")
    }

    /// Whether the control of dialog d with access key c (GTK form) is enabled; None when no
    /// control has that key.
    pub(crate) fn access_enabled(serial: u64, c: char) -> Option<bool> {
        let (_, id) = access_keys(serial)
            .into_iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(&c))?;
        let st = get(serial)?;
        let s = st.borrow();
        match id {
            Some(id) => s
                .buttons
                .iter()
                .find(|b| b.0 == id)
                .map(|b| b.1.isEnabled()),
            None => s.check.as_ref().map(|c| c.isEnabled()),
        }
    }

    /// The access keys (GTK form) of dialog d's buttons and check box: (key, button id or None
    /// for the check box).
    pub(crate) fn access_keys(serial: u64) -> Vec<(char, Option<Id>)> {
        let Some(st) = get(serial) else {
            return Vec::new();
        };
        let s = st.borrow();
        let mut out = Vec::new();
        for (id, _) in &s.buttons {
            if let Some(k) = KEYS.with(|k| k.borrow().get(&(serial, *id)).copied()) {
                out.push((k, Some(*id)));
            }
        }
        if let Some(k) = KEYS.with(|k| k.borrow().get(&(serial, u32::MAX)).copied()) {
            out.push((k, None));
        }
        out
    }

    thread_local! {
        pub(super) static KEYS: RefCell<HashMap<(u64, Id), char>> = RefCell::new(HashMap::new());
    }

    /// Remembers the GTK access keys of spec's buttons and check box for dialog serial.
    pub(crate) fn remember_keys(serial: u64, spec: &Spec) {
        KEYS.with(|k| {
            let mut k = k.borrow_mut();
            for (id, label) in &spec.buttons {
                if let Some(c) = crate::confirmation::text::access_key(label) {
                    k.insert((serial, *id), c);
                }
            }
            for (_, id, label) in &spec.rows {
                if let Some(c) = crate::confirmation::text::access_key(label) {
                    k.insert((serial, *id), c);
                }
            }
            if let Some(c) = spec
                .check
                .as_deref()
                .and_then(crate::confirmation::text::access_key)
            {
                k.insert((serial, u32::MAX), c);
            }
        });
    }
}
