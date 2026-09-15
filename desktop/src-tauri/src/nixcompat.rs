//! Nix-built GTK without a wrapper: point GLib at the schemas it needs.
//!
//! On NixOS a binary linked against `/nix/store/...-gtk+3-*/lib/libgtk-3.so`
//! only finds GTK's GSettings schemas (`org.gtk.Settings.FileChooser` and
//! friends) when `XDG_DATA_DIRS` names that store path's
//! `share/gsettings-schemas/<name>` directory. The Nix package is wrapped by
//! `wrapGAppsHook` and gets this for free; a `cargo build` or `make install`
//! binary is not, and the first file dialog aborts the process with
//! "Settings schema 'org.gtk.Settings.FileChooser' is not installed".
//!
//! Shared libraries are mapped before `main` runs, so `/proc/self/maps` already
//! says which GTK we are using. For every mapped Nix store library whose
//! package ships compiled schemas, add that directory to `XDG_DATA_DIRS` before
//! anything touches GTK. Distros with `/usr/share/glib-2.0/schemas` are left
//! alone: nothing mapped lives under `/nix/store`.

use std::collections::BTreeSet;
use std::env;
use std::fs;
use std::path::Path;

const STORE: &str = "/nix/store/";

/// Adds the schema directories of every mapped Nix-store library to
/// `XDG_DATA_DIRS`. Returns the directories it added, for logging.
pub fn fix_gsettings_env() -> Vec<String> {
    if env::var_os("BNM_NO_NIX_COMPAT").is_some() {
        return Vec::new();
    }
    let Ok(maps) = fs::read_to_string("/proc/self/maps") else {
        return Vec::new();
    };
    let found = schema_dirs_from_maps(&maps, |p| Path::new(p).is_file());
    if found.is_empty() {
        return Vec::new();
    }
    let current = env::var("XDG_DATA_DIRS").unwrap_or_default();
    let mut parts: Vec<String> = found.iter().cloned().collect();
    for p in current.split(':').filter(|p| !p.is_empty()) {
        if !parts.iter().any(|f| f == p) {
            parts.push(p.to_string());
        }
    }
    if current.is_empty() {
        parts.push("/usr/local/share".into());
        parts.push("/usr/share".into());
    }
    // Safe: called at the top of main before any thread or GTK initialisation.
    unsafe { env::set_var("XDG_DATA_DIRS", parts.join(":")) };
    found.into_iter().collect()
}

/// Pure part: `/nix/store/<hash>-<name>/lib/lib*.so` mapped paths become
/// `/nix/store/<hash>-<name>/share/gsettings-schemas/<name>` when that
/// directory holds a `glib-2.0/schemas/gschemas.compiled` (checked by `exists`).
fn schema_dirs_from_maps(maps: &str, exists: impl Fn(&str) -> bool) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for line in maps.lines() {
        let Some(path) = line.split_whitespace().nth(5) else {
            continue;
        };
        let Some(rest) = path.strip_prefix(STORE) else {
            continue;
        };
        let Some(lib_at) = rest.find("/lib/") else {
            continue;
        };
        let pkg = &rest[..lib_at]; // <hash>-<name>
        let Some((_, name)) = pkg.split_once('-') else {
            continue;
        };
        let dir = format!("{STORE}{pkg}/share/gsettings-schemas/{name}");
        if out.contains(&dir) {
            continue;
        }
        if exists(&format!("{dir}/glib-2.0/schemas/gschemas.compiled")) {
            out.insert(dir);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    const MAPS: &str = "\
7f00 r-xp 00000000 00:00 1 /nix/store/abc123-gtk+3-3.24.52/lib/libgtk-3.so.0
7f01 r-xp 00000000 00:00 2 /nix/store/abc123-gtk+3-3.24.52/lib/libgdk-3.so.0
7f02 r-xp 00000000 00:00 3 /nix/store/def456-glib-2.84/lib/libglib-2.0.so.0
7f03 r-xp 00000000 00:00 4 /usr/lib/libc.so.6
7f04 rw-p 00000000 00:00 0
";

    #[test]
    fn finds_gtk_schema_dir_once() {
        let dirs =
            schema_dirs_from_maps(MAPS, |p| p.starts_with("/nix/store/abc123-gtk+3-3.24.52/"));
        assert_eq!(
            dirs.into_iter().collect::<Vec<_>>(),
            vec![
                "/nix/store/abc123-gtk+3-3.24.52/share/gsettings-schemas/gtk+3-3.24.52".to_string()
            ]
        );
    }

    #[test]
    fn ignores_packages_without_schemas_and_non_nix_paths() {
        let dirs = schema_dirs_from_maps(MAPS, |_| false);
        assert!(dirs.is_empty());
    }
}
