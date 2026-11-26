// The pluxy desktop app. See lib.rs.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    pluxy_app::run();
}
