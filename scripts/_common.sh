# shellcheck shell=bash
# Shared helpers for the scripts in this directory. Source it; do not run it.

set -euo pipefail

# A user's CDPATH makes `cd` print the directory, which corrupts $(cd ...) captures.
unset CDPATH

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=repo.conf
. "$SCRIPT_DIR/repo.conf"

# Each module is built with its own go.mod only. A developer's go.work must not leak in.
export GOWORK=off

die() {
  echo "error: $*" >&2
  exit 1
}

# Print the groups, one per line, in rank order.
groups() {
  local pair
  for pair in $GROUP_RANKS; do
    echo "$pair"
  done | sort -t: -k2,2n -k1,1 | cut -d: -f1
}

# group_rank GROUP: print the rank of GROUP, or nothing when it is not a group.
group_rank() {
  local pair
  for pair in $GROUP_RANKS; do
    if [ "${pair%%:*}" = "$1" ]; then
      echo "${pair#*:}"
      return 0
    fi
  done
  return 0
}

is_group() {
  [ -n "$(group_rank "$1")" ]
}

is_legacy_flat() {
  local m
  for m in $LEGACY_FLAT_MODULES; do
    [ "$m" = "$1" ] && return 0
  done
  return 1
}

# Print the module directories relative to the repository root, one per line:
# <group>/<module> for every <group>/<module>/go.mod, then each legacy flat <module>.
modules() {
  local g f
  for g in $(groups); do
    for f in "$REPO_ROOT/$g"/*/go.mod; do
      [ -e "$f" ] || continue
      echo "$g/$(basename "$(dirname "$f")")"
    done
  done
  for g in $LEGACY_FLAT_MODULES; do
    [ -e "$REPO_ROOT/$g/go.mod" ] || continue
    echo "$g"
  done
}

# valid_module NAME: succeeds when NAME is a module directory that exists here.
valid_module() {
  [ -f "$REPO_ROOT/$1/go.mod" ] && modules | grep -qxF -- "$1"
}

# Print what a commit could contain: tracked files plus untracked files that are not ignored.
repo_files() {
  git -C "$REPO_ROOT" ls-files --cached --others --exclude-standard
}

# Escape the dots in a module path for use inside an extended regular expression.
# Module paths hold only letters, digits, '.', '-' and '/', so dots are the only metacharacter.
re_escape() {
  printf '%s' "$1" | sed 's/\./\\./g'
}
