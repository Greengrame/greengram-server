#!/bin/sh
# Builds the admin panel the server serves at /admin (GREENGRAM_ADMIN_DIR):
#
#   scripts/build-admin.sh <out dir>
#
# Installs admin/'s packages with bun (pinned by admin/bun.lock), bundles
# admin/src and writes <out dir>/admin: index.html and admin-assets/.
# Needs bun 1.4 or newer. Takes under a minute and a few hundred MB of memory.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: $0 <out dir>" >&2
	exit 2
fi
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
out=$(cd "$1" && pwd)/admin

if ! command -v bun >/dev/null 2>&1; then
	echo "bun is needed for the admin panel: https://bun.sh" >&2
	exit 1
fi

cd "$here/admin"
bun install --frozen-lockfile
rm -rf "$out"
bun build.js "$out/admin-assets"
cp admin.html "$out/index.html"
cp "$here/third_party/admin-panel/NOTICE" "$out/admin-assets/NOTICE.txt"
echo "built $out"
echo "run with GREENGRAM_ADMIN_DIR=$out"
