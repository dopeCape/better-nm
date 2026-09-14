#!/usr/bin/env bash
# Fetches the Phosphor icons this design uses from phosphor-icons/core (MIT).
# Re-run after editing the lists; then run build-sprite.py to regenerate sprite.js.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p regular fill duotone
BASE=https://raw.githubusercontent.com/phosphor-icons/core/main/assets
REGULAR="house wifi-high wifi-medium wifi-low wifi-none wifi-slash shield-check pulse gauge gear-six network
arrows-clockwise magnifying-glass lock-simple lock-simple-open cell-signal-full cell-signal-high cell-signal-medium cell-signal-low cell-signal-none cell-signal-slash
check funnel eye eye-slash key plus file-arrow-up arrow-square-out play pause arrow-counter-clockwise lightning command
arrow-right arrow-left arrow-up arrow-down arrow-elbow-down-left x caret-down caret-right caret-up-down copy palette text-aa rows bell
globe dots-three plugs-connected plug moon sun desktop device-mobile laptop spinner-gap clock-countdown floppy-disk
shield-warning trash download-simple upload-simple keyboard question warning-circle info check-circle x-circle circle
sliders-horizontal terminal-window broadcast list-dashes cell-tower star sign-in sign-out user link power hard-drives
app-window bell-slash"
FILL="check-circle warning-circle info x-circle circle star lock-simple lightning shield-check shield-warning wifi-high pulse"
DUOTONE="wifi-high shield-check pulse gauge house gear-six network"
for n in $REGULAR; do curl -sSfL -o "regular/$n.svg" "$BASE/regular/$n.svg" || echo "MISSING regular/$n"; done
for n in $FILL; do curl -sSfL -o "fill/$n-fill.svg" "$BASE/fill/$n-fill.svg" || echo "MISSING fill/$n"; done
for n in $DUOTONE; do curl -sSfL -o "duotone/$n-duotone.svg" "$BASE/duotone/$n-duotone.svg" || echo "MISSING duotone/$n"; done
echo done
