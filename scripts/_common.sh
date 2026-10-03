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

# Print the module directories (top-level directories holding a go.mod), one per line.
modules() {
  local f
  for f in "$REPO_ROOT"/*/go.mod; do
    [ -e "$f" ] || continue
    basename "$(dirname "$f")"
  done
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
