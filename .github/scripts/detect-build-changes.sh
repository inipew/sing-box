#!/usr/bin/env bash
set -euo pipefail

if [[ "$EVENT_NAME" == workflow_dispatch ]]; then
  echo 'should_build=true' >> "$GITHUB_OUTPUT"
  exit 0
fi

if [[ "$EVENT_NAME" == push ]]; then
  base="$PUSH_BEFORE"
  head="$(git rev-parse HEAD)"
elif [[ "$EVENT_NAME" == pull_request ]]; then
  base="$PR_BASE"
  head="$PR_HEAD"
else
  echo 'should_build=true' >> "$GITHUB_OUTPUT"
  exit 0
fi

# A new branch or a force push can leave the previous commit unavailable.
# Build in that case so an unknown range never silently skips artifacts.
if [[ ! "$base" =~ ^[0-9a-f]{40}$ || "$base" == 0000000000000000000000000000000000000000 ]] \
  || ! git cat-file -e "$base^{commit}" \
  || ! git cat-file -e "$head^{commit}"; then
  echo 'should_build=true' >> "$GITHUB_OUTPUT"
  exit 0
fi

if ! changed=$(git diff --name-only "$base" "$head"); then
  echo 'should_build=true' >> "$GITHUB_OUTPUT"
  exit 0
fi

if grep -Eq '\.(go|mod|sum)$|^\.github/(workflows|scripts)/' <<< "$changed"; then
  echo 'should_build=true' >> "$GITHUB_OUTPUT"
else
  echo 'should_build=false' >> "$GITHUB_OUTPUT"
fi
