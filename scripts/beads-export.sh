#!/usr/bin/env bash
# Regenerate .beads/plans/*.jsonl from the local bd database (one file per plan,
# one stable line per bead). The JSONL is the source of truth for the PLAN; run
# state stays local (ADR-0005): stage:* labels are dropped, in_progress is
# written as open, and updated_at is omitted, so a bead's line changes only when
# its plan or its completion does. Never hand-edit the JSONL.
set -euo pipefail
command -v bd >/dev/null || { echo "error: bd not on PATH" >&2; exit 1; }
command -v nugit >/dev/null || { echo "error: nugit not on PATH" >&2; exit 1; }
command -v jq >/dev/null || { echo "error: jq not on PATH" >&2; exit 1; }

root="$(git rev-parse --show-toplevel)"
staging="$root/.beads/store.jsonl"
raw="$(mktemp)"
trap 'rm -f "$staging" "$raw"' EXIT
mkdir -p "$root/.beads/plans"
bd export -o "$raw"
jq -c '
  del(.updated_at)
  | if .status == "in_progress" then .status = "open" else . end
  | if .labels then .labels |= map(select(startswith("stage:") | not)) else . end
  | if (.labels // []) == [] then del(.labels) else . end
' "$raw" > "$staging"
rm -f "$root"/.beads/plans/*.jsonl
nugit plan normalize -C "$root" -split -write
git -C "$root" --no-pager diff --stat -- .beads/ || true
