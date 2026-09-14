// Review-only: flips presets/density/font while looking at the mockups.
// Not part of the product. The product reads the same tokens from its config.
//   ?theme=<slug>      pick a preset (also remembered in localStorage)
//   ?density=compact   compact | default | comfortable
//   ?font=inter        geist | inter | system
//   ?chrome=0          hide this control (screenshots)
//   ?motion=0          settle every animation immediately (screenshots)
(function () {
  var PRESETS = [
    ["dark", "bnm Dark"], ["light", "bnm Light"],
    ["catppuccin-mocha", "Catppuccin Mocha"], ["catppuccin-latte", "Catppuccin Latte"],
    ["gruvbox-dark", "Gruvbox Dark"], ["nord", "Nord"], ["tokyo-night", "Tokyo Night"],
    ["rose-pine", "Rosé Pine"], ["dracula", "Dracula"], ["one-dark", "One Dark"]
  ];
  var q = new URLSearchParams(location.search);
  var store = { get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
                set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) {} } };
  var link = document.getElementById("preset");
  if (!link) { link = document.createElement("link"); link.id = "preset"; link.rel = "stylesheet"; document.head.appendChild(link); }
  var base = (link.getAttribute("href") || "presets/dark.css").replace(/[^/]*$/, "");

  function apply(slug) {
    if (!PRESETS.some(function (p) { return p[0] === slug; })) slug = "dark";
    link.href = base + slug + ".css";
    document.documentElement.dataset.theme = slug;
    store.set("bnm.theme", slug);
    var sel = document.getElementById("ts-theme"); if (sel) sel.value = slug;
  }
  function applyAttr(name, v) {
    if (v && v !== "default") document.documentElement.dataset[name] = v; else delete document.documentElement.dataset[name];
    store.set("bnm." + name, v || "default");
    var sel = document.getElementById("ts-" + name); if (sel) sel.value = v || "default";
  }

  apply(q.get("theme") || store.get("bnm.theme") || document.documentElement.dataset.theme || "dark");
  applyAttr("density", q.get("density") || store.get("bnm.density"));
  applyAttr("font", q.get("font") || store.get("bnm.font"));
  if (q.get("motion") === "0") document.documentElement.dataset.motion = "0";

  if (q.get("chrome") === "0") return;

  function mount() {
    var box = document.createElement("div");
    box.id = "theme-switcher";
    box.innerHTML =
      '<style>#theme-switcher{position:fixed;left:12px;bottom:12px;z-index:9999;display:flex;gap:6px;align-items:center;padding:6px;' +
      'background:var(--overlay);border:1px solid var(--border);border-radius:var(--r);font:12px var(--font-ui);color:var(--subtext);opacity:.35;transition:opacity 120ms}' +
      '#theme-switcher:hover{opacity:1}#theme-switcher select{height:24px;border:1px solid var(--border);background:var(--surface);color:var(--text);border-radius:var(--r);font:inherit;padding:0 4px}' +
      '#theme-switcher b{font-weight:500;padding:0 4px}</style>' +
      '<b>review</b>' +
      '<select id="ts-theme" title="preset">' + PRESETS.map(function (p) { return '<option value="' + p[0] + '">' + p[1] + '</option>'; }).join("") + '</select>' +
      '<select id="ts-density" title="density"><option value="default">default</option><option value="compact">compact</option><option value="comfortable">comfortable</option></select>' +
      '<select id="ts-font" title="font"><option value="default">Geist</option><option value="inter">Inter</option><option value="system">system</option></select>';
    document.body.appendChild(box);
    document.getElementById("ts-theme").value = document.documentElement.dataset.theme;
    document.getElementById("ts-density").value = document.documentElement.dataset.density || "default";
    document.getElementById("ts-font").value = document.documentElement.dataset.font || "default";
    document.getElementById("ts-theme").addEventListener("change", function (e) { apply(e.target.value); });
    document.getElementById("ts-density").addEventListener("change", function (e) { applyAttr("density", e.target.value); });
    document.getElementById("ts-font").addEventListener("change", function (e) { applyAttr("font", e.target.value); });
    // Keyboard: ] and [ cycle presets while reviewing.
    document.addEventListener("keydown", function (e) {
      if (e.target && /INPUT|SELECT|TEXTAREA/.test(e.target.tagName)) return;
      if (e.key !== "]" && e.key !== "[") return;
      var i = PRESETS.findIndex(function (p) { return p[0] === document.documentElement.dataset.theme; });
      i = (i + (e.key === "]" ? 1 : PRESETS.length - 1)) % PRESETS.length;
      apply(PRESETS[i][0]);
    });
  }
  if (document.body) mount(); else document.addEventListener("DOMContentLoaded", mount);
})();
