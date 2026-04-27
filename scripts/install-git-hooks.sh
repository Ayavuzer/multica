#!/usr/bin/env bash
# Install repo-local git hooks for the Multica fork.
# Idempotent — safe to run on every checkout / setup.

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
hooks_src="$repo_root/scripts/git-hooks"
hooks_dst="$(git rev-parse --git-path hooks)"

mkdir -p "$hooks_dst"

for hook in "$hooks_src"/*; do
  name="$(basename "$hook")"
  target="$hooks_dst/$name"
  cp "$hook" "$target"
  chmod +x "$target"
  echo "installed: $name → $target"
done

echo ""
echo "Done. Pre-push guard active — pushes to multica-ai/* will be blocked."
echo "Bypass (rare): ALLOW_MULTICA_AI_PUSH=1 git push ..."
