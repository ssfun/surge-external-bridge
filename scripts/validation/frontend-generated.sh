#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/surgeeb-frontend.XXXXXX")"

cleanup() {
  rm -rf "$temporary_dir"
}
trap cleanup EXIT

FRONTEND_OUT_DIR="$temporary_dir" npm run build --prefix "$repo_dir/frontend"
diff -ru "$repo_dir/internal/webassets/static" "$temporary_dir"
