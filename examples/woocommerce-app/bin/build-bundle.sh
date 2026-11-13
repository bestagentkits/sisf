#!/bin/sh
# Builds the shop's content bundle: the document root the runtime image
# serves. It downloads every archive content.lock names, refuses one whose
# sha256 differs from the pin, and adds the shop's own plugins and settings.
# Nothing downloaded is executed. The output is build/bundle (a directory)
# and build/bundle.tar, a tar with fixed order, owners and times, so the same
# inputs always give the same digest.
#
# Usage: bin/build-bundle.sh [cache-dir]
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
cache=${1:-"$here/build/cache"}
out="$here/build/bundle"
mkdir -p "$cache"
rm -rf "$out" "$here/build/bundle.tar"
mkdir -p "$out"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

fetch() { # kind slug version sha
	case $1 in
	core) url="https://downloads.wordpress.org/release/wordpress-$3.zip" ;;
	plugin) url="https://downloads.wordpress.org/plugin/$2.$3.zip" ;;
	*) echo "content.lock: unknown kind $1" >&2; exit 2 ;;
	esac
	file="$cache/$1-$2-$3.zip"
	if [ ! -f "$file" ] || [ "$(sha256 "$file")" != "$4" ]; then
		curl -fsSL --retry 3 -o "$file.part" "$url"
		mv "$file.part" "$file"
	fi
	got=$(sha256 "$file")
	if [ "$got" != "$4" ]; then
		echo "content.lock: $2 $3 has sha256 $got, the pin is $4" >&2
		exit 1
	fi
	echo "$file"
}

cores=0
grep -v '^[[:space:]]*#' "$here/content.lock" | grep -v '^[[:space:]]*$' | while read -r kind slug version sum; do
	zip=$(fetch "$kind" "$slug" "$version" "$sum")
	case $kind in
	core)
		tmp="$out.core"; rm -rf "$tmp"; mkdir -p "$tmp"
		unzip -q "$zip" -d "$tmp"
		cp -R "$tmp/wordpress/." "$out/"
		rm -rf "$tmp"
		# Only the shop's plugins run: drop the samples the core ships.
		rm -rf "$out/wp-content/plugins/akismet" "$out/wp-content/plugins/hello.php"
		;;
	plugin)
		mkdir -p "$out/wp-content/plugins"
		unzip -q "$zip" -d "$out/wp-content/plugins"
		;;
	esac
done
[ -f "$out/wp-settings.php" ] || { echo "content.lock names no core" >&2; exit 1; }

mkdir -p "$out/wp-content/plugins" "$out/wp-content/mu-plugins" "$out/wp-content/uploads"
for p in "$here"/plugins/*/; do cp -R "$p" "$out/wp-content/plugins/"; done
cp "$here"/mu-plugins/*.php "$out/wp-content/mu-plugins/"
cp "$here/wp-config.php" "$out/wp-config.php"
cp "$here/htaccess" "$out/.htaccess"
chmod -R a+rX,go-w "$out"

# A reproducible tar: sorted names, numeric owner 0, a fixed time.
(cd "$out" && find . -print | LC_ALL=C sort | tar --no-recursion --numeric-owner --owner=0 --group=0 \
	--mtime='2026-01-01 00:00:00Z' -cf "$here/build/bundle.tar" -T - 2>/dev/null) ||
	(cd "$out" && find . -print | LC_ALL=C sort | COPYFILE_DISABLE=1 bsdtar --no-recursion --uid 0 --gid 0 --uname root --gname root -cf "$here/build/bundle.tar" -T -)
echo "bundle sha256:$(sha256 "$here/build/bundle.tar")"
