// Prevents an extra console window on Windows in release; harmless on Linux.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    bnm_desktop_lib::run()
}
