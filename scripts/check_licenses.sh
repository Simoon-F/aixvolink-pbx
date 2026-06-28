#!/bin/sh
set -eu

GO_LICENSES=${GO_LICENSES:-go-licenses}

command -v "$GO_LICENSES" >/dev/null 2>&1 || {
  echo "go-licenses is required; run 'make tools'" >&2
  exit 1
}

"$GO_LICENSES" check ./spikes/... \
  --ignore github.com/Simoon-F/aixvolink-pbx \
  --allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,MPL-2.0
