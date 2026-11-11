// Every app command, for the app's command manifest (build.rs) and the tests. A command missing
// here cannot be called at all; capabilities/main.json grants each to the app's window only.
pub const COMMANDS: &[&str] = &[
    "connection_get",
    "connection_set",
    "core_restart",
    "core_call",
    "sign_in",
    "sign_out",
    "page_open",
    "page_place",
    "page_close",
    "page_send",
    "confirmation_request",
    "confirmation_settings",
    "device_list",
    "device_registries",
    "device_install",
    "device_install_file",
    "device_enable",
    "device_remove",
    "device_rollback",
    "device_page_open",
];
