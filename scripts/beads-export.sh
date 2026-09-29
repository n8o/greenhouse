#!/usr/bin/env bash
# Regenerate .beads/plans/*.jsonl from the local bd database (one file per plan,
# one stable line per bead). The JSONL is the source of truth; the database is a
# disposable cache. Never hand-edit the JSONL.
set -euo pipefail
command -v bd >/dev/null || { echo "error: bd not on PATH" >&2; exit 1; }
command -v nugit >/dev/null || { echo "error: nugit not on PATH" >&2; exit 1; }

root="$(git rev-parse --show-toplevel)"
staging="$root/.beads/store.jsonl"
mkdir -p "$root/.beads/plans"
bd export -o "$staging"
trap 'rm -f "$staging"' EXIT
rm -f "$root"/.beads/plans/*.jsonl
nugit plan normalize -C "$root" -split -write
git -C "$root" --no-pager diff --stat -- .beads/ || true
