#!/usr/bin/env bash
# Materialize github.com/criyle/go-sandbox at the version in go.mod and apply
# patches/allow-open-dir.patch. go.mod replaces the module with that tree.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

mod="github.com/criyle/go-sandbox"
ver="$(go mod edit -json | python3 -c '
import json, sys
mod = sys.argv[1]
for req in json.load(sys.stdin)["Require"]:
    if req["Path"] == mod:
        print(req["Version"])
        break
else:
    sys.exit("go.mod does not require %s" % mod)
' "$mod")"
dest="${root}/third_party/go-sandbox"
patch_file="${root}/patches/allow-open-dir.patch"

src="$(go mod download -json "${mod}@${ver}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["Dir"])')"
rm -rf "$dest"
mkdir -p "$dest"
cp -a "${src}/." "$dest/"
chmod -R u+w "$dest"
if ! patch -d "$dest" -p1 --forward --batch < "$patch_file"; then
	echo "failed to apply ${patch_file} to ${mod}@${ver}" >&2
	exit 1
fi
