//! The desktop configuration file (`$XDG_CONFIG_HOME/bnmdesktop/config.yaml`):
//! the `DesktopConfig` the frontend receives, the tolerant YAML loader (unknown or
//! malformed keys become `errors`, never a failure), the palette resolver
//! (preset -> Base16 file -> pywal `colors.json` -> `custom` keys -> `accent`),
//! the patch writer and the inotify watcher. Field names and defaults are the
//! contract in `desktop/CONTRACT.md`; the Base16 mapping is `desktop/design/DESIGN.md`.
//!
//! Everything but the watcher is pure over strings and tested below.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::sync::Mutex;
use std::time::Duration;

use notify::{RecommendedWatcher, RecursiveMode, Watcher};
use serde::{Deserialize, Serialize};
use serde_json::Value as Json;
use serde_yaml::Value as Yaml;
use tracing::{debug, info, warn};

/// The file the shell writes on first run: the contract's block, verbatim.
pub const DEFAULT_FILE: &str = r##"# bnm desktop configuration. Every key is optional; this file shows the defaults.
# The app watches this file; edits apply live. `bnm desktop` settings write here too
# (the header comment is kept, comments next to keys may be rewritten).
theme: dark            # preset id: dark | light | catppuccin-mocha | catppuccin-latte | gruvbox-dark
                       #            | nord | tokyo-night | rose-pine | dracula | one-dark | custom
accent: ""             # optional hex override of the preset's accent, e.g. "#89b4fa"
font: ""               # UI font family; "" = bundled Geist
mono_font: ""          # machine-value font; "" = bundled JetBrains Mono (a Nerd Font name works)
density: default       # compact | default | comfortable
sidebar: left          # left | hidden
start_section: overview
tray: auto             # auto | on | off
close_to_tray: true
reduced_motion: false  # true forces the no-motion variant regardless of the OS setting

custom:                # used when theme: custom; any token omitted falls back to `dark`
  base: "#141517"
  mantle: "#0f1012"
  surface: "#1c1e21"
  overlay: "#1f2124"
  text: "#e7e9ec"
  subtext: "#a0a5ad"
  muted: "#737880"
  accent: "#5fa8c8"
  accent_text: "#0b1215"
  ok: "#52b788"
  warn: "#e3b341"
  error: "#e5695f"
  vpn: "#e08a4f"
  wifi: "#6db7d6"
  border: "#26292d"
  selection: "#5fa8c826"

base16: ""             # path to a Base16 YAML scheme; when set and theme: custom, tokens derive
                       # from it using the mapping in desktop/design/DESIGN.md; `custom` keys
                       # then override individual tokens
import: ""             # path to a pywal/wallust colors.json; same precedence as base16
"##;

pub const THEMES: [&str; 11] = [
    "dark",
    "light",
    "catppuccin-mocha",
    "catppuccin-latte",
    "gruvbox-dark",
    "nord",
    "tokyo-night",
    "rose-pine",
    "dracula",
    "one-dark",
    "custom",
];
pub const DENSITIES: [&str; 3] = ["compact", "default", "comfortable"];
pub const SIDEBARS: [&str; 2] = ["left", "hidden"];
pub const TRAYS: [&str; 3] = ["auto", "on", "off"];

/// The sixteen palette tokens, in DESIGN.md order.
pub const TOKEN_NAMES: [&str; 16] = [
    "base",
    "mantle",
    "surface",
    "overlay",
    "text",
    "subtext",
    "muted",
    "accent",
    "accent_text",
    "ok",
    "warn",
    "error",
    "vpn",
    "wifi",
    "border",
    "selection",
];

/// One palette: the values of `desktop/design/presets/<id>.css`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Tokens {
    pub base: String,
    pub mantle: String,
    pub surface: String,
    pub overlay: String,
    pub text: String,
    pub subtext: String,
    pub muted: String,
    pub accent: String,
    pub accent_text: String,
    pub ok: String,
    pub warn: String,
    pub error: String,
    pub vpn: String,
    pub wifi: String,
    pub border: String,
    pub selection: String,
}

impl Default for Tokens {
    fn default() -> Self {
        Tokens::preset("dark").expect("dark preset exists")
    }
}

macro_rules! tokens {
    ($($name:literal => [$($v:literal),* $(,)?]),* $(,)?) => {
        pub fn preset(id: &str) -> Option<Tokens> {
            let vals: [&str; 16] = match id {
                $($name => [$($v),*],)*
                _ => return None,
            };
            let mut t = Tokens::empty();
            for (name, v) in TOKEN_NAMES.iter().zip(vals.iter()) {
                t.set(name, v);
            }
            Some(t)
        }
    };
}

impl Tokens {
    // base mantle surface overlay text subtext muted accent accent_text ok warn error vpn wifi border selection
    tokens! {
        "dark" => ["#141517", "#0f1012", "#1c1e21", "#1f2124", "#e7e9ec", "#a0a5ad", "#737880", "#5fa8c8", "#0b1215", "#52b788", "#e3b341", "#e5695f", "#e08a4f", "#6db7d6", "#26292d", "#5fa8c826"],
        "light" => ["#f7f7f8", "#efeff1", "#e8e8eb", "#ffffff", "#1b1c1f", "#5c6068", "#767b84", "#2b7fa6", "#ffffff", "#2e8b57", "#b7791f", "#c8433b", "#c2652a", "#2a8fbd", "#dcdde1", "#2b7fa622"],
        "catppuccin-mocha" => ["#1e1e2e", "#181825", "#313244", "#252537", "#cdd6f4", "#a6adc8", "#6c7086", "#89b4fa", "#1e1e2e", "#a6e3a1", "#f9e2af", "#f38ba8", "#fab387", "#89dceb", "#313244", "#89b4fa26"],
        "catppuccin-latte" => ["#eff1f5", "#e6e9ef", "#dce0e8", "#f8f9fc", "#4c4f69", "#6c6f85", "#9ca0b0", "#1e66f5", "#eff1f5", "#40a02b", "#df8e1d", "#d20f39", "#fe640b", "#04a5e5", "#ccd0da", "#1e66f522"],
        "gruvbox-dark" => ["#282828", "#1d2021", "#3c3836", "#32302f", "#ebdbb2", "#a89984", "#7c6f64", "#83a598", "#282828", "#b8bb26", "#fabd2f", "#fb4934", "#fe8019", "#8ec07c", "#3c3836", "#83a59826"],
        "nord" => ["#2e3440", "#272c36", "#3b4252", "#353b4a", "#eceff4", "#d8dee9", "#7b88a1", "#88c0d0", "#2e3440", "#a3be8c", "#ebcb8b", "#bf616a", "#d08770", "#81a1c1", "#3b4252", "#88c0d026"],
        "tokyo-night" => ["#1a1b26", "#16161e", "#24283b", "#1f2335", "#c0caf5", "#a9b1d6", "#565f89", "#7aa2f7", "#1a1b26", "#9ece6a", "#e0af68", "#f7768e", "#ff9e64", "#7dcfff", "#292e42", "#7aa2f726"],
        "rose-pine" => ["#191724", "#16141f", "#1f1d2e", "#26233a", "#e0def4", "#908caa", "#6e6a86", "#ebbcba", "#191724", "#9ccfd8", "#f6c177", "#eb6f92", "#31748f", "#9ccfd8", "#2a273f", "#ebbcba22"],
        "dracula" => ["#282a36", "#21222c", "#343746", "#2f313f", "#f8f8f2", "#a8abbe", "#6272a4", "#8be9fd", "#282a36", "#50fa7b", "#f1fa8c", "#ff5555", "#ffb86c", "#8be9fd", "#44475a", "#8be9fd22"],
        "one-dark" => ["#282c34", "#21252b", "#2c313a", "#2f343d", "#c8ccd4", "#9da5b4", "#5c6370", "#61afef", "#282c34", "#98c379", "#e5c07b", "#e06c75", "#d19a66", "#56b6c2", "#3a3f4b", "#61afef26"],
    }

    /// All sixteen tokens empty; the mappers fill every one.
    pub fn empty() -> Tokens {
        Tokens {
            base: String::new(),
            mantle: String::new(),
            surface: String::new(),
            overlay: String::new(),
            text: String::new(),
            subtext: String::new(),
            muted: String::new(),
            accent: String::new(),
            accent_text: String::new(),
            ok: String::new(),
            warn: String::new(),
            error: String::new(),
            vpn: String::new(),
            wifi: String::new(),
            border: String::new(),
            selection: String::new(),
        }
    }

    pub fn get(&self, name: &str) -> Option<&str> {
        Some(match name {
            "base" => &self.base,
            "mantle" => &self.mantle,
            "surface" => &self.surface,
            "overlay" => &self.overlay,
            "text" => &self.text,
            "subtext" => &self.subtext,
            "muted" => &self.muted,
            "accent" => &self.accent,
            "accent_text" => &self.accent_text,
            "ok" => &self.ok,
            "warn" => &self.warn,
            "error" => &self.error,
            "vpn" => &self.vpn,
            "wifi" => &self.wifi,
            "border" => &self.border,
            "selection" => &self.selection,
            _ => return None,
        })
    }

    /// Sets one token by name; false for an unknown name.
    pub fn set(&mut self, name: &str, value: &str) -> bool {
        let slot = match name {
            "base" => &mut self.base,
            "mantle" => &mut self.mantle,
            "surface" => &mut self.surface,
            "overlay" => &mut self.overlay,
            "text" => &mut self.text,
            "subtext" => &mut self.subtext,
            "muted" => &mut self.muted,
            "accent" => &mut self.accent,
            "accent_text" => &mut self.accent_text,
            "ok" => &mut self.ok,
            "warn" => &mut self.warn,
            "error" => &mut self.error,
            "vpn" => &mut self.vpn,
            "wifi" => &mut self.wifi,
            "border" => &mut self.border,
            "selection" => &mut self.selection,
            _ => return false,
        };
        *slot = value.to_string();
        true
    }
}

/// The effective configuration, as `config_get` returns it and `bnm://config` carries it.
/// Serialises to the snake_case JSON the frontend's `DesktopConfig` type expects.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct DesktopConfig {
    pub theme: String,
    pub accent: String,
    pub font: String,
    pub mono_font: String,
    pub density: String,
    pub sidebar: String,
    pub start_section: String,
    pub tray: String,
    pub close_to_tray: bool,
    pub reduced_motion: bool,
    /// The effective palette: always all sixteen tokens, resolved per the precedence.
    pub custom: Tokens,
    pub base16: String,
    pub import: String,
    pub source_path: String,
    pub errors: Vec<String>,
}

impl Default for DesktopConfig {
    fn default() -> Self {
        DesktopConfig {
            theme: "dark".into(),
            accent: String::new(),
            font: String::new(),
            mono_font: String::new(),
            density: "default".into(),
            sidebar: "left".into(),
            start_section: "overview".into(),
            tray: "auto".into(),
            close_to_tray: true,
            reduced_motion: false,
            custom: Tokens::default(),
            base16: String::new(),
            import: String::new(),
            source_path: String::new(),
            errors: Vec::new(),
        }
    }
}

/// What the file says, before resolution: `custom` holds only the keys present.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileConfig {
    pub theme: String,
    pub accent: String,
    pub font: String,
    pub mono_font: String,
    pub density: String,
    pub sidebar: String,
    pub start_section: String,
    pub tray: String,
    pub close_to_tray: bool,
    pub reduced_motion: bool,
    pub custom: BTreeMap<String, String>,
    pub base16: String,
    pub import: String,
    pub errors: Vec<String>,
}

impl Default for FileConfig {
    fn default() -> Self {
        let d = DesktopConfig::default();
        FileConfig {
            theme: d.theme,
            accent: d.accent,
            font: d.font,
            mono_font: d.mono_font,
            density: d.density,
            sidebar: d.sidebar,
            start_section: d.start_section,
            tray: d.tray,
            close_to_tray: d.close_to_tray,
            reduced_motion: d.reduced_motion,
            custom: BTreeMap::new(),
            base16: d.base16,
            import: d.import,
            errors: Vec::new(),
        }
    }
}

// --- colour helpers ------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Rgb(pub u8, pub u8, pub u8);

/// Parses `#rrggbb`, `rrggbb`, `#rrggbbaa` (alpha returned separately).
pub fn parse_hex(s: &str) -> Option<(Rgb, Option<u8>)> {
    let h = s.trim().trim_start_matches('#');
    if !(h.len() == 6 || h.len() == 8) || !h.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    let b = |i: usize| u8::from_str_radix(&h[i..i + 2], 16).ok();
    let rgb = Rgb(b(0)?, b(2)?, b(4)?);
    let alpha = if h.len() == 8 { Some(b(6)?) } else { None };
    Some((rgb, alpha))
}

/// Normalises a colour to lowercase `#rrggbb[aa]`, or None when it is not one.
pub fn normalize_hex(s: &str) -> Option<String> {
    let (rgb, alpha) = parse_hex(s)?;
    Some(match alpha {
        Some(a) => format!("{}{a:02x}", hex(rgb)),
        None => hex(rgb),
    })
}

pub fn hex(c: Rgb) -> String {
    format!("#{:02x}{:02x}{:02x}", c.0, c.1, c.2)
}

/// `a` moved `t` (0..1) of the way toward `b`.
pub fn mix(a: Rgb, b: Rgb, t: f64) -> Rgb {
    let m = |x: u8, y: u8| {
        ((x as f64) * (1.0 - t) + (y as f64) * t)
            .round()
            .clamp(0.0, 255.0) as u8
    };
    Rgb(m(a.0, b.0), m(a.1, b.1), m(a.2, b.2))
}

/// Relative luminance (WCAG), 0 = black, 1 = white.
pub fn luminance(c: Rgb) -> f64 {
    let lin = |v: u8| {
        let x = v as f64 / 255.0;
        if x <= 0.03928 {
            x / 12.92
        } else {
            ((x + 0.055) / 1.055).powf(2.4)
        }
    };
    0.2126 * lin(c.0) + 0.7152 * lin(c.1) + 0.0722 * lin(c.2)
}

pub fn is_dark(c: Rgb) -> bool {
    luminance(c) < 0.5
}

/// `--selection`: the accent at 15 % alpha.
pub fn selection_for(accent: &str) -> String {
    match parse_hex(accent) {
        Some((rgb, _)) => format!("{}26", hex(rgb)),
        None => accent.to_string(),
    }
}

const BLACK: Rgb = Rgb(0, 0, 0);
const WHITE: Rgb = Rgb(255, 255, 255);

// --- Base16 ----------------------------------------------------------------------------

/// Tokens from a Base16 / Tinted scheme (legacy top-level `base00:` keys or a
/// `palette:` map), mapped as DESIGN.md prescribes.
pub fn base16_tokens(yaml: &str) -> Result<Tokens, String> {
    let v: Yaml = serde_yaml::from_str(yaml).map_err(|e| format!("yaml: {e}"))?;
    let top = v.as_mapping().ok_or("scheme is not a mapping")?;
    let palette = top.get("palette").and_then(Yaml::as_mapping).unwrap_or(top);
    let slot = |name: &str| -> Result<Rgb, String> {
        let raw = palette.get(name).ok_or_else(|| format!("missing {name}"))?;
        let s = match raw {
            Yaml::String(s) => s.clone(),
            Yaml::Number(n) => n.to_string(),
            _ => return Err(format!("{name} is not a colour")),
        };
        parse_hex(&s)
            .map(|(c, _)| c)
            .ok_or_else(|| format!("{name}: bad colour {s:?}"))
    };
    let base00 = slot("base00")?;
    let base01 = slot("base01")?;
    let dark = match top.get("variant").and_then(Yaml::as_str) {
        Some("light") => false,
        Some("dark") => true,
        _ => is_dark(base00),
    };
    let accent = slot("base0D")?;
    let mut t = Tokens::empty();
    t.base = hex(base00);
    t.mantle = hex(mix(base00, BLACK, 0.03));
    t.surface = hex(base01);
    t.overlay = hex(if dark {
        mix(base00, base01, 0.5)
    } else {
        mix(base00, WHITE, 0.5)
    });
    t.text = hex(slot("base05")?);
    t.subtext = hex(slot("base04")?);
    t.muted = hex(slot("base03")?);
    t.accent = hex(accent);
    t.accent_text = hex(if dark { base00 } else { slot("base07")? });
    t.ok = hex(slot("base0B")?);
    t.warn = hex(slot("base0A")?);
    t.error = hex(slot("base08")?);
    t.vpn = hex(slot("base09")?);
    t.wifi = hex(slot("base0C")?);
    t.border = hex(slot("base02")?);
    t.selection = selection_for(&t.accent);
    Ok(t)
}

// --- pywal / wallust --------------------------------------------------------------------

/// Tokens from a pywal-style `colors.json` (`special.background/foreground`,
/// `colors.color0..15`). Surfaces are mixes of background and foreground.
pub fn pywal_tokens(json: &str) -> Result<Tokens, String> {
    let v: Json = serde_json::from_str(json).map_err(|e| format!("json: {e}"))?;
    let color = |path: &[&str]| -> Result<Rgb, String> {
        let mut cur = &v;
        for p in path {
            cur = cur
                .get(p)
                .ok_or_else(|| format!("missing {}", path.join(".")))?;
        }
        let s = cur
            .as_str()
            .ok_or_else(|| format!("{} is not a string", path.join(".")))?;
        parse_hex(s)
            .map(|(c, _)| c)
            .ok_or_else(|| format!("{}: bad colour {s:?}", path.join(".")))
    };
    let bg = color(&["special", "background"])?;
    let fg = color(&["special", "foreground"])?;
    let dark = is_dark(bg);
    let accent = color(&["colors", "color4"])?;
    let muted = color(&["colors", "color8"]).unwrap_or(mix(fg, bg, 0.5));
    let mut t = Tokens::empty();
    t.base = hex(bg);
    t.mantle = hex(mix(bg, BLACK, 0.03));
    t.surface = hex(mix(bg, fg, 0.08));
    t.overlay = hex(if dark {
        mix(bg, fg, 0.04)
    } else {
        mix(bg, WHITE, 0.5)
    });
    t.text = hex(fg);
    t.subtext = hex(mix(fg, bg, 0.25));
    t.muted = hex(muted);
    t.accent = hex(accent);
    // Text on the accent: whichever of background/foreground contrasts more with it.
    let la = luminance(accent);
    t.accent_text = hex(
        if (la - luminance(bg)).abs() >= (la - luminance(fg)).abs() {
            bg
        } else {
            fg
        },
    );
    t.ok = hex(color(&["colors", "color2"])?);
    t.warn = hex(color(&["colors", "color3"])?);
    t.error = hex(color(&["colors", "color1"])?);
    t.vpn = hex(color(&["colors", "color5"])?);
    t.wifi = hex(color(&["colors", "color6"])?);
    t.border = hex(mix(bg, fg, 0.12));
    t.selection = selection_for(&t.accent);
    Ok(t)
}

// --- loading ---------------------------------------------------------------------------

/// `~/x` and `$HOME`-less relative paths, resolved against `base_dir`.
pub fn expand_path(p: &str, base_dir: &Path) -> String {
    if p.is_empty() {
        return String::new();
    }
    if let Some(rest) = p.strip_prefix("~/") {
        return crate::daemon::home_dir().join(rest).display().to_string();
    }
    if p == "~" {
        return crate::daemon::home_dir().display().to_string();
    }
    let path = Path::new(p);
    if path.is_absolute() {
        p.to_string()
    } else {
        base_dir.join(path).display().to_string()
    }
}

fn yaml_string(v: &Yaml) -> Option<String> {
    match v {
        Yaml::String(s) => Some(s.clone()),
        Yaml::Bool(b) => Some(b.to_string()),
        Yaml::Number(n) => Some(n.to_string()),
        Yaml::Null => Some(String::new()),
        _ => None,
    }
}

/// Parses YAML text tolerantly: every problem is an entry in `errors` and the key
/// keeps its default.
pub fn parse(text: &str) -> FileConfig {
    let mut cfg = FileConfig::default();
    let value: Yaml = match serde_yaml::from_str(text) {
        Ok(v) => v,
        Err(e) => {
            cfg.errors.push(format!("yaml: {e}"));
            return cfg;
        }
    };
    let map = match &value {
        Yaml::Mapping(m) => m,
        Yaml::Null => return cfg,
        _ => {
            cfg.errors.push("top level is not a mapping".into());
            return cfg;
        }
    };
    for (k, v) in map {
        let Some(key) = k.as_str() else {
            cfg.errors.push(format!("non-string key {k:?}"));
            continue;
        };
        let mut string_field = |slot: &mut String, allowed: Option<&[&str]>| {
            let Some(s) = yaml_string(v) else {
                cfg.errors.push(format!("`{key}`: expected a string"));
                return;
            };
            if let Some(allowed) = allowed {
                if !allowed.contains(&s.as_str()) {
                    cfg.errors.push(format!(
                        "`{key}`: {s:?} is not one of {}",
                        allowed.join(" | ")
                    ));
                    return;
                }
            }
            *slot = s;
        };
        match key {
            "theme" => string_field(&mut cfg.theme, Some(&THEMES)),
            "density" => string_field(&mut cfg.density, Some(&DENSITIES)),
            "sidebar" => string_field(&mut cfg.sidebar, Some(&SIDEBARS)),
            "tray" => {
                // `tray: on` is a string in YAML 1.2, but be kind to YAML 1.1 habits.
                match v {
                    Yaml::Bool(true) => cfg.tray = "on".into(),
                    Yaml::Bool(false) => cfg.tray = "off".into(),
                    _ => string_field(&mut cfg.tray, Some(&TRAYS)),
                }
            }
            "font" => string_field(&mut cfg.font, None),
            "mono_font" => string_field(&mut cfg.mono_font, None),
            "start_section" => string_field(&mut cfg.start_section, None),
            "base16" => string_field(&mut cfg.base16, None),
            "import" => string_field(&mut cfg.import, None),
            "accent" => match yaml_string(v) {
                Some(s) if s.is_empty() => cfg.accent = s,
                Some(s) => match normalize_hex(&s) {
                    Some(h) if h.len() == 7 => cfg.accent = h,
                    _ => cfg
                        .errors
                        .push(format!("`accent`: {s:?} is not a #rrggbb colour")),
                },
                None => cfg.errors.push("`accent`: expected a string".into()),
            },
            "close_to_tray" | "reduced_motion" => match v {
                Yaml::Bool(b) => {
                    if key == "close_to_tray" {
                        cfg.close_to_tray = *b
                    } else {
                        cfg.reduced_motion = *b
                    }
                }
                _ => cfg.errors.push(format!("`{key}`: expected true or false")),
            },
            "custom" => match v {
                Yaml::Mapping(m) => {
                    for (ck, cv) in m {
                        let Some(name) = ck.as_str() else {
                            cfg.errors.push(format!("`custom`: non-string key {ck:?}"));
                            continue;
                        };
                        if !TOKEN_NAMES.contains(&name) {
                            cfg.errors.push(format!("`custom.{name}`: unknown token"));
                            continue;
                        }
                        match yaml_string(cv).and_then(|s| normalize_hex(&s)) {
                            Some(h) => {
                                cfg.custom.insert(name.to_string(), h);
                            }
                            None => cfg
                                .errors
                                .push(format!("`custom.{name}`: expected a #rrggbb[aa] colour")),
                        }
                    }
                }
                Yaml::Null => {}
                _ => cfg.errors.push("`custom`: expected a mapping".into()),
            },
            // Read-only outputs of config_get; harmless if someone pastes them back.
            "source_path" | "errors" => {}
            other => cfg.errors.push(format!("unknown key `{other}`")),
        }
    }
    cfg
}

impl FileConfig {
    /// Applies the palette precedence and expands paths. `path` is the config file,
    /// used for `source_path` and to resolve relative `base16`/`import` paths.
    pub fn resolve(&self, path: &Path) -> DesktopConfig {
        let base_dir = path.parent().unwrap_or(Path::new("."));
        let mut errors = self.errors.clone();
        let theme = if THEMES.contains(&self.theme.as_str()) {
            self.theme.as_str()
        } else {
            "dark"
        };
        let preset_id = if theme == "custom" { "dark" } else { theme };
        let mut tokens = Tokens::preset(preset_id).unwrap_or_default();
        let base16 = expand_path(&self.base16, base_dir);
        let import = expand_path(&self.import, base_dir);
        if theme == "custom" {
            if !base16.is_empty() {
                match std::fs::read_to_string(&base16)
                    .map_err(|e| e.to_string())
                    .and_then(|s| base16_tokens(&s))
                {
                    Ok(t) => tokens = t,
                    Err(e) => errors.push(format!("base16 {base16}: {e}")),
                }
            }
            if !import.is_empty() {
                match std::fs::read_to_string(&import)
                    .map_err(|e| e.to_string())
                    .and_then(|s| pywal_tokens(&s))
                {
                    Ok(t) => tokens = t,
                    Err(e) => errors.push(format!("import {import}: {e}")),
                }
            }
            for (k, v) in &self.custom {
                tokens.set(k, v);
            }
            if self.custom.contains_key("accent") && !self.custom.contains_key("selection") {
                tokens.selection = selection_for(&tokens.accent);
            }
        }
        if !self.accent.is_empty() {
            tokens.accent = self.accent.clone();
            tokens.selection = selection_for(&self.accent);
        }
        DesktopConfig {
            theme: theme.to_string(),
            accent: self.accent.clone(),
            font: self.font.clone(),
            mono_font: self.mono_font.clone(),
            density: self.density.clone(),
            sidebar: self.sidebar.clone(),
            start_section: self.start_section.clone(),
            tray: self.tray.clone(),
            close_to_tray: self.close_to_tray,
            reduced_motion: self.reduced_motion,
            custom: tokens,
            base16,
            import,
            source_path: path.display().to_string(),
            errors,
        }
    }
}

// --- writing ---------------------------------------------------------------------------

/// The leading comment block of a file (comment and blank lines before the first key).
pub fn header_of(text: &str) -> String {
    let mut out = String::new();
    for line in text.lines() {
        let t = line.trim_start();
        if t.is_empty() || t.starts_with('#') {
            out.push_str(line);
            out.push('\n');
        } else {
            break;
        }
    }
    out
}

fn json_to_yaml(v: &Json) -> Result<Yaml, String> {
    serde_yaml::to_value(v).map_err(|e| e.to_string())
}

/// Merges a JSON patch into the YAML text and returns the new text. `custom` is
/// merged key by key; everything else is replaced. The result must parse without
/// new errors, or the merge is rejected.
pub fn merge_patch(text: &str, patch: &Json) -> Result<String, String> {
    let Json::Object(patch) = patch else {
        return Err("patch must be an object".into());
    };
    let header = header_of(text);
    let mut doc: Yaml = serde_yaml::from_str(text).map_err(|e| format!("current file: {e}"))?;
    if doc.is_null() {
        doc = Yaml::Mapping(Default::default());
    }
    let Yaml::Mapping(map) = &mut doc else {
        return Err("current file is not a mapping".into());
    };
    for (k, v) in patch {
        let key = Yaml::String(k.clone());
        if k == "custom" {
            if v.is_null() {
                map.remove(&key);
                continue;
            }
            let Json::Object(sub) = v else {
                return Err("`custom` must be an object".into());
            };
            let entry = map
                .entry(key)
                .or_insert_with(|| Yaml::Mapping(Default::default()));
            if entry.is_null() {
                *entry = Yaml::Mapping(Default::default());
            }
            let Yaml::Mapping(cm) = entry else {
                return Err("`custom` in the file is not a mapping".into());
            };
            for (ck, cv) in sub {
                if cv.is_null() {
                    cm.remove(Yaml::String(ck.clone()));
                } else {
                    cm.insert(Yaml::String(ck.clone()), json_to_yaml(cv)?);
                }
            }
        } else if v.is_null() {
            map.remove(&key);
        } else {
            map.insert(key, json_to_yaml(v)?);
        }
    }
    // Do not keep the shell's own read-only fields if a frontend sends the whole config back.
    for ro in ["source_path", "errors"] {
        map.remove(Yaml::String(ro.into()));
    }
    let body = serde_yaml::to_string(&doc).map_err(|e| e.to_string())?;
    let before = parse(text).errors;
    let after = parse(&body).errors;
    let new_errors: Vec<&String> = after.iter().filter(|e| !before.contains(e)).collect();
    if !new_errors.is_empty() {
        return Err(format!(
            "patch rejected: {}",
            new_errors
                .iter()
                .map(|s| s.as_str())
                .collect::<Vec<_>>()
                .join("; ")
        ));
    }
    Ok(format!("{header}{body}"))
}

/// `$XDG_CONFIG_HOME/bnmdesktop/config.yaml`.
pub fn default_path() -> PathBuf {
    crate::daemon::xdg_config_home()
        .join("bnmdesktop")
        .join("config.yaml")
}

/// Writes through symlinks: dotfile managers (stow, chezmoi, home-manager) link
/// `config.yaml` to their own tree, and a rename over the link would replace it
/// with a plain file. The temp file lives beside the real target so the rename is
/// atomic on the same filesystem.
fn write_atomic(path: &Path, text: &str) -> Result<(), String> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    }
    let target = std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    let tmp = target.with_extension("yaml.tmp");
    std::fs::write(&tmp, text).map_err(|e| format!("write {}: {e}", tmp.display()))?;
    std::fs::rename(&tmp, &target).map_err(|e| format!("rename to {}: {e}", target.display()))
}

/// The config file plus the last resolved value.
pub struct ConfigStore {
    path: PathBuf,
    current: Mutex<DesktopConfig>,
}

impl ConfigStore {
    /// Opens (creating the commented default on first run) and loads the file.
    pub fn open(path: PathBuf) -> Self {
        if !path.exists() {
            match write_atomic(&path, DEFAULT_FILE) {
                Ok(()) => info!(path = %path.display(), "wrote default config"),
                Err(e) => warn!(path = %path.display(), error = %e, "cannot write default config"),
            }
        }
        let store = ConfigStore {
            current: Mutex::new(DesktopConfig::default()),
            path,
        };
        store.reload();
        store
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Re-reads the file and returns the new effective config.
    pub fn reload(&self) -> DesktopConfig {
        let cfg = match std::fs::read_to_string(&self.path) {
            Ok(text) => parse(&text).resolve(&self.path),
            Err(e) => {
                let mut c = FileConfig::default().resolve(&self.path);
                c.errors.push(format!("read {}: {e}", self.path.display()));
                c
            }
        };
        if !cfg.errors.is_empty() {
            warn!(errors = ?cfg.errors, "config has problems");
        }
        debug!(theme = %cfg.theme, density = %cfg.density, tray = %cfg.tray, "config loaded");
        *self.current.lock().unwrap_or_else(|p| p.into_inner()) = cfg.clone();
        cfg
    }

    pub fn get(&self) -> DesktopConfig {
        self.current
            .lock()
            .unwrap_or_else(|p| p.into_inner())
            .clone()
    }

    /// Applies a patch to the file and returns the resolved result. The watcher
    /// also fires afterwards.
    pub fn set(&self, patch: &Json) -> Result<DesktopConfig, String> {
        let text = match std::fs::read_to_string(&self.path) {
            Ok(t) => t,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => DEFAULT_FILE.to_string(),
            Err(e) => return Err(format!("read {}: {e}", self.path.display())),
        };
        let merged = merge_patch(&text, patch)?;
        write_atomic(&self.path, &merged)?;
        Ok(self.reload())
    }
}

/// The directories to watch for `path` and the file names that count inside them:
/// the file's own directory, plus the directory of the real file when `path` is a
/// symlink (a dotfile manager's tree), since inotify on the link's directory never
/// sees writes to the target.
pub fn watch_targets(path: &Path) -> Result<Vec<(PathBuf, std::ffi::OsString)>, String> {
    let mut out = Vec::new();
    let mut push = |p: &Path| -> Result<(), String> {
        let dir = p
            .parent()
            .map(Path::to_path_buf)
            .ok_or_else(|| format!("{}: no parent directory", p.display()))?;
        let name = p
            .file_name()
            .map(|s| s.to_os_string())
            .ok_or_else(|| format!("{}: no file name", p.display()))?;
        if !out.contains(&(dir.clone(), name.clone())) {
            out.push((dir, name));
        }
        Ok(())
    };
    push(path)?;
    if let Ok(real) = std::fs::canonicalize(path) {
        if real != path {
            push(&real)?;
        }
    }
    Ok(out)
}

/// Watches the config file's directory (so editors that write-and-rename still
/// count) and calls `on_change` at most once per 150 ms burst. Keep the returned
/// watcher alive.
pub fn watch(
    path: PathBuf,
    on_change: impl Fn() + Send + 'static,
) -> Result<RecommendedWatcher, String> {
    let targets = watch_targets(&path)?;
    let (dir, _) = targets
        .first()
        .cloned()
        .ok_or("config path has no parent")?;
    std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    let names: Vec<std::ffi::OsString> = targets.iter().map(|(_, n)| n.clone()).collect();
    let (tx, rx) = mpsc::channel::<()>();
    let mut watcher =
        notify::recommended_watcher(move |res: notify::Result<notify::Event>| match res {
            Ok(ev) => {
                // Our own reload reads the file, which a directory watch reports as
                // an access event; reacting to those would loop forever.
                if ev.kind.is_access() {
                    return;
                }
                if ev
                    .paths
                    .iter()
                    .any(|p| p.file_name().is_some_and(|f| names.iter().any(|n| n == f)))
                {
                    let _ = tx.send(());
                }
            }
            Err(e) => warn!(error = %e, "config watcher"),
        })
        .map_err(|e| format!("watcher: {e}"))?;
    for (d, _) in &targets {
        watcher
            .watch(d, RecursiveMode::NonRecursive)
            .map_err(|e| format!("watch {}: {e}", d.display()))?;
    }
    std::thread::Builder::new()
        .name("config-debounce".into())
        .spawn(move || {
            while rx.recv().is_ok() {
                // Swallow the rest of the burst.
                while rx.recv_timeout(Duration::from_millis(150)).is_ok() {}
                on_change();
            }
        })
        .map_err(|e| format!("debounce thread: {e}"))?;
    info!(dirs = ?targets.iter().map(|(d, _)| d.display().to_string()).collect::<Vec<_>>(), "watching config");
    Ok(watcher)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn defaults_match_contract() {
        let d = DesktopConfig::default();
        assert_eq!(d.theme, "dark");
        assert_eq!(d.accent, "");
        assert_eq!(d.density, "default");
        assert_eq!(d.sidebar, "left");
        assert_eq!(d.start_section, "overview");
        assert_eq!(d.tray, "auto");
        assert!(d.close_to_tray);
        assert!(!d.reduced_motion);
        assert_eq!(d.custom, Tokens::preset("dark").unwrap());
        assert_eq!(d.custom.selection, "#5fa8c826");
        let js = serde_json::to_value(&d).unwrap();
        for key in [
            "theme",
            "accent",
            "font",
            "mono_font",
            "density",
            "sidebar",
            "start_section",
            "tray",
            "close_to_tray",
            "reduced_motion",
            "custom",
            "base16",
            "import",
            "source_path",
            "errors",
        ] {
            assert!(js.get(key).is_some(), "missing {key}");
        }
        assert_eq!(js["custom"]["accent_text"], "#0b1215");
    }

    #[test]
    fn default_file_parses_to_defaults() {
        let f = parse(DEFAULT_FILE);
        assert!(f.errors.is_empty(), "{:?}", f.errors);
        let d = f.resolve(Path::new("/x/config.yaml"));
        let want = DesktopConfig {
            source_path: "/x/config.yaml".into(),
            ..DesktopConfig::default()
        };
        assert_eq!(d, want);
    }

    #[test]
    fn yaml_round_trip() {
        let d = DesktopConfig {
            theme: "nord".into(),
            font: "Inter".into(),
            close_to_tray: false,
            ..DesktopConfig::default()
        };
        let text = serde_yaml::to_string(&d).unwrap();
        let f = parse(&text);
        assert!(f.errors.is_empty(), "{:?}", f.errors);
        let back = f.resolve(Path::new("/x/config.yaml"));
        assert_eq!(back.theme, "nord");
        assert_eq!(back.font, "Inter");
        assert!(!back.close_to_tray);
        assert_eq!(back.custom, Tokens::preset("nord").unwrap());
    }

    #[test]
    fn unknown_and_bad_keys_are_collected() {
        let f = parse("theme: nope\nbogus: 1\ndensity: 3\nclose_to_tray: yes\ncustom:\n  base: red\n  foo: \"#000000\"\ntray: true\n");
        assert_eq!(f.theme, "dark");
        assert_eq!(f.density, "default");
        assert!(f.close_to_tray, "unparsable bool keeps the default");
        assert_eq!(f.tray, "on");
        assert!(f.custom.is_empty());
        let joined = f.errors.join("\n");
        assert!(joined.contains("`theme`: \"nope\""), "{joined}");
        assert!(joined.contains("unknown key `bogus`"), "{joined}");
        assert!(joined.contains("`density`"), "{joined}");
        assert!(joined.contains("`close_to_tray`"), "{joined}");
        assert!(joined.contains("`custom.base`"), "{joined}");
        assert!(joined.contains("`custom.foo`: unknown token"), "{joined}");
        assert_eq!(f.errors.len(), 6);
    }

    #[test]
    fn garbage_yaml_is_one_error() {
        let f = parse("theme: [unterminated\n");
        assert_eq!(f.errors.len(), 1);
        assert!(f.errors[0].starts_with("yaml:"));
        assert_eq!(f.theme, "dark");
    }

    const SAMPLE_BASE16: &str = r##"
system: "base16"
name: "Sample Dark"
author: "someone"
variant: "dark"
palette:
  base00: "#181818"
  base01: "#282828"
  base02: "#383838"
  base03: "#585858"
  base04: "#b8b8b8"
  base05: "#d8d8d8"
  base06: "#e8e8e8"
  base07: "#f8f8f8"
  base08: "#ab4642"
  base09: "#dc9656"
  base0A: "#f7ca88"
  base0B: "#a1b56c"
  base0C: "#86c1b9"
  base0D: "#7cafc2"
  base0E: "#ba8baf"
  base0F: "#a16946"
"##;

    #[test]
    fn base16_mapping_dark() {
        let t = base16_tokens(SAMPLE_BASE16).unwrap();
        assert_eq!(t.base, "#181818");
        assert_eq!(t.surface, "#282828");
        assert_eq!(t.border, "#383838");
        assert_eq!(t.muted, "#585858");
        assert_eq!(t.subtext, "#b8b8b8");
        assert_eq!(t.text, "#d8d8d8");
        assert_eq!(t.accent, "#7cafc2");
        assert_eq!(t.accent_text, "#181818");
        assert_eq!(t.ok, "#a1b56c");
        assert_eq!(t.warn, "#f7ca88");
        assert_eq!(t.error, "#ab4642");
        assert_eq!(t.vpn, "#dc9656");
        assert_eq!(t.wifi, "#86c1b9");
        assert_eq!(t.selection, "#7cafc226");
        // derived: mantle 3 % toward black, overlay the midpoint of base00 and base01
        assert_eq!(t.mantle, "#171717");
        assert_eq!(t.overlay, "#202020");
    }

    #[test]
    fn base16_legacy_layout_and_light_variant() {
        let legacy = r##"
scheme: "Sample Light"
base00: "f8f8f8"
base01: "e8e8e8"
base02: "d8d8d8"
base03: "b8b8b8"
base04: "585858"
base05: "383838"
base06: "282828"
base07: "181818"
base08: "ab4642"
base09: "dc9656"
base0A: "f7ca88"
base0B: "a1b56c"
base0C: "86c1b9"
base0D: "7cafc2"
base0E: "ba8baf"
base0F: "a16946"
"##;
        let t = base16_tokens(legacy).unwrap();
        assert_eq!(t.base, "#f8f8f8");
        assert_eq!(
            t.accent_text, "#181818",
            "light schemes put base07 on the accent"
        );
        assert_eq!(t.overlay, "#fcfcfc", "light overlay lifts toward white");
        assert!(base16_tokens("palette:\n  base00: '#000000'\n")
            .unwrap_err()
            .contains("missing base01"));
    }

    const SAMPLE_PYWAL: &str = r##"{
  "wallpaper": "/x.png",
  "alpha": "100",
  "special": {"background": "#1a1b26", "foreground": "#c0caf5", "cursor": "#c0caf5"},
  "colors": {
    "color0": "#1a1b26", "color1": "#f7768e", "color2": "#9ece6a", "color3": "#e0af68",
    "color4": "#7aa2f7", "color5": "#bb9af7", "color6": "#7dcfff", "color7": "#a9b1d6",
    "color8": "#414868", "color9": "#f7768e", "color10": "#9ece6a", "color11": "#e0af68",
    "color12": "#7aa2f7", "color13": "#bb9af7", "color14": "#7dcfff", "color15": "#c0caf5"
  }
}"##;

    #[test]
    fn pywal_mapping() {
        let t = pywal_tokens(SAMPLE_PYWAL).unwrap();
        assert_eq!(t.base, "#1a1b26");
        assert_eq!(t.text, "#c0caf5");
        assert_eq!(t.accent, "#7aa2f7");
        assert_eq!(t.ok, "#9ece6a");
        assert_eq!(t.warn, "#e0af68");
        assert_eq!(t.error, "#f7768e");
        assert_eq!(t.vpn, "#bb9af7");
        assert_eq!(t.wifi, "#7dcfff");
        assert_eq!(t.muted, "#414868");
        assert_eq!(t.accent_text, "#1a1b26");
        assert_eq!(t.selection, "#7aa2f726");
        assert_eq!(t.mantle, "#191a25");
        assert_eq!(t.surface, "#272937");
        assert_eq!(t.border, "#2e303f");
        assert!(pywal_tokens("{}")
            .unwrap_err()
            .contains("missing special.background"));
    }

    #[test]
    fn precedence_preset_base16_import_custom_accent() {
        let dir = tempfile::tempdir().unwrap();
        let cfg_path = dir.path().join("config.yaml");
        std::fs::write(dir.path().join("scheme.yaml"), SAMPLE_BASE16).unwrap();
        std::fs::write(dir.path().join("colors.json"), SAMPLE_PYWAL).unwrap();

        // preset only
        let d = parse("theme: nord\n").resolve(&cfg_path);
        assert_eq!(d.custom, Tokens::preset("nord").unwrap());

        // a preset theme ignores base16/import/custom but honours accent
        let d = parse(
            "theme: nord\nbase16: scheme.yaml\ncustom:\n  base: \"#000000\"\naccent: \"#ff0000\"\n",
        )
        .resolve(&cfg_path);
        assert_eq!(d.custom.base, "#2e3440");
        assert_eq!(d.custom.accent, "#ff0000");
        assert_eq!(d.custom.selection, "#ff000026");
        assert_eq!(
            d.base16,
            dir.path().join("scheme.yaml").display().to_string(),
            "relative paths resolve against the config dir"
        );

        // custom without anything = dark
        let d = parse("theme: custom\n").resolve(&cfg_path);
        assert_eq!(d.custom, Tokens::preset("dark").unwrap());

        // custom + base16
        let d = parse("theme: custom\nbase16: scheme.yaml\n").resolve(&cfg_path);
        assert_eq!(d.custom.base, "#181818");
        assert!(d.errors.is_empty(), "{:?}", d.errors);

        // import beats base16
        let d =
            parse("theme: custom\nbase16: scheme.yaml\nimport: colors.json\n").resolve(&cfg_path);
        assert_eq!(d.custom.base, "#1a1b26");
        assert_eq!(d.custom.accent, "#7aa2f7");

        // custom keys beat both; selection follows a custom accent
        let d = parse("theme: custom\nbase16: scheme.yaml\nimport: colors.json\ncustom:\n  base: \"#010203\"\n  accent: \"#abcdef\"\n").resolve(&cfg_path);
        assert_eq!(d.custom.base, "#010203");
        assert_eq!(d.custom.accent, "#abcdef");
        assert_eq!(d.custom.selection, "#abcdef26");
        assert_eq!(
            d.custom.text, "#c0caf5",
            "untouched tokens come from the import"
        );

        // top-level accent beats everything
        let d = parse("theme: custom\nimport: colors.json\ncustom:\n  accent: \"#abcdef\"\naccent: \"#123456\"\n").resolve(&cfg_path);
        assert_eq!(d.custom.accent, "#123456");
        assert_eq!(d.custom.selection, "#12345626");

        // a missing file is an error entry, not a failure
        let d = parse("theme: custom\nbase16: nope.yaml\n").resolve(&cfg_path);
        assert_eq!(d.custom, Tokens::preset("dark").unwrap());
        assert_eq!(d.errors.len(), 1);
        assert!(d.errors[0].starts_with("base16 "), "{}", d.errors[0]);
    }

    #[test]
    fn tilde_expansion() {
        let home = crate::daemon::home_dir().display().to_string();
        assert_eq!(
            expand_path("~/a.yaml", Path::new("/c")),
            format!("{home}/a.yaml")
        );
        assert_eq!(expand_path("/abs.yaml", Path::new("/c")), "/abs.yaml");
        assert_eq!(expand_path("rel.yaml", Path::new("/c")), "/c/rel.yaml");
        assert_eq!(expand_path("", Path::new("/c")), "");
    }

    #[test]
    fn patch_merge_keeps_header_and_merges_custom() {
        let merged = merge_patch(
            DEFAULT_FILE,
            &json!({"theme": "custom", "custom": {"accent": "#ff00ff"}, "close_to_tray": false}),
        )
        .unwrap();
        assert!(
            merged.starts_with("# bnm desktop configuration."),
            "{merged}"
        );
        let f = parse(&merged);
        assert!(f.errors.is_empty(), "{:?}", f.errors);
        assert_eq!(f.theme, "custom");
        assert!(!f.close_to_tray);
        assert_eq!(f.custom["accent"], "#ff00ff");
        assert_eq!(f.custom["base"], "#141517", "other custom keys survive");
        assert_eq!(f.custom.len(), 16);
        // key order is preserved
        let ti = merged.find("\ntheme:").unwrap();
        let ai = merged.find("\naccent:").unwrap();
        assert!(ti < ai);
    }

    #[test]
    fn patch_merge_rejects_invalid_and_strips_readonly() {
        let err = merge_patch("theme: dark\n", &json!({"density": "huge"})).unwrap_err();
        assert!(err.contains("`density`"), "{err}");
        assert!(merge_patch("theme: dark\n", &json!(["x"])).is_err());
        let merged = merge_patch(
            "",
            &json!({"theme": "nord", "source_path": "/x", "errors": ["e"]}),
        )
        .unwrap();
        assert!(!merged.contains("source_path"));
        assert!(!merged.contains("errors"));
        assert!(merged.contains("theme: nord"));
        // null removes a key
        let merged = merge_patch("theme: nord\nfont: X\n", &json!({"font": null})).unwrap();
        assert!(!merged.contains("font"));
        // null removes the whole custom map too; a key inside it is removed alone
        let merged = merge_patch(DEFAULT_FILE, &json!({"custom": null})).unwrap();
        assert!(!merged.contains("custom"), "{merged}");
        assert!(parse(&merged).custom.is_empty());
        let merged = merge_patch(DEFAULT_FILE, &json!({"custom": {"base": null}})).unwrap();
        let f = parse(&merged);
        assert!(f.errors.is_empty(), "{:?}", f.errors);
        assert_eq!(f.custom.len(), 15);
        assert!(!f.custom.contains_key("base"));
        // a bad token in the nested patch rejects the whole patch
        let err = merge_patch(DEFAULT_FILE, &json!({"custom": {"base": "red"}})).unwrap_err();
        assert!(err.contains("`custom.base`"), "{err}");
        let err = merge_patch(DEFAULT_FILE, &json!({"custom": {"nope": "#000000"}})).unwrap_err();
        assert!(err.contains("`custom.nope`"), "{err}");
        assert!(merge_patch(DEFAULT_FILE, &json!({"custom": 5})).is_err());
    }

    #[test]
    fn store_writes_through_a_symlink() {
        let dir = tempfile::tempdir().unwrap();
        let real_dir = dir.path().join("dotfiles");
        std::fs::create_dir_all(&real_dir).unwrap();
        let real = real_dir.join("bnmdesktop.yaml");
        std::fs::write(&real, "theme: nord\n").unwrap();
        let cfg_dir = dir.path().join("config");
        std::fs::create_dir_all(&cfg_dir).unwrap();
        let link = cfg_dir.join("config.yaml");
        std::os::unix::fs::symlink(&real, &link).unwrap();

        let store = ConfigStore::open(link.clone());
        assert_eq!(store.get().theme, "nord");
        store.set(&json!({"theme": "dracula"})).unwrap();
        assert!(
            std::fs::symlink_metadata(&link)
                .unwrap()
                .file_type()
                .is_symlink(),
            "the link must survive a write"
        );
        assert!(std::fs::read_to_string(&real)
            .unwrap()
            .contains("theme: dracula"));
        assert!(!real_dir.join("bnmdesktop.yaml.tmp").exists());

        // The watcher covers both the link's directory and the target's.
        let targets = watch_targets(&link).unwrap();
        assert_eq!(targets.len(), 2);
        assert_eq!(targets[0].0, cfg_dir);
        assert_eq!(targets[0].1, "config.yaml");
        assert_eq!(targets[1].0, std::fs::canonicalize(&real_dir).unwrap());
        assert_eq!(targets[1].1, "bnmdesktop.yaml");
        // A plain file watches one directory.
        assert_eq!(watch_targets(&real).unwrap().len(), 1);
    }

    #[test]
    fn store_writes_default_and_applies_patches() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sub").join("config.yaml");
        let store = ConfigStore::open(path.clone());
        assert_eq!(std::fs::read_to_string(&path).unwrap(), DEFAULT_FILE);
        let cfg = store.get();
        assert_eq!(cfg.theme, "dark");
        assert_eq!(cfg.source_path, path.display().to_string());
        let cfg = store.set(&json!({"theme": "dracula"})).unwrap();
        assert_eq!(cfg.theme, "dracula");
        assert_eq!(cfg.custom.accent, "#8be9fd");
        assert_eq!(store.get().theme, "dracula");
        assert!(store.set(&json!({"theme": "unknown"})).is_err());
        assert_eq!(
            store.get().theme,
            "dracula",
            "a rejected patch leaves the file alone"
        );
    }

    #[test]
    fn header_extraction() {
        assert_eq!(header_of("# a\n\n# b\ntheme: x\n# c\n"), "# a\n\n# b\n");
        assert_eq!(header_of("theme: x\n"), "");
    }

    #[test]
    fn colour_helpers() {
        assert_eq!(normalize_hex("#ABCDEF"), Some("#abcdef".into()));
        assert_eq!(normalize_hex("abcdef"), Some("#abcdef".into()));
        assert_eq!(normalize_hex("#abcdef26"), Some("#abcdef26".into()));
        assert_eq!(normalize_hex("#abc"), None);
        assert_eq!(normalize_hex("red"), None);
        assert_eq!(
            mix(Rgb(0, 0, 0), Rgb(255, 255, 255), 0.5),
            Rgb(128, 128, 128)
        );
        assert!(is_dark(Rgb(20, 21, 23)));
        assert!(!is_dark(Rgb(247, 247, 248)));
    }
}
