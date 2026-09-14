#!/bin/sh
# Injects the burn-pin overlay into the management panel. Idempotent per
# overlay version: an already-current panel is left alone, an older overlay is
# replaced by restoring the pristine backup first. Re-run after any panel
# update. Usage: apply-burn-pin-ui.sh [deploy-dir] (default: script directory,
# expecting static/management.html and burn-pin-ui.html beside it).
set -eu
DIR="${1:-$(dirname "$0")}"
PANEL="$DIR/static/management.html"
PRISTINE="$PANEL.pre-burn-pin"
SNIPPET="$(dirname "$0")/burn-pin-ui.html"
MARKER="$(head -1 "$SNIPPET")"

if ! [ -f "$PANEL" ]; then
  echo "panel not found: $PANEL" >&2
  exit 1
fi
if grep -qF "$MARKER" "$PANEL"; then
  echo "already applied ($MARKER)"
  exit 0
fi
if grep -q 'burn-pin-ui v' "$PANEL"; then
  if ! [ -f "$PRISTINE" ]; then
    echo "older overlay present but pristine backup missing: $PRISTINE" >&2
    exit 1
  fi
  cp "$PRISTINE" "$PANEL"
  echo "restored pristine panel before upgrading overlay"
fi
if ! grep -q '</body>' "$PANEL"; then
  echo "no </body> tag found in panel" >&2
  exit 1
fi
cp "$PANEL" "$PRISTINE"
python3 - "$PANEL" "$SNIPPET" <<'PYEOF'
import sys
panel_path, snippet_path = sys.argv[1], sys.argv[2]
panel = open(panel_path, encoding="utf-8").read()
snippet = open(snippet_path, encoding="utf-8").read()
idx = panel.rindex("</body>")
open(panel_path, "w", encoding="utf-8").write(panel[:idx] + snippet + panel[idx:])
PYEOF
echo "applied $MARKER ($(wc -c < "$PANEL") bytes)"
