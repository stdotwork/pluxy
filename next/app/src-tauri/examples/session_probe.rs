//! The native test's probe of the desktop services a run's private session must offer (see
//! native/session.ts). It talks D-Bus through gio, because passing a file descriptor to the
//! document portal is something gdbus and busctl cannot do. Linux only; built with the
//! `native-test` feature and never part of a release.
//!
//! ```text
//! session_probe documents <file>   add the file to the document portal; prints mount, doc, name
//! session_probe watcher            register a status notifier item with the watcher and see it listed
//! session_probe portal             the portal frontend answers, with the gtk backend behind it
//! session_probe owner <name>       prints pid=<process that owns the bus name>
//! ```
//!
//! Each prints `key=value` lines and exits 0, or prints what is wrong on stderr and exits 1.

#[cfg(not(target_os = "linux"))]
fn main() {
    eprintln!("session_probe runs on Linux only");
    std::process::exit(2);
}

#[cfg(target_os = "linux")]
fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let result = match args.as_slice() {
        ["documents", file] => linux::documents(file),
        ["watcher"] => linux::watcher(),
        ["tray-pid", pid] => linux::tray_pid(pid),
        ["portal"] => linux::portal(),
        ["owner", name] => linux::owner(name),
        ["tray", pid, action] => linux::tray(pid, action),
        _ => Err(
            "usage: session_probe documents <file> | watcher | portal | owner <name> | tray <pid> <action>"
                .into(),
        ),
    };
    match result {
        Ok(lines) => lines.iter().for_each(|line| println!("{line}")),
        Err(problem) => {
            eprintln!("{problem}");
            std::process::exit(1);
        }
    }
}

#[cfg(target_os = "linux")]
mod linux {
    use std::fs::OpenOptions;
    use std::os::unix::fs::OpenOptionsExt;
    use std::path::Path;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::Arc;
    use std::time::{Duration, Instant};

    use gio::glib::variant::{Handle, ObjectPath};
    use gio::glib::{timeout_future, MainContext, ToVariant, Variant, VariantTy};
    use gio::prelude::UnixFDListExtManual;
    use gio::{
        bus_get_sync, BusType, Cancellable, DBusCallFlags, DBusConnection, DBusMessage,
        DBusMessageType, DBusNodeInfo, DBusSendMessageFlags, UnixFDList,
    };

    type Lines = Result<Vec<String>, String>;

    const TIMEOUT_MS: i32 = 10_000;
    const DOCUMENTS: &str = "org.freedesktop.portal.Documents";
    const DESKTOP: &str = "org.freedesktop.portal.Desktop";
    const GTK_BACKEND: &str = "org.freedesktop.impl.portal.desktop.gtk";
    const WATCHER: &str = "org.kde.StatusNotifierWatcher";
    const ITEM: &str = "org.kde.StatusNotifierItem";
    const ITEM_PATH: &str = "/StatusNotifierItem";

    /// The part of the status notifier item interface a watcher reads to accept an item.
    const ITEM_XML: &str = r#"<node><interface name="org.kde.StatusNotifierItem">
        <property name="Category" type="s" access="read"/>
        <property name="Id" type="s" access="read"/>
        <property name="Title" type="s" access="read"/>
        <property name="Status" type="s" access="read"/>
        <property name="IconName" type="s" access="read"/>
        <property name="Menu" type="o" access="read"/>
        <property name="ItemIsMenu" type="b" access="read"/>
    </interface></node>"#;

    fn session() -> Result<DBusConnection, String> {
        bus_get_sync(BusType::Session, Cancellable::NONE)
            .map_err(|e| format!("cannot reach the session bus: {}", e.message()))
    }

    fn reply_type(signature: &str) -> &VariantTy {
        VariantTy::new(signature).expect("a valid reply signature")
    }

    /// A method call that waits for its reply.
    fn call(
        conn: &DBusConnection,
        name: &str,
        path: &str,
        interface: &str,
        method: &str,
        params: Option<&Variant>,
        reply: &str,
    ) -> Result<Variant, String> {
        conn.call_sync(
            Some(name),
            path,
            interface,
            method,
            params,
            Some(reply_type(reply)),
            DBusCallFlags::NONE,
            TIMEOUT_MS,
            Cancellable::NONE,
        )
        .map_err(|e| format!("{name} {interface}.{method}: {}", e.message()))
    }

    fn bus_call(
        conn: &DBusConnection,
        method: &str,
        params: &Variant,
        reply: &str,
    ) -> Result<Variant, String> {
        call(
            conn,
            "org.freedesktop.DBus",
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            method,
            Some(params),
            reply,
        )
    }

    fn name_owner(conn: &DBusConnection, name: &str) -> Result<String, String> {
        let (owner,) = bus_call(conn, "GetNameOwner", &(name,).to_variant(), "(s)")?
            .get::<(String,)>()
            .ok_or("GetNameOwner answered with something else than a name")?;
        Ok(owner)
    }

    fn owner_pid(conn: &DBusConnection, name: &str) -> Result<u32, String> {
        let (pid,) = bus_call(
            conn,
            "GetConnectionUnixProcessID",
            &(name,).to_variant(),
            "(u)",
        )?
        .get::<(u32,)>()
        .ok_or("GetConnectionUnixProcessID answered with something else than a pid")?;
        Ok(pid)
    }

    pub fn owner(name: &str) -> Lines {
        let conn = session()?;
        Ok(vec![format!("pid={}", owner_pid(&conn, name)?)])
    }

    /// Adds `file` to the document portal, passing a file descriptor, and says where the portal
    /// mounts the document, which the caller reads back.
    pub fn documents(file: &str) -> Lines {
        let conn = session()?;
        let path = "/org/freedesktop/portal/documents";
        let (mount,) = call(
            &conn,
            DOCUMENTS,
            path,
            DOCUMENTS,
            "GetMountPoint",
            None,
            "(ay)",
        )?
        .get::<(Vec<u8>,)>()
        .ok_or("GetMountPoint answered with something else than a path")?;
        let mount = String::from_utf8(mount.into_iter().take_while(|b| *b != 0).collect())
            .map_err(|_| "the document portal's mount point is not UTF-8")?;

        // The portal wants an O_PATH descriptor.
        let handle = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_PATH)
            .open(file)
            .map_err(|e| format!("cannot open {file}: {e}"))?;
        let fds = UnixFDList::new();
        // The list holds its own copy of the descriptor.
        fds.append(handle)
            .map_err(|e| format!("cannot pass the file descriptor: {}", e.message()))?;
        // The message API, because gio-rs's call_with_unix_fd_list_sync panics on a reply that
        // carries no descriptors.
        let message = DBusMessage::new_method_call(Some(DOCUMENTS), path, Some(DOCUMENTS), "Add");
        message.set_body(&(Handle(0), false, false).to_variant());
        message.set_unix_fd_list(Some(&fds));
        let (reply, _) = conn
            .send_message_with_reply_sync(
                &message,
                DBusSendMessageFlags::NONE,
                TIMEOUT_MS,
                Cancellable::NONE,
            )
            .map_err(|e| format!("{DOCUMENTS}.Add: {}", e.message()))?;
        // Not to_gerror, which gio-rs asserts wrongly.
        if reply.message_type() == DBusMessageType::Error {
            let name = reply
                .error_name()
                .map(|n| n.to_string())
                .unwrap_or_default();
            let text = reply
                .body()
                .and_then(|body| body.get::<(String,)>())
                .map(|(t,)| t)
                .unwrap_or_default();
            return Err(format!("{DOCUMENTS}.Add: {name}: {text}"));
        }
        let (doc,) = reply
            .body()
            .and_then(|body| body.get::<(String,)>())
            .ok_or("Add answered with something else than a document id")?;
        let name = Path::new(file)
            .file_name()
            .and_then(|n| n.to_str())
            .ok_or("the file has no name")?;
        Ok(vec![
            format!("mount={mount}"),
            format!("doc={doc}"),
            format!("name={name}"),
        ])
    }

    /// The portal frontend answers, the gtk backend runs, and the frontend offers what that
    /// backend implements.
    pub fn portal() -> Lines {
        let conn = session()?;
        let backend = owner_pid(&conn, GTK_BACKEND)
            .map_err(|e| format!("the gtk portal backend is not running: {e}"))?;
        let (xml,) = call(
            &conn,
            DESKTOP,
            "/org/freedesktop/portal/desktop",
            "org.freedesktop.DBus.Introspectable",
            "Introspect",
            None,
            "(s)",
        )?
        .get::<(String,)>()
        .ok_or("Introspect answered with something else than XML")?;
        let node = DBusNodeInfo::for_xml(&xml)
            .map_err(|e| format!("the portal frontend's description: {}", e.message()))?;
        // The frontend offers an interface only when a backend implements it; the gtk backend
        // implements these.
        for interface in [
            "org.freedesktop.portal.FileChooser",
            "org.freedesktop.portal.Settings",
        ] {
            if node.lookup_interface(interface).is_none() {
                return Err(format!("the portal frontend does not offer {interface}"));
            }
        }
        // A call the frontend passes on to the backend.
        call(
            &conn,
            DESKTOP,
            "/org/freedesktop/portal/desktop",
            "org.freedesktop.portal.Settings",
            "ReadAll",
            Some(&(vec!["org.freedesktop.appearance"],).to_variant()),
            "(a{sa{sv}})",
        )?;
        Ok(vec![
            format!("frontend_pid={}", owner_pid(&conn, DESKTOP)?),
            format!("gtk_backend_pid={backend}"),
        ])
    }

    /// Follow the real watcher's app item to its dbusmenu, as a desktop tray host does.
    pub fn tray(pid: &str, action: &str) -> Lines {
        use std::collections::HashMap;
        let pid: u32 = pid.parse().map_err(|_| "invalid app pid")?;
        let conn = session()?;
        let entries = MainContext::default().block_on(registered_items(&conn))?;
        let mut item = None;
        for entry in entries {
            let Some((name, suffix)) = entry.split_once('/') else {
                continue;
            };
            if owner_pid(&conn, name).ok() == Some(pid) {
                item = Some((name.to_string(), format!("/{suffix}")));
                break;
            }
        }
        let (name, path) = item.ok_or("the watcher lists no item owned by the app")?;
        let (menu,) = call(
            &conn,
            &name,
            &path,
            "org.freedesktop.DBus.Properties",
            "Get",
            Some(&(ITEM, "Menu").to_variant()),
            "(v)",
        )?
        .get::<(Variant,)>()
        .ok_or("Menu is not a variant")?;
        let menu = menu
            .get::<ObjectPath>()
            .ok_or("Menu is not an object path")?;
        let interface = "com.canonical.dbusmenu";
        let layout = call(
            &conn,
            &name,
            menu.as_str(),
            interface,
            "GetLayout",
            Some(&(0i32, -1i32, Vec::<String>::new()).to_variant()),
            "(u(ia{sv}av))",
        )?;
        fn walk(node: Variant, rows: &mut Vec<(i32, String, bool)>) -> Result<(), String> {
            let (id, props, children) = node
                .get::<(i32, HashMap<String, Variant>, Vec<Variant>)>()
                .ok_or("invalid dbusmenu layout")?;
            let label = props
                .get("label")
                .and_then(|v| v.get::<String>())
                .unwrap_or_default();
            let enabled = props
                .get("enabled")
                .and_then(|v| v.get::<bool>())
                .unwrap_or(true);
            rows.push((id, label, enabled));
            for child in children {
                walk(child, rows)?;
            }
            Ok(())
        }
        let mut rows = Vec::new();
        walk(layout.child_value(1), &mut rows)?;
        let mut out = vec![format!("item={name}{path}")];
        for (_, label, enabled) in &rows {
            if !label.is_empty() {
                out.push(format!("{label}={enabled}"));
            }
        }
        if action != "inspect" {
            let (id, _, _) = rows
                .iter()
                .find(|(_, label, _)| label == action)
                .ok_or_else(|| format!("tray has no {action:?} item: {rows:?}"))?;
            // Send even if disabled: the native action must refuse foreign-process control too.
            call(
                &conn,
                &name,
                menu.as_str(),
                interface,
                "Event",
                Some(&(*id, "clicked", 0i32.to_variant(), 0u32).to_variant()),
                "()",
            )?;
            out.push(format!("clicked={action}"));
        }
        Ok(out)
    }

    async fn registered_items(conn: &DBusConnection) -> Result<Vec<String>, String> {
        let reply = conn
            .call_future(
                Some(WATCHER),
                "/StatusNotifierWatcher",
                "org.freedesktop.DBus.Properties",
                "Get",
                Some(&(WATCHER, "RegisteredStatusNotifierItems").to_variant()),
                Some(reply_type("(v)")),
                DBusCallFlags::NONE,
                TIMEOUT_MS,
            )
            .await
            .map_err(|e| format!("{WATCHER} RegisteredStatusNotifierItems: {}", e.message()))?;
        let (value,) = reply
            .get::<(Variant,)>()
            .ok_or("the item list is not a variant")?;
        value
            .get::<Vec<String>>()
            .ok_or_else(|| "RegisteredStatusNotifierItems is not a list of names".into())
    }

    /// Check a real application's tray item rather than this probe's fixture item.
    pub fn tray_pid(pid: &str) -> Lines {
        let pid: u32 = pid.parse().map_err(|_| "invalid app pid")?;
        let conn = session()?;
        MainContext::default().block_on(async {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                for item in registered_items(&conn).await? {
                    let name = item.split('/').next().unwrap_or("");
                    if owner_pid(&conn, name).ok() == Some(pid) {
                        return Ok(vec![format!("app_tray_pid={pid}"), format!("item={item}")]);
                    }
                }
                if Instant::now() >= deadline {
                    return Err(format!("no registered tray item belongs to app pid {pid}"));
                }
                timeout_future(Duration::from_millis(100)).await;
            }
        })
    }

    /// Owns a status notifier item, registers it with the watcher and waits until the watcher
    /// lists it. A watcher drops an item that does not answer for its properties, so the item
    /// is real: it answers.
    pub fn watcher() -> Lines {
        let conn = session()?;
        let owner =
            name_owner(&conn, WATCHER).map_err(|e| format!("{WATCHER} has no owner: {e}"))?;
        let watcher_pid = owner_pid(&conn, &owner)?;

        let reads = Arc::new(AtomicUsize::new(0));
        let node = DBusNodeInfo::for_xml(ITEM_XML).map_err(|e| e.message().to_string())?;
        let interface = node
            .lookup_interface(ITEM)
            .ok_or("the item interface is missing")?;
        let counted = Arc::clone(&reads);
        conn.register_object(
            ITEM_PATH,
            &interface,
            |_, _, _, _, _, _, call| {
                call.return_dbus_error("org.freedesktop.DBus.Error.UnknownMethod", "no methods")
            },
            move |_, _, _, _, property| {
                counted.fetch_add(1, Ordering::SeqCst);
                match property {
                    "Category" => "ApplicationStatus".to_variant(),
                    "Id" => "pluxy-native-test".to_variant(),
                    "Title" => "pluxy native test".to_variant(),
                    "Status" => "Active".to_variant(),
                    "IconName" => "application-x-executable".to_variant(),
                    "Menu" => ObjectPath::try_from("/NO_DBUSMENU")
                        .expect("an object path")
                        .to_variant(),
                    "ItemIsMenu" => false.to_variant(),
                    _ => ().to_variant(),
                }
            },
            |_, _, _, _, _, _| false,
        )
        .map_err(|e| format!("cannot export the item: {}", e.message()))?;

        let name = format!("{ITEM}-{}-1", std::process::id());
        let (granted,) = bus_call(
            &conn,
            "RequestName",
            &(name.as_str(), 4u32).to_variant(),
            "(u)",
        )?
        .get::<(u32,)>()
        .ok_or("RequestName answered with something else than a code")?;
        if granted != 1 {
            return Err(format!("cannot own {name} (code {granted})"));
        }
        let entry = format!("{name}{ITEM_PATH}");

        // The watcher reads the item's properties after registering it, so the main context must
        // run while this waits.
        MainContext::default().block_on(async {
            conn.call_future(
                Some(WATCHER),
                "/StatusNotifierWatcher",
                WATCHER,
                "RegisterStatusNotifierItem",
                Some(&(name.as_str(),).to_variant()),
                Some(VariantTy::UNIT),
                DBusCallFlags::NONE,
                TIMEOUT_MS,
            )
            .await
            .map_err(|e| format!("{WATCHER}.RegisterStatusNotifierItem: {}", e.message()))?;

            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                let items = registered_items(&conn).await?;
                if items.contains(&entry) && reads.load(Ordering::SeqCst) > 0 {
                    break;
                }
                if Instant::now() > deadline {
                    return Err(format!(
                        "registered {entry}, but the watcher lists {items:?} and read the item's properties {} times",
                        reads.load(Ordering::SeqCst)
                    ));
                }
                timeout_future(Duration::from_millis(100)).await;
            }
            // The watcher drops an item whose properties are wrong once it has read them.
            timeout_future(Duration::from_millis(500)).await;
            let items = registered_items(&conn).await?;
            if !items.contains(&entry) {
                return Err(format!("the watcher dropped {entry} after reading its properties; it lists {items:?}"));
            }
            Ok(vec![
                format!("watcher_pid={watcher_pid}"),
                format!("item={entry}"),
                format!("property_reads={}", reads.load(Ordering::SeqCst)),
            ])
        })
    }
}
