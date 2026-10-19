#!/bin/bash
# Prepare pinned SDK fixtures outside tests; tests never fetch or install.
set -euo pipefail
: "${TMPDIR:?set TMPDIR to disk-backed scratch}"
source_dir=$(go list -m -f '{{.Dir}}' github.com/hollis-labs/plugin-sdk)
target_dir=$(mktemp -d "$TMPDIR/hooks-sdk-source.XXXXXX")
cp -R "$source_dir/." "$target_dir/"
chmod -R u+w "$target_dir"
(cd "$target_dir" && go test -c -o "$target_dir/sdk-child" ./subprocess)
(cd "$target_dir/ts" && npm ci --ignore-scripts && npm run build --workspace @hollis-labs/plugin-sdk)
export SDK_TEST_SOURCE="$target_dir"
export SDK_TEST_GO_CHILD="$target_dir/sdk-child"
printf 'export SDK_TEST_SOURCE=%q\nexport SDK_TEST_GO_CHILD=%q\n' "$SDK_TEST_SOURCE" "$SDK_TEST_GO_CHILD" > "${1:?supply environment receipt path}"
