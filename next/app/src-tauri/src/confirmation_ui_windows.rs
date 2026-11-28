//! The native confirmation, its settings and the launch notice on Windows (`dialog_windows.rs`),
//! with the same texts and answers as the GTK ones (`confirmation_ui.rs`): every answer goes
//! through `respond`, so only this native handler issues a token, as on Linux.
use super::*;
use crate::dialog::{After, Spec};
use crate::dialog_windows as dw;
use std::cell::Cell;

const CANCEL: u32 = 1;
const CONFIRM: u32 = 2;
const CLOSE: u32 = 3;
/// Row buttons (each remembered launch's Revoke) from here.
const ROW: u32 = 100;

/// Always host-owned, including the result of a refused page Start. A later notice replaces the
/// one showing.
pub(crate) fn notice(app: &tauri::AppHandle, text: String) {
    thread_local! {
        static NOTICE: Cell<Option<dw::Handle>> = const { Cell::new(None) };
    }
    let _ = app.run_on_main_thread(move || {
        if let Some(earlier) = NOTICE.take() {
            dw::close(earlier);
        }
        let mut spec = Spec::new(text::NOTICE_TITLE, Box::new(|_, _| After::Close));
        spec.text = Some(text);
        match dw::show(spec, None) {
            Ok(h) => NOTICE.set(Some(h)),
            Err(e) => eprintln!("pluxy-app: {e}"),
        }
    });
}

pub(crate) fn request(app: &tauri::AppHandle, id: String) -> Result<(), String> {
    let Review {
        review,
        details,
        remember,
        summary,
        page,
        quiet,
        confirm,
    } = begin(app, &id)?;
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let (answer_app, answer_id) = (handle.clone(), id.clone());
        let failed = (handle.clone(), id.clone(), summary.clone());
        let mut spec = Spec::new(
            text::CONFIRM_TITLE,
            Box::new(move |c, button| {
                respond(
                    &answer_app,
                    &answer_id,
                    button == CONFIRM,
                    c.checked(),
                    &summary,
                    quiet,
                );
                After::Close
            }),
        );
        // What will happen in plain words first, the exact details below it.
        match details {
            Some(details) => {
                spec.text = Some(review);
                spec.review = Some(details);
            }
            None => spec.review = Some(review),
        }
        spec.check = remember;
        spec.buttons = vec![(CANCEL, text::CANCEL.into()), (CONFIRM, confirm.into())];
        spec.default = Some(CANCEL);
        spec.cancel = Some(CANCEL);
        spec.area = (640.0, 320.0);
        if page {
            spec.focus = Some(CANCEL);
            spec.delay = Some(CONFIRM);
            #[cfg(feature = "native-test")]
            {
                spec.on_shown = Some(Box::new(|off| {
                    super::native::PAGE_MAPPED_DISABLED
                        .store(off, std::sync::atomic::Ordering::SeqCst)
                }));
            }
        }
        // Closing/expiry cannot manufacture a confirm response. The engine checks its own clock.
        spec.lifetime = Some(SHOWN_LIFETIME);
        let (watch_app, watch_id) = (handle.clone(), id.clone());
        spec.watch = Some(Box::new(move || withdrawn(&watch_app, &watch_id)));
        if let Err(e) = dw::show(spec, None) {
            eprintln!("pluxy-app: {e}");
            let (app, id, summary) = failed;
            respond(&app, &id, false, false, &summary, quiet);
        }
    })
    .map_err(|e| e.to_string())
}

/// Native settings keep revocation out of page messages and DOM events.
pub(crate) fn settings(app: &tauri::AppHandle) -> Result<(), String> {
    let handle = app.clone();
    let list = remembered_list(app);
    app.run_on_main_thread(move || {
        let ids: Vec<String> = list.iter().map(|(id, _)| id.clone()).collect();
        let app = handle.clone();
        let mut spec = Spec::new(
            text::SETTINGS_TITLE,
            Box::new(move |c, button| {
                if button == CLOSE {
                    return After::Close;
                }
                let Some(approval) = ids.get((button - ROW) as usize) else {
                    return After::Stay;
                };
                match revoke_approval(&app, approval) {
                    Ok(()) => c.set_button(button, text::REVOKED, false),
                    Err(e) => c.set_button(button, &format!("{}{e}", text::COULD_NOT_REVOKE), true),
                }
                After::Stay
            }),
        );
        if list.is_empty() {
            spec.text = Some(text::NO_LAUNCHES.into());
        }
        spec.rows = list
            .into_iter()
            .enumerate()
            .map(|(i, (_, review))| {
                // Only the first has the access key, as in the GTK settings.
                let label = if i == 0 {
                    text::REVOKE.to_string()
                } else {
                    text::plain(text::REVOKE).replace('_', "__")
                };
                (review, ROW + i as u32, label)
            })
            .collect();
        spec.buttons = vec![(CLOSE, text::SETTINGS_CLOSE.into())];
        spec.default = Some(CLOSE);
        spec.cancel = Some(CLOSE);
        spec.area = (680.0, 500.0);
        if let Err(e) = dw::show(spec, None) {
            eprintln!("pluxy-app: {e}");
        }
    })
    .map_err(|e| e.to_string())
}
