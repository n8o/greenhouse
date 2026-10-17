#!/usr/bin/env bash
# Regenerate .beads/plans/*.jsonl from the local bd database (one file per plan,
# one stable line per bead). The JSONL is the source of truth for the PLAN; run
# state stays local (ADR-0005): stage:* labels are dropped, in_progress is
# written as open, and updated_at and started_at are omitted, so a bead's line changes only when
# its plan or its completion does. Never hand-edit the JSONL.
#
# The export is MERGED with HEAD's store (nugit ADR-0047). The bd database is
# per clone and nothing pulls other sessions' merged plan changes into it, but
# `bd export` writes every plan: a database behind main would reopen closes and
# delete beads. normalize -merge HEAD does a three-way merge whose base is the
# previous staged export, kept in the git common dir (one per clone, shared by
# every worktree), so the diff is only this session's bd edits. With no base
# yet (first run on a clone) it falls back to closes-win; read that diff.
set -euo pipefail
command -v bd >/dev/null || { echo "error: bd not on PATH" >&2; exit 1; }
command -v nugit >/dev/null || { echo "error: nugit not on PATH" >&2; exit 1; }
command -v jq >/dev/null || { echo "error: jq not on PATH" >&2; exit 1; }
normalize_help="$(nugit plan normalize -h 2>&1 || true)"
if [[ "$normalize_help" != *-merge-base* ]]; then
  echo "error: nugit has no 'plan normalize -merge-base'; upgrade it (greenhouse doctor names the version)" >&2
  exit 1
fi

root="$(git rev-parse --show-toplevel)"
staging="$root/.beads/store.jsonl"
base="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)/beads-export-base.jsonl"
raw="$(mktemp)"
trap 'rm -f "$staging" "$raw" "$base.next"' EXIT
mkdir -p "$root/.beads/plans"
bd export -o "$raw"
jq -c '
  del(.updated_at, .started_at)
  | if .status == "in_progress" then .status = "open" else . end
  | if .labels then .labels |= map(select(startswith("stage:") | not)) else . end
  | if (.labels // []) == [] then del(.labels) else . end
' "$raw" > "$staging"
# Keep exactly what normalize is fed (run state already stripped): it is the
# next run's merge base.
cp "$staging" "$base.next"
rm -f "$root"/.beads/plans/*.jsonl
if ! nugit plan normalize -C "$root" -split -merge HEAD -merge-base "$base" -write; then
  git -C "$root" checkout -- .beads/plans/ 2>/dev/null || true
  exit 1
fi
mv "$base.next" "$base"
git -C "$root" --no-pager diff --stat -- .beads/ || true
