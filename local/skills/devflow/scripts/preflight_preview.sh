#!/usr/bin/env bash
set -euo pipefail
root="${1:-.}"
resolved_root="$(cd "$root" && pwd -P)"
if ! git_root="$(git -C "$resolved_root" rev-parse --show-toplevel 2>/dev/null)"; then
  echo "ERROR: current directory is not inside a Git repository"
  exit 1
fi
branch="$(git -C "$resolved_root" branch --show-current)"
if [[ -z "$branch" ]]; then
  echo "ERROR: detached HEAD"
  exit 1
fi
echo "project_root=$resolved_root"
echo "git_root=$git_root"
echo "branch=$branch"
if [[ "$git_root" != "$resolved_root" ]]; then
  echo "current_directory_is_git_root=false"
fi
if [[ -n "$(git -C "$resolved_root" status --porcelain)" ]]; then
  echo "worktree=dirty"
  git -C "$resolved_root" status --short
  exit 2
fi
echo "worktree=clean"
if git -C "$resolved_root" remote get-url origin >/dev/null 2>&1; then
  echo "origin=present"
else
  echo "origin=missing"
fi
