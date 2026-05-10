#!/usr/bin/env bash
# scripts/workspace.sh — write a sibling-aware go.work above this repo.
#
# Usage: run from any of the 4 sibling clones:
#   <parent>/llm-agent
#   <parent>/llm-agent-providers
#   <parent>/llm-agent-otel
#   <parent>/llm-agent-customer-support
#
# Result: <parent>/go.work points at all 4 modules. The file is gitignored
# in every repo (Pitfall 13). Idempotent — safe to re-run.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/.." && pwd)"
parent_dir="$(cd "$repo_dir/.." && pwd)"

cd "$parent_dir"

modules=()
for d in llm-agent llm-agent-providers llm-agent-otel llm-agent-customer-support; do
  if [ -d "$d" ] && [ -f "$d/go.mod" ]; then
    modules+=("./$d")
  fi
done

if [ "${#modules[@]}" -eq 0 ]; then
  echo "scripts/workspace.sh: no sibling Go modules found in $parent_dir" >&2
  exit 1
fi

# Drop any existing go.work first so re-runs are clean.
rm -f go.work go.work.sum

go work init "${modules[@]}"

echo "wrote $parent_dir/go.work pointing at:"
for m in "${modules[@]}"; do echo "  - $m"; done
echo
echo "go.work is .gitignored in every sibling repo. To revert: rm $parent_dir/go.work"
