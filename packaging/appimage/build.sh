#!/usr/bin/env bash
# Builds bnm-desktop-<version>-<arch>.AppImage from an already-built ./bin/bnm-desktop.
# Fyne links only base GL/X11/Wayland libs, all on the AppImage exclude list, so the
# AppDir is: binary + .desktop + icon + AppRun. Build on the oldest supported distro
# so the glibc floor stays low (the release workflow uses ubuntu-22.04).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
version=${VERSION:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)}
arch=${ARCH:-$(uname -m)}
bin=${BIN:-$root/bin/bnm-desktop}
out=${OUT:-$root/dist}
[ -x "$bin" ] || { echo "missing $bin (run: make desktop)"; exit 1; }

work=$(mktemp -d)
appdir=$work/bnm.AppDir
mkdir -p "$appdir/usr/bin" "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/256x256/apps"
cp "$bin" "$appdir/usr/bin/bnm-desktop"
cp "$root/packaging/desktop/bnm-desktop.desktop" "$appdir/"
cp "$root/packaging/desktop/bnm-desktop.desktop" "$appdir/usr/share/applications/"
cp "$root/packaging/desktop/bnm.png" "$appdir/bnm.png"
cp "$root/packaging/desktop/bnm.png" "$appdir/usr/share/icons/hicolor/256x256/apps/bnm.png"
cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
here=$(dirname "$(readlink -f "$0")")
export PATH="$here/usr/bin:$PATH"
exec "$here/usr/bin/bnm-desktop" "$@"
EOF
chmod +x "$appdir/AppRun"

tool=$work/appimagetool
if ! command -v appimagetool >/dev/null 2>&1; then
  case "$arch" in x86_64|aarch64) ;; *) echo "unsupported arch $arch"; exit 1;; esac
  curl -sSL -o "$tool" "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-${arch}.AppImage"
  chmod +x "$tool"
  # In containers without FUSE, extract and run the inner tool.
  if ! "$tool" --version >/dev/null 2>&1; then
    (cd "$work" && "$tool" --appimage-extract >/dev/null) && tool=$work/squashfs-root/AppRun
  fi
else
  tool=$(command -v appimagetool)
fi
mkdir -p "$out"
ARCH=$arch "$tool" -n "$appdir" "$out/bnm-desktop-${version}-${arch}.AppImage"
echo "built $out/bnm-desktop-${version}-${arch}.AppImage"
rm -rf "$work"
