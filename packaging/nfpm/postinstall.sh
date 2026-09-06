#!/bin/sh
# Runs after deb/rpm/pacman install. Enables the user unit where the distro's policy allows.
set -e
if command -v systemctl >/dev/null 2>&1; then
  systemctl --global daemon-reload >/dev/null 2>&1 || true
fi
if command -v sysctl >/dev/null 2>&1 && [ -f /usr/lib/sysctl.d/50-bnm.conf ]; then
  sysctl -q -p /usr/lib/sysctl.d/50-bnm.conf >/dev/null 2>&1 || true
fi
case "$(cat /etc/os-release 2>/dev/null | grep '^ID=' | cut -d= -f2 | tr -d '"')" in
  debian|ubuntu|linuxmint|pop)
    # Debian policy: services start on install.
    if command -v deb-systemd-helper >/dev/null 2>&1; then
      deb-systemd-helper --user enable bnmd.service >/dev/null 2>&1 || true
    else
      systemctl --global enable bnmd.service >/dev/null 2>&1 || true
    fi
    ;;
  fedora|rhel|centos|rocky|almalinux|opensuse*)
    # rpm: preset file decides; apply it.
    systemctl --global preset bnmd.service >/dev/null 2>&1 || true
    ;;
  *)
    echo "bnm: enable the daemon for your user with:  systemctl --user enable --now bnmd.service"
    ;;
esac
exit 0
