//! Native dialogs on Windows (`dialog.rs` says what they show): modeless Win32 dialogs from an
//! in-memory template, their controls made in `WM_INITDIALOG`, laid out in dialog units.
//!
//! Modeless, because a modal dialog runs a message loop of its own: inside a Tauri main-thread
//! task, tao would hold every later task (`event_loop/runner.rs`, events buffered while its
//! handler runs) until the dialog closed. A modeless dialog gets no keyboard handling (Tab,
//! Return, Escape, Alt+letter) from tao's loop, which never calls `IsDialogMessageW`, so a
//! `WH_GETMESSAGE` hook on this thread calls it for keys meant for an open dialog.
//!
//! A dialog with an owner is modal to it the way `DialogBox` makes it: the owner is disabled
//! while the dialog is open and enabled again before it is destroyed.
use crate::dialog::{After, Controls, Id, Spec};
use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;
use windows::core::{w, PCWSTR};
use windows::Win32::Foundation::{HWND, LPARAM, LRESULT, RECT, WPARAM};
use windows::Win32::Graphics::Gdi::{
    CreateFontIndirectW, DeleteObject, GetObjectW, HFONT, HGDIOBJ, LOGFONTW,
};
use windows::Win32::System::LibraryLoader::GetModuleHandleW;
use windows::Win32::System::Threading::GetCurrentThreadId;
use windows::Win32::UI::Controls::SetScrollInfo;
use windows::Win32::UI::Input::KeyboardAndMouse::{EnableWindow, IsWindowEnabled, SetFocus};
use windows::Win32::UI::WindowsAndMessaging::*;
use zeroize::Zeroizing;

const TIMER_LIFETIME: usize = 1;
const TIMER_WATCH: usize = 2;
const TIMER_DELAY: usize = 3;
/// Control IDs: bottom buttons from here, row buttons from `ROW`.
const BUTTON: i32 = 1000;
const ROW: i32 = 2000;
const CHECK: i32 = 3000;
const SECRET: i32 = 3001;
/// The tallest a dialog's client area gets, in dialog units, before it scrolls.
const MAX_HEIGHT: i32 = 330;

/// One open dialog's state, kept on the main thread only.
struct State {
    hwnd: HWND,
    serial: u64,
    owner: Option<HWND>,
    spec_on_button: Option<crate::dialog::OnButton>,
    watch: Option<Box<dyn FnMut() -> bool>>,
    on_shown: Option<Box<dyn FnOnce(bool)>>,
    cancel: Option<Id>,
    delay: Option<Id>,
    focus: Option<Id>,
    /// (control ID, button id, window).
    buttons: Vec<(i32, Id, HWND)>,
    check: Option<HWND>,
    secret: Option<HWND>,
    layout: Layout,
    mono: Option<HFONT>,
    done: bool,
    /// Windows is destroying the dialog (its owner went, say): `finish` does not destroy it again.
    destroying: bool,
    shown: bool,
    scroll: i32,
    /// The layout and texts, until `WM_INITDIALOG` makes the controls.
    spec: Option<Spec>,
}

thread_local! {
    static OPEN: RefCell<HashMap<isize, Rc<RefCell<State>>>> = RefCell::new(HashMap::new());
    /// The state `WM_INITDIALOG` adopts (set just before the dialog is created).
    static CREATING: RefCell<Option<Rc<RefCell<State>>>> = const { RefCell::new(None) };
    static HOOK: RefCell<Option<HHOOK>> = const { RefCell::new(None) };
    static SERIAL: std::cell::Cell<u64> = const { std::cell::Cell::new(0) };
}

/// An open dialog: its window, and the number that tells it from a later window given the same
/// handle once this one is gone.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) struct Handle {
    pub hwnd: HWND,
    serial: u64,
}

fn key(h: HWND) -> isize {
    h.0 as isize
}

fn get(h: HWND) -> Option<Rc<RefCell<State>>> {
    OPEN.with(|o| o.borrow().get(&key(h)).cloned())
}

fn open_dialogs() -> Vec<HWND> {
    OPEN.with(|o| o.borrow().values().map(|s| s.borrow().hwnd).collect())
}

/// text as a NUL-terminated UTF-16 string, lines ending in CR LF as edit controls need. Cleared
/// from memory when dropped (one of these texts is the rotated key), and made at its largest size
/// at once, so no smaller copy is left behind by growing it.
fn wide(text: &str) -> Zeroizing<Vec<u16>> {
    // At most two units for each byte (an LF becomes CR LF), and the NUL.
    let mut v: Zeroizing<Vec<u16>> = Zeroizing::new(Vec::with_capacity(2 * text.len() + 1));
    let mut prev_cr = false;
    for u in text.encode_utf16() {
        if u == u16::from(b'\n') && !prev_cr {
            v.push(u16::from(b'\r'));
        }
        prev_cr = u == u16::from(b'\r');
        v.push(u);
    }
    v.push(0);
    v
}

/// Where each part goes, in dialog units.
#[derive(Default, Clone)]
struct Layout {
    width: i32,
    height: i32,
    text: Option<(i32, i32)>,
    review: Option<(i32, i32)>,
    value: Option<i32>,
    secret: Option<i32>,
    check: Option<i32>,
    rows: Vec<(i32, i32)>,
    buttons: i32,
}

const MARGIN: i32 = 7;
const LINE: i32 = 9;

/// Lines text takes at width dialog units (a character is about 4 units wide), at least one.
fn lines(text: &str, width: i32) -> i32 {
    let per = (width / 4).max(1) as usize;
    text.split('\n')
        .map(|l| (l.chars().count().max(1)).div_ceil(per) as i32)
        .sum::<i32>()
        .max(1)
}

fn layout(spec: &Spec) -> Layout {
    let wide_kind = spec.review.is_some() || !spec.rows.is_empty();
    let width = if wide_kind { 380 } else { 260 };
    let inner = width - 2 * MARGIN;
    let mut l = Layout {
        width,
        ..Default::default()
    };
    let mut y = MARGIN;
    if let Some(t) = &spec.text {
        let h = lines(t, inner) * LINE;
        l.text = Some((y, h));
        y += h + 6;
    }
    if spec.review.is_some() {
        let h = ((spec.area.1 / 1.6) as i32).max(150);
        l.review = Some((y, h));
        y += h + 6;
    }
    if spec.value.is_some() {
        l.value = Some(y);
        y += 14 + 6;
    }
    if spec.secret {
        l.secret = Some(y);
        y += 14 + 6;
    }
    if spec.check.is_some() {
        let h = lines(spec.check.as_deref().unwrap_or(""), inner - 12) * LINE + 2;
        l.check = Some(y);
        y += h + 6;
    }
    for _ in &spec.rows {
        l.rows.push((y, 80));
        y += 80 + 4 + 14 + 8;
    }
    l.buttons = y;
    y += 14 + MARGIN;
    l.height = y;
    // A dialog is wide enough for its bottom buttons, each as wide as its label needs.
    let w = button_widths(spec);
    let row = w.iter().sum::<i32>() + BUTTON_GAP * w.len().saturating_sub(1) as i32;
    l.width = l.width.max(row + 2 * MARGIN);
    l
}

/// The gap between the bottom buttons, in dialog units.
const BUTTON_GAP: i32 = 4;

/// The bottom buttons' widths, in dialog units.
fn button_widths(spec: &Spec) -> Vec<i32> {
    spec.buttons
        .iter()
        .map(|(_, label)| crate::confirmation::text::button_width_win(label) as i32)
        .collect()
}

/// The in-memory dialog template: no controls, a caption, the message font (Segoe UI, 9 pt).
fn template(title: &str, cx: i32, cy: i32, scroll: bool) -> Vec<u32> {
    let mut style =
        (DS_SETFONT | DS_MODALFRAME | DS_CENTER) as u32 | WS_POPUP.0 | WS_CAPTION.0 | WS_SYSMENU.0;
    if scroll {
        style |= WS_VSCROLL.0;
    }
    let mut w: Vec<u16> = vec![
        (style & 0xFFFF) as u16,
        (style >> 16) as u16,
        0,
        0, // extended style
        0, // no controls
        0,
        0, // x, y
        cx as u16,
        cy as u16,
        0, // no menu
        0, // the standard dialog class
    ];
    w.extend(title.encode_utf16());
    w.push(0);
    w.push(9);
    w.extend("Segoe UI".encode_utf16());
    w.push(0);
    if w.len() % 2 == 1 {
        w.push(0);
    }
    w.chunks(2)
        .map(|c| u32::from(c[0]) | (u32::from(c[1]) << 16))
        .collect()
}

/// Shows spec, modal to owner when one is given; returns the dialog's window. On the main thread.
pub(crate) fn show(spec: Spec, owner: Option<HWND>) -> Result<Handle, String> {
    let serial = SERIAL.with(|s| {
        s.set(s.get() + 1);
        s.get()
    });
    let l = layout(&spec);
    let height = l.height.min(MAX_HEIGHT);
    let t = template(&spec.title, l.width, height, l.height > MAX_HEIGHT);
    let state = Rc::new(RefCell::new(State {
        hwnd: HWND::default(),
        serial,
        owner,
        spec_on_button: None,
        watch: None,
        on_shown: None,
        cancel: spec.cancel,
        delay: spec.delay,
        focus: spec.focus,
        buttons: Vec::new(),
        check: None,
        secret: None,
        layout: l,
        mono: None,
        done: false,
        destroying: false,
        shown: false,
        scroll: 0,
        spec: Some(spec),
    }));
    CREATING.with(|c| *c.borrow_mut() = Some(state.clone()));
    let instance = unsafe { GetModuleHandleW(None) }.map_err(|e| e.to_string())?;
    let created = unsafe {
        CreateDialogIndirectParamW(
            Some(instance.into()),
            t.as_ptr() as *const DLGTEMPLATE,
            owner,
            Some(dialog_proc),
            LPARAM(0),
        )
    };
    CREATING.with(|c| c.borrow_mut().take());
    let hwnd = created.map_err(|e| format!("could not open the native dialog: {e}"))?;
    hook();
    if let Some(o) = owner {
        unsafe {
            let _ = EnableWindow(o, false);
        }
    }
    unsafe {
        let _ = ShowWindow(hwnd, SW_SHOW);
        let _ = SetForegroundWindow(hwnd);
    }
    let focus = {
        let s = state.borrow();
        s.focus
            .and_then(|id| s.buttons.iter().find(|b| b.1 == id).map(|b| b.2))
            .or(s.secret)
    };
    if let Some(f) = focus {
        unsafe {
            let _ = SetFocus(Some(f));
        }
    }
    Ok(Handle { hwnd, serial })
}

/// Closes dialog h without an answer (a later rotated key's window replacing an earlier one), if
/// it is still open.
pub(crate) fn close(h: Handle) {
    if get(h.hwnd).is_some_and(|s| s.borrow().serial == h.serial) {
        finish(h.hwnd);
    }
}

/// Puts text on the clipboard, marked to stay out of clipboard history, cloud clipboard sync and
/// clipboard monitors (the formats Windows documents for that), as a copied password should.
pub(crate) fn copy_concealed(text: &str) -> Result<(), String> {
    use windows::Win32::Foundation::{GlobalFree, HANDLE, HGLOBAL};
    use windows::Win32::System::DataExchange::{
        CloseClipboard, EmptyClipboard, OpenClipboard, RegisterClipboardFormatW, SetClipboardData,
    };
    use windows::Win32::System::Memory::{GlobalAlloc, GlobalLock, GlobalUnlock, GMEM_MOVEABLE};
    use windows::Win32::System::Ole::CF_UNICODETEXT;
    fn global(bytes: &[u8]) -> Result<HGLOBAL, String> {
        let h =
            unsafe { GlobalAlloc(GMEM_MOVEABLE, bytes.len().max(1)) }.map_err(|e| e.to_string())?;
        let p = unsafe { GlobalLock(h) };
        if p.is_null() {
            let _ = unsafe { GlobalFree(Some(h)) };
            return Err("could not lock clipboard memory".into());
        }
        unsafe {
            std::ptr::copy_nonoverlapping(bytes.as_ptr(), p as *mut u8, bytes.len());
            let _ = GlobalUnlock(h);
        }
        Ok(h)
    }
    fn put(format: u32, bytes: &[u8]) -> Result<(), String> {
        let h = global(bytes)?;
        if let Err(e) = unsafe { SetClipboardData(format, Some(HANDLE(h.0))) } {
            let _ = unsafe { GlobalFree(Some(h)) };
            return Err(e.to_string());
        }
        Ok(())
    }
    unsafe { OpenClipboard(None) }.map_err(|e| e.to_string())?;
    let result = (|| {
        unsafe { EmptyClipboard() }.map_err(|e| e.to_string())?;
        let units: Zeroizing<Vec<u16>> = Zeroizing::new(text.encode_utf16().chain([0]).collect());
        let bytes: Zeroizing<Vec<u8>> =
            Zeroizing::new(units.iter().flat_map(|u| u.to_le_bytes()).collect());
        put(u32::from(CF_UNICODETEXT.0), &bytes)?;
        for (name, value) in [
            (w!("ExcludeClipboardContentFromMonitorProcessing"), 0u32),
            (w!("CanIncludeInClipboardHistory"), 0),
            (w!("CanUploadToCloudClipboard"), 0),
        ] {
            let f = unsafe { RegisterClipboardFormatW(name) };
            if f != 0 {
                put(f, &value.to_le_bytes())?;
            }
        }
        Ok(())
    })();
    let _ = unsafe { CloseClipboard() };
    result
}

fn hook() {
    HOOK.with(|h| {
        if h.borrow().is_none() {
            let id = unsafe { GetCurrentThreadId() };
            if let Ok(hook) = unsafe { SetWindowsHookExW(WH_GETMESSAGE, Some(keys), None, id) } {
                *h.borrow_mut() = Some(hook);
            }
        }
    });
}

fn unhook() {
    HOOK.with(|h| {
        if let Some(hook) = h.borrow_mut().take() {
            unsafe {
                let _ = UnhookWindowsHookEx(hook);
            }
        }
    });
}

/// Gives each key message meant for an open dialog (or one of its controls) to
/// `IsDialogMessageW`, as a modal dialog's own loop would; a handled one becomes `WM_NULL`.
unsafe extern "system" fn keys(code: i32, wparam: WPARAM, lparam: LPARAM) -> LRESULT {
    if code >= 0 && wparam.0 == PM_REMOVE.0 as usize && lparam.0 != 0 {
        let msg = unsafe { &mut *(lparam.0 as *mut MSG) };
        if (WM_KEYFIRST..=WM_KEYLAST).contains(&msg.message) {
            for d in open_dialogs() {
                if msg.hwnd == d || unsafe { IsChild(d, msg.hwnd) }.as_bool() {
                    if unsafe { IsDialogMessageW(d, msg) }.as_bool() {
                        msg.message = WM_NULL;
                        msg.wParam = WPARAM(0);
                        msg.lParam = LPARAM(0);
                    }
                    break;
                }
            }
        }
    }
    unsafe { CallNextHookEx(None, code, wparam, lparam) }
}

/// Dialog units to pixels for this dialog.
fn px(hwnd: HWND, x: i32, y: i32, w: i32, h: i32) -> RECT {
    let mut r = RECT {
        left: x,
        top: y,
        right: x + w,
        bottom: y + h,
    };
    unsafe {
        let _ = MapDialogRect(hwnd, &mut r);
    }
    r
}

#[allow(clippy::too_many_arguments)]
fn child(
    parent: HWND,
    class: PCWSTR,
    text: &str,
    style: u32,
    ex: WINDOW_EX_STYLE,
    id: i32,
    r: RECT,
    font: Option<HFONT>,
) -> HWND {
    let t = wide(text);
    let h = unsafe {
        CreateWindowExW(
            ex,
            class,
            PCWSTR(t.as_ptr()),
            WINDOW_STYLE(WS_CHILD.0 | WS_VISIBLE.0 | style),
            r.left,
            r.top,
            r.right - r.left,
            r.bottom - r.top,
            Some(parent),
            Some(HMENU(id as isize as *mut core::ffi::c_void)),
            None,
            None,
        )
    }
    .unwrap_or_default();
    if let Some(f) = font {
        unsafe {
            SendMessageW(h, WM_SETFONT, Some(WPARAM(f.0 as usize)), Some(LPARAM(1)));
        }
    }
    h
}

/// Makes spec's controls in dialog hwnd.
fn build(hwnd: HWND, s: &mut State) {
    let Some(mut spec) = s.spec.take() else {
        return;
    };
    s.hwnd = hwnd;
    let font = HFONT(unsafe { SendMessageW(hwnd, WM_GETFONT, None, None) }.0 as *mut _);
    let font = (!font.is_invalid()).then_some(font);
    let l = s.layout.clone();
    let inner = l.width - 2 * MARGIN;
    if let (Some(t), Some((y, h))) = (&spec.text, l.text) {
        // SS_LEFT | SS_NOPREFIX: wrapped, and an & shown as it is.
        child(
            hwnd,
            w!("STATIC"),
            t,
            0x80,
            WINDOW_EX_STYLE(0),
            -1,
            px(hwnd, MARGIN, y, inner, h),
            font,
        );
    }
    if let (Some(t), Some((y, h))) = (&spec.review, l.review) {
        child(
            hwnd,
            w!("EDIT"),
            t,
            (ES_MULTILINE | ES_READONLY | ES_AUTOVSCROLL) as u32 | WS_VSCROLL.0 | WS_TABSTOP.0,
            WS_EX_CLIENTEDGE,
            -1,
            px(hwnd, MARGIN, y, inner, h),
            font,
        );
    }
    if let (Some(v), Some(y)) = (&spec.value, l.value) {
        // The rotated key, in the dialog font's size and a fixed-width face.
        let mut lf = LOGFONTW::default();
        if let Some(f) = font {
            unsafe {
                GetObjectW(
                    HGDIOBJ(f.0),
                    std::mem::size_of::<LOGFONTW>() as i32,
                    Some(&mut lf as *mut LOGFONTW as *mut core::ffi::c_void),
                );
            }
        }
        for (i, c) in "Consolas".encode_utf16().chain([0]).enumerate() {
            lf.lfFaceName[i] = c;
        }
        let mono = unsafe { CreateFontIndirectW(&lf) };
        s.mono = (!mono.is_invalid()).then_some(mono);
        child(
            hwnd,
            w!("EDIT"),
            v,
            (ES_READONLY | ES_AUTOHSCROLL) as u32 | WS_TABSTOP.0,
            WS_EX_CLIENTEDGE,
            -1,
            px(hwnd, MARGIN, y, inner, 14),
            s.mono.or(font),
        );
    }
    if let Some(y) = l.secret {
        s.secret = Some(child(
            hwnd,
            w!("EDIT"),
            "",
            (ES_PASSWORD | ES_AUTOHSCROLL) as u32 | WS_TABSTOP.0,
            WS_EX_CLIENTEDGE,
            SECRET,
            px(hwnd, MARGIN, y, inner, 14),
            font,
        ));
    }
    if let (Some(c), Some(y)) = (&spec.check, l.check) {
        let h = lines(c, inner - 12) * LINE + 2;
        s.check = Some(child(
            hwnd,
            w!("BUTTON"),
            &crate::confirmation::text::win32(c),
            (BS_AUTOCHECKBOX | BS_MULTILINE) as u32 | WS_TABSTOP.0,
            WINDOW_EX_STYLE(0),
            CHECK,
            px(hwnd, MARGIN, y, inner, h),
            font,
        ));
    }
    for (i, ((text, id, label), (y, h))) in spec.rows.iter().zip(l.rows.iter()).enumerate() {
        child(
            hwnd,
            w!("EDIT"),
            text,
            (ES_MULTILINE | ES_READONLY | ES_AUTOVSCROLL) as u32 | WS_VSCROLL.0,
            WS_EX_CLIENTEDGE,
            -1,
            px(hwnd, MARGIN, *y, inner, *h),
            font,
        );
        let ctl = ROW + i as i32;
        let b = child(
            hwnd,
            w!("BUTTON"),
            &crate::confirmation::text::win32(label),
            BS_PUSHBUTTON as u32 | WS_TABSTOP.0,
            WINDOW_EX_STYLE(0),
            ctl,
            px(hwnd, MARGIN, y + h + 4, 120, 14),
            font,
        );
        s.buttons.push((ctl, *id, b));
    }
    let widths = button_widths(&spec);
    let row = widths.iter().sum::<i32>() + BUTTON_GAP * widths.len().saturating_sub(1) as i32;
    let mut x = l.width - MARGIN - row;
    for (i, (id, label)) in spec.buttons.iter().enumerate() {
        let ctl = BUTTON + i as i32;
        let default = spec.default == Some(*id);
        let style = if default {
            BS_DEFPUSHBUTTON
        } else {
            BS_PUSHBUTTON
        } as u32
            | WS_TABSTOP.0;
        let b = child(
            hwnd,
            w!("BUTTON"),
            &crate::confirmation::text::win32(label),
            style,
            WINDOW_EX_STYLE(0),
            ctl,
            px(hwnd, x, l.buttons, widths[i], 14),
            font,
        );
        if default {
            unsafe {
                SendMessageW(hwnd, DM_SETDEFID, Some(WPARAM(ctl as usize)), None);
            }
        }
        if spec.delay == Some(*id) {
            unsafe {
                let _ = EnableWindow(b, false);
            }
        }
        s.buttons.push((ctl, *id, b));
        x += widths[i] + BUTTON_GAP;
    }
    if l.height > MAX_HEIGHT {
        let total = px(hwnd, 0, 0, 0, l.height).bottom;
        let page = px(hwnd, 0, 0, 0, MAX_HEIGHT).bottom;
        let info = SCROLLINFO {
            cbSize: std::mem::size_of::<SCROLLINFO>() as u32,
            fMask: SIF_RANGE | SIF_PAGE | SIF_POS,
            nMin: 0,
            nMax: total,
            nPage: page as u32,
            nPos: 0,
            nTrackPos: 0,
        };
        unsafe {
            SetScrollInfo(hwnd, SB_VERT, &info, true);
        }
    }
    unsafe {
        if let Some(ms) = spec
            .lifetime
            .map(|d| d.as_millis().min(u32::MAX as u128) as u32)
        {
            SetTimer(Some(hwnd), TIMER_LIFETIME, ms, None);
        }
        if spec.watch.is_some() {
            SetTimer(Some(hwnd), TIMER_WATCH, 250, None);
        }
    }
    s.watch = spec.watch.take();
    s.on_shown = spec.on_shown.take();
    s.spec_on_button = Some(std::mem::replace(
        &mut spec.on_button,
        Box::new(|_, _| After::Close),
    ));
}

/// The press handler's view of a dialog.
struct Ctl(Rc<RefCell<State>>);

impl Controls for Ctl {
    fn checked(&self) -> bool {
        self.0.borrow().check.is_some_and(|c| unsafe {
            SendMessageW(c, BM_GETCHECK, None, None).0 == 1 // BST_CHECKED
        })
    }
    fn take_secret(&mut self) -> Zeroizing<String> {
        let Some(e) = self.0.borrow().secret else {
            return Zeroizing::new(String::new());
        };
        let n = unsafe { GetWindowTextLengthW(e) }.max(0) as usize;
        let mut b = Zeroizing::new(vec![0u16; n + 1]);
        let got = unsafe { GetWindowTextW(e, &mut b) }.max(0) as usize;
        let text = Zeroizing::new(String::from_utf16_lossy(&b[..got.min(n)]));
        // Over the edit's own copy first, then empty: as far as this side can clear it.
        let filler = wide(&"*".repeat(got));
        unsafe {
            let _ = SetWindowTextW(e, PCWSTR(filler.as_ptr()));
            let _ = SetWindowTextW(e, w!(""));
        }
        text
    }
    fn set_button(&mut self, id: Id, label: &str, enabled: bool) {
        let b = self
            .0
            .borrow()
            .buttons
            .iter()
            .find(|b| b.1 == id)
            .map(|b| b.2);
        if let Some(b) = b {
            let t = wide(&crate::confirmation::text::win32(label));
            unsafe {
                let _ = SetWindowTextW(b, PCWSTR(t.as_ptr()));
                let _ = EnableWindow(b, enabled);
            }
        }
    }
}

/// Presses button id of dialog hwnd: its handler runs, and the dialog closes if it says so.
fn press(hwnd: HWND, id: Id) {
    let Some(st) = get(hwnd) else {
        return;
    };
    let handler = {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.spec_on_button.take()
    };
    let Some(mut handler) = handler else {
        return;
    };
    let after = handler(&mut Ctl(st.clone()), id);
    st.borrow_mut().spec_on_button = Some(handler);
    if after == After::Close {
        finish(hwnd);
    }
}

/// The cancel answer, or closing when the dialog has none.
fn cancel(hwnd: HWND) {
    let id = get(hwnd).and_then(|s| s.borrow().cancel);
    match id {
        Some(id) => press(hwnd, id),
        None => finish(hwnd),
    }
}

/// Windows destroys the dialog other than through `finish` (its owner window was destroyed, which
/// takes the windows it owns with it): the cancel answer, so whoever waits on the dialog hears
/// it, and the dialog's state, timers, font and hook go as `finish` makes them go.
fn destroyed(hwnd: HWND) {
    let Some(st) = get(hwnd) else {
        return;
    };
    {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.destroying = true;
    }
    cancel(hwnd);
    // The cancel answer may keep a dialog open; this one is going all the same.
    finish(hwnd);
}

fn finish(hwnd: HWND) {
    let Some(st) = get(hwnd) else {
        return;
    };
    let (owner, mono, secret, destroying) = {
        let mut s = st.borrow_mut();
        if s.done {
            return;
        }
        s.done = true;
        (s.owner, s.mono.take(), s.secret, s.destroying)
    };
    unsafe {
        let _ = KillTimer(Some(hwnd), TIMER_LIFETIME);
        let _ = KillTimer(Some(hwnd), TIMER_WATCH);
        let _ = KillTimer(Some(hwnd), TIMER_DELAY);
        // Whatever the entry still holds, before the control goes.
        if let Some(e) = secret {
            let _ = SetWindowTextW(e, w!(""));
        }
        if let Some(o) = owner {
            let _ = EnableWindow(o, true);
        }
        if !destroying {
            let _ = DestroyWindow(hwnd);
        }
        if let Some(m) = mono {
            let _ = DeleteObject(HGDIOBJ(m.0));
        }
    }
    let empty = OPEN.with(|o| {
        let mut o = o.borrow_mut();
        o.remove(&key(hwnd));
        o.is_empty()
    });
    if empty {
        unhook();
    }
}

fn scroll_to(hwnd: HWND, pos: i32) {
    let Some(st) = get(hwnd) else {
        return;
    };
    let mut info = SCROLLINFO {
        cbSize: std::mem::size_of::<SCROLLINFO>() as u32,
        fMask: SIF_ALL,
        ..Default::default()
    };
    unsafe {
        let _ = GetScrollInfo(hwnd, SB_VERT, &mut info);
    }
    let max = (info.nMax - info.nPage as i32 + 1).max(0);
    let pos = pos.clamp(0, max);
    let old = st.borrow().scroll;
    if pos == old {
        return;
    }
    st.borrow_mut().scroll = pos;
    info.fMask = SIF_POS;
    info.nPos = pos;
    unsafe {
        SetScrollInfo(hwnd, SB_VERT, &info, true);
        let _ = ScrollWindowEx(
            hwnd,
            0,
            old - pos,
            None,
            None,
            None,
            None,
            SW_SCROLLCHILDREN | SW_INVALIDATE | SW_ERASE,
        );
    }
}

unsafe extern "system" fn dialog_proc(
    hwnd: HWND,
    msg: u32,
    wparam: WPARAM,
    _lparam: LPARAM,
) -> isize {
    match msg {
        WM_INITDIALOG => {
            if let Some(st) = CREATING.with(|c| c.borrow_mut().take()) {
                OPEN.with(|o| o.borrow_mut().insert(key(hwnd), st.clone()));
                build(hwnd, &mut st.borrow_mut());
            }
            // The focus is set once the dialog shows.
            0
        }
        WM_SHOWWINDOW if wparam.0 != 0 => {
            if let Some(st) = get(hwnd) {
                let first = !std::mem::replace(&mut st.borrow_mut().shown, true);
                if first {
                    let delayed = {
                        let s = st.borrow();
                        s.delay
                            .and_then(|id| s.buttons.iter().find(|b| b.1 == id).map(|b| b.2))
                    };
                    let off = delayed.is_some_and(|b| !unsafe { IsWindowEnabled(b) }.as_bool());
                    if delayed.is_some() {
                        unsafe {
                            SetTimer(Some(hwnd), TIMER_DELAY, 1000, None);
                        }
                    }
                    let shown = st.borrow_mut().on_shown.take();
                    if let Some(f) = shown {
                        f(off);
                    }
                }
            }
            0
        }
        WM_TIMER => {
            match wparam.0 {
                TIMER_LIFETIME => cancel(hwnd),
                TIMER_DELAY => {
                    unsafe {
                        let _ = KillTimer(Some(hwnd), TIMER_DELAY);
                    }
                    if let Some(st) = get(hwnd) {
                        let s = st.borrow();
                        if let Some(b) = s
                            .delay
                            .and_then(|id| s.buttons.iter().find(|b| b.1 == id).map(|b| b.2))
                        {
                            unsafe {
                                let _ = EnableWindow(b, true);
                            }
                        }
                    }
                }
                TIMER_WATCH => {
                    let watch = get(hwnd).and_then(|s| s.borrow_mut().watch.take());
                    if let Some(mut w) = watch {
                        let gone = w();
                        if let Some(st) = get(hwnd) {
                            st.borrow_mut().watch = Some(w);
                        }
                        if gone {
                            cancel(hwnd);
                        }
                    }
                }
                _ => {}
            }
            1
        }
        WM_COMMAND => {
            let ctl = (wparam.0 & 0xFFFF) as i32;
            let code = ((wparam.0 >> 16) & 0xFFFF) as u32;
            if ctl == IDCANCEL.0 {
                cancel(hwnd);
                return 1;
            }
            if code != BN_CLICKED {
                return 0;
            }
            let button = get(hwnd).and_then(|s| {
                let s = s.borrow();
                s.buttons.iter().find(|b| b.0 == ctl).map(|b| (b.1, b.2))
            });
            // A disabled button (Confirm during its delay) answers nothing, even to a click
            // message posted to the dialog rather than made on the button.
            if let Some((id, b)) = button {
                if unsafe { IsWindowEnabled(b) }.as_bool() {
                    press(hwnd, id);
                }
            }
            1
        }
        WM_CLOSE => {
            cancel(hwnd);
            1
        }
        WM_DESTROY => {
            destroyed(hwnd);
            0
        }
        WM_VSCROLL => {
            let pos = get(hwnd).map(|s| s.borrow().scroll).unwrap_or(0);
            let line = px(hwnd, 0, 0, 0, LINE).bottom;
            let page = px(hwnd, 0, 0, 0, MAX_HEIGHT - 20).bottom;
            let to = match SCROLLBAR_COMMAND((wparam.0 & 0xFFFF) as i32) {
                SB_LINEUP => pos - line,
                SB_LINEDOWN => pos + line,
                SB_PAGEUP => pos - page,
                SB_PAGEDOWN => pos + page,
                SB_THUMBTRACK | SB_THUMBPOSITION => ((wparam.0 >> 16) & 0xFFFF) as i32,
                SB_TOP => 0,
                SB_BOTTOM => i32::MAX,
                _ => pos,
            };
            scroll_to(hwnd, to);
            1
        }
        WM_MOUSEWHEEL => {
            let delta = ((wparam.0 >> 16) & 0xFFFF) as u16 as i16 as i32;
            let pos = get(hwnd).map(|s| s.borrow().scroll).unwrap_or(0);
            let line = px(hwnd, 0, 0, 0, LINE).bottom;
            scroll_to(hwnd, pos - delta / 120 * 3 * line);
            1
        }
        _ => 0,
    }
}

/// What the native test reads and does in open dialogs, on the main thread.
#[cfg(feature = "native-test")]
pub(crate) mod inspect {
    use super::*;

    /// The open, shown dialogs titled title.
    pub(crate) fn all(title: &str) -> Vec<HWND> {
        open_dialogs()
            .into_iter()
            .filter(|&d| {
                let mut b = [0u16; 512];
                let n = unsafe { GetWindowTextW(d, &mut b) }.max(0) as usize;
                String::from_utf16_lossy(&b[..n]) == title
                    && unsafe { IsWindowVisible(d) }.as_bool()
            })
            .collect()
    }

    /// Whether the button of dialog d's cancel answer has the keyboard focus.
    pub(crate) fn cancel_focused(d: HWND) -> bool {
        let Some(id) = get(d).and_then(|s| s.borrow().cancel) else {
            return false;
        };
        button(d, id).is_some_and(|b| focus() == Some(b))
    }

    /// Every text the dialog's static and edit controls show (not the hidden entry's).
    pub(crate) fn texts(d: HWND) -> Vec<String> {
        let secret = get(d).and_then(|s| s.borrow().secret);
        let mut out = Vec::new();
        let mut child = unsafe { GetWindow(d, GW_CHILD) }.ok();
        while let Some(c) = child {
            if Some(c) != secret {
                let n = unsafe { GetWindowTextLengthW(c) }.max(0) as usize;
                let mut b = vec![0u16; n + 1];
                let got = unsafe { GetWindowTextW(c, &mut b) }.max(0) as usize;
                out.push(String::from_utf16_lossy(&b[..got]).replace("\r\n", "\n"));
            }
            child = unsafe { GetWindow(c, GW_HWNDNEXT) }.ok();
        }
        out
    }

    /// Whether the dialog's entry hides what is typed (`ES_PASSWORD`).
    pub(crate) fn secret_hidden(d: HWND) -> bool {
        get(d)
            .and_then(|s| s.borrow().secret)
            .is_some_and(|e| unsafe { GetWindowLongW(e, GWL_STYLE) } & ES_PASSWORD != 0)
    }

    /// The window that has the keyboard focus on this thread.
    pub(crate) fn focus() -> Option<HWND> {
        let f = unsafe { windows::Win32::UI::Input::KeyboardAndMouse::GetFocus() };
        (!f.is_invalid()).then_some(f)
    }

    /// Button id's window.
    pub(crate) fn button(d: HWND, id: Id) -> Option<HWND> {
        get(d).and_then(|s| s.borrow().buttons.iter().find(|b| b.1 == id).map(|b| b.2))
    }

    /// The hidden entry's window.
    pub(crate) fn secret(d: HWND) -> Option<HWND> {
        get(d).and_then(|s| s.borrow().secret)
    }

    /// The clipboard's text, and whether it is marked to stay out of clipboard history.
    pub(crate) fn clipboard() -> (Option<String>, bool) {
        use windows::Win32::Foundation::HGLOBAL;
        use windows::Win32::System::DataExchange::{
            CloseClipboard, GetClipboardData, IsClipboardFormatAvailable, OpenClipboard,
            RegisterClipboardFormatW,
        };
        use windows::Win32::System::Memory::{GlobalLock, GlobalUnlock};
        use windows::Win32::System::Ole::CF_UNICODETEXT;
        if unsafe { OpenClipboard(None) }.is_err() {
            return (None, false);
        }
        let text = unsafe { GetClipboardData(u32::from(CF_UNICODETEXT.0)) }
            .ok()
            .and_then(|h| {
                let g = HGLOBAL(h.0);
                let p = unsafe { GlobalLock(g) } as *const u16;
                if p.is_null() {
                    return None;
                }
                let mut n = 0;
                while unsafe { *p.add(n) } != 0 {
                    n += 1;
                }
                let s = String::from_utf16_lossy(unsafe { std::slice::from_raw_parts(p, n) });
                let _ = unsafe { GlobalUnlock(g) };
                Some(s)
            });
        let history = unsafe { RegisterClipboardFormatW(w!("CanIncludeInClipboardHistory")) };
        let concealed = history != 0 && unsafe { IsClipboardFormatAvailable(history) }.is_ok();
        let _ = unsafe { CloseClipboard() };
        (text, concealed)
    }
}
