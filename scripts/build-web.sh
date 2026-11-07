#!/bin/sh
# Builds the browser client the server serves (GREENGRAM_WEB_DIR):
#
#   scripts/build-web.sh <out dir>
#
# Telegram Web A (Ajaxy/telegram-tt, GPL-3.0) at the commit pinned below,
# downloaded and checked, plus web/telegram-web-a.patch, which points it at
# this server, and web/brand/: Greengram's icons, name and description.
# Writes <out dir>/web.
#
#   scripts/build-web.sh <greengram-web checkout> <out dir>
#
# The owners' form: Web A from that checkout's vendor/telegram-web-a instead of
# the download.
#
# The admin panel is a separate build: scripts/build-admin.sh.
#
set -eu

if ! command -v bun >/dev/null 2>&1; then
	echo "bun is needed for the web client: https://bun.sh" >&2
	exit 1
fi

WEBA_REPO=https://codeload.github.com/Ajaxy/telegram-tt/tar.gz
WEBA_COMMIT=6002824b6532f3d913997ce496d9ab5f9b86bc22 # 12.0.48
WEBA_SHA256=b090f56cfe7bd8ae2adb3482bc2bb574b1d0886f68db5a0789581176af5df74d

case $# in
1) src= out_arg=$1 ;;
2) src=$(cd "$1" && pwd) out_arg=$2 ;;
*)
	echo "usage: $0 <out dir>" >&2
	echo "       $0 <greengram-web checkout> <out dir>" >&2
	exit 2
	;;
esac
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out_arg"
out=$(cd "$out_arg" && pwd)
work=${GREENGRAM_BUILD_DIR:-$out/.build}

# Web A
rm -rf "$work"
mkdir -p "$work/webapp"
if [ -n "$src" ]; then
	tar -C "$src/vendor/telegram-web-a" --exclude=./dist --exclude=./node_modules -cf - . | tar -C "$work/webapp" -xf -
else
	echo "downloading Web A $WEBA_COMMIT"
	curl -fsSL -o "$work/weba.tar.gz" "$WEBA_REPO/$WEBA_COMMIT"
	got=$(sha256sum "$work/weba.tar.gz" | cut -d' ' -f1)
	if [ "$got" != "$WEBA_SHA256" ]; then
		echo "Web A download has sha256 $got, expected $WEBA_SHA256" >&2
		exit 1
	fi
	tar -C "$work/webapp" --strip-components=1 -xzf "$work/weba.tar.gz"
	rm -rf "$work/webapp/dist"
	rm "$work/weba.tar.gz"
fi
bun "$here/scripts/apply-web-patch.mjs" "$here/web/telegram-web-a.patch" "$work/webapp"
(
	cd "$work/webapp"
	bun --bun install --frozen-lockfile
	# the server doesn't check api_id; it only shows up in the sessions list.
	# BASE_URL stays Telegram's: vite reads it as an asset path in index.html
	# when it's relative, and it only feeds og:url and "other versions" links,
	# which brand() takes out below.
	APP_ENV=production \
		TELEGRAM_API_ID=2496 TELEGRAM_API_HASH=greengram \
		APP_TITLE=Greengram APP_NAME=Greengram bun run --bun vite build
)
rm -rf "$out/web"
cp -R "$work/webapp/dist" "$out/web"
find "$out/web" -name '*.map' -delete
# build reports, and Telegram's Google site verification file: served from
# another domain it would let Telegram's Search Console account claim it
rm -f "$out/web"/google*.html "$out/web/statoscope-report.html" "$out/web/build-stats.json"

# Greengram's icons over Web A's, under every name Web A links them by, then
# the name and description in the manifests and index.html.
b=$here/web/brand
w=$out/web
for f in favicon.svg favicon-unread.svg favicon.ico favicon-16x16.png favicon-32x32.png mstile-150x150.png; do
	cp "$b/$f" "$w/$f"
done
cp "$b/apple-touch-icon.png" "$w/apple-touch-icon.png"
cp "$b/apple-touch-icon.png" "$w/apple-touch-icon-dev.png"
for n in 192 384 512; do
	for k in icon icon-dev icon-square icon-square-dev; do
		cp "$b/icon-${n}x$n.png" "$w/$k-${n}x$n.png"
	done
done
cp "$b/icon-512x512.png" "$w/icon-electron-macos.png"
bun "$here/scripts/brand-web.mjs" "$w"

rm -rf "$work"
echo "built $out/web"
echo "run with GREENGRAM_WEB_DIR=$out/web"
