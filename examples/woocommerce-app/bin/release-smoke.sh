#!/bin/sh
# Release smoke of the trial shop on real Docker, through the factory's own
# deploy path: the deploy role of the Docker request filter, the Compose
# backend, the edge and the release pipeline.
#
# It prepares the shop as its Owner runs it before SISF manages it (a MariaDB
# database, the secrets volume, the uploads volume, WordPress installed and
# seeded with 100 products, 20 customers and 50 orders), builds three bundles
# (the installed release, WooCommerce 11.2.1 -> 11.2.2, and a shop plugin
# change that breaks every page but health), runs the smoke test, then checks
# that the orders that existed before are unchanged, takes a backup, restores
# it into a disposable copy and compares the copy.
#
# Usage: release-smoke.sh [--keep]. Needs OrbStack (containers are reached as
# <name>.orb.local), port 7480 free, and SISF (the sisf binary) on PATH.
set -eu
keep=${1:-}
app=$(cd "$(dirname "$0")/.." && pwd)
root=$(cd "$app/../.." && pwd)
sisf=${SISF:-sisf}
work=${SISF_TRIAL_DIR:-"$HOME/.sisf-trial"}/release-smoke
net=sisf-smoke
db=sisf-smoke-db
copy=sisf-smoke-db-copy
uploads=sisf-smoke-uploads
secrets=sisf-smoke-secrets
files=sisf-smoke-files
edge=127.0.0.1:7480
factory=acme-hello
alpine=alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
cli=wordpress:cli-php8.3@sha256:a4fff017eb08c03187e90fe52d4bff8d3a4d4a5103de05829a6e747c4ca3056e
mariadb=mariadb:11.4@sha256:5e73baf8179b29f115101bb102763a47a017361f3e9792daccba6646c3ab1532
woo_new="plugin woocommerce 11.2.2 a4746183efd951de5837396aad119f7cf508f06cb699ccb590a01cada4c44932"

fail() { echo "release smoke failed: $*" >&2; exit 1; }
now() { perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; }
json() { python3 -I -c "import json,sys; print(json.load(open(sys.argv[1]))$2)" "$1"; }

cleanup() {
	docker rm -f "$db" "$copy" >/dev/null 2>&1 || true
	ids=$(docker ps -aq --filter "label=sisf.factory=$factory")
	[ -z "$ids" ] || docker rm -f $ids >/dev/null
	vols=$(docker volume ls -q --filter "label=sisf.factory=$factory")
	[ -z "$vols" ] || docker volume rm $vols >/dev/null
	docker volume rm "$uploads" "$secrets" "$files" sisf-smoke-db sisf-smoke-db-copy >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
}

if lsof -nP -iTCP:7480 -sTCP:LISTEN >/dev/null 2>&1; then
	fail "port 7480 is in use"
fi
cleanup
rm -rf "$work"
mkdir -p "$work/bundles"
chmod 700 "$work"
t0=$(now)

echo "bundles"
"$sisf" app bundle --spec "$app/sisf-app.yaml" --out "$work/bundles/v1.tar" >/dev/null
for v in v2 bad; do
	mkdir -p "$work/$v"
	(cd "$app" && tar -c --exclude ./build --exclude ./secrets .) | tar -x -C "$work/$v"
	sed -i '' "s/^plugin woocommerce 11\.2\.1 .*/$woo_new/" "$work/$v/content.lock"
done
cat >>"$work/bad/plugins/sisf-shop/sisf-shop.php" <<'EOF'

// A broken change: every page calls a helper that does not exist.
add_action( 'wp_loaded', function () { sisf_shop_missing_helper(); } );
EOF
"$sisf" app bundle --spec "$work/v2/sisf-app.yaml" --out "$work/bundles/v2.tar" >/dev/null
"$sisf" app bundle --spec "$work/bad/sisf-app.yaml" --out "$work/bundles/bad.tar" >/dev/null

echo "owner setup"
docker network create "$net" >/dev/null
docker volume create "$secrets" >/dev/null
docker run --rm --network none -v "$secrets:/s" "$alpine" sh -c \
	'for f in db_password wp_secret; do head -c 32 /dev/urandom | od -An -tx1 | tr -d " \n" >/s/$f; done; chmod 444 /s/*'
docker volume create "$uploads" >/dev/null
docker run --rm --network none -v "$uploads:/u" "$alpine" chown 33:33 /u
docker volume create "$files" >/dev/null
docker run --rm -i --network none -v "$files:/f" "$alpine" tar -x -C /f <"$work/bundles/v1.tar"

start_db() { # name volume alias
	docker run -d --name "$1" --network "$net" --network-alias "$3" \
		-e MARIADB_DATABASE=shop -e MARIADB_USER=shop -e MARIADB_PASSWORD_FILE=/run/secrets/db_password \
		-e MARIADB_RANDOM_ROOT_PASSWORD=1 -v "$secrets:/run/secrets:ro" -v "$2:/var/lib/mysql" "$mariadb" >/dev/null
	i=0
	until docker exec "$1" sh -c 'MYSQL_PWD="$(cat "$MARIADB_PASSWORD_FILE")" mariadb -ushop -e "SELECT 1" shop' >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt 90 ] || fail "database $1 did not start"
		sleep 2
	done
}
wp() { # host args...
	host=$1
	shift
	docker run --rm --network "$net" --user 33:33 --read-only --tmpfs /tmp \
		-e HOME=/tmp -e WORDPRESS_DB_HOST="$host" -e WORDPRESS_HOME="http://$edge" \
		-e WORDPRESS_DB_PASSWORD_FILE=/run/secrets/db_password -e WORDPRESS_SECRET_FILE=/run/secrets/wp_secret \
		-v "$secrets:/run/secrets:ro" -v "$files:/var/www/html:ro" \
		-v "$uploads:/var/www/html/wp-content/uploads" -v "$app/seed:/seed:ro" \
		"$cli" wp "$@"
}
start_db "$db" sisf-smoke-db db
wp db core install --url="http://$edge" --title="SISF trial shop" --admin_user=owner --admin_email=owner@example.test \
	--admin_password="$(openssl rand -hex 16)" --skip-email --quiet
wp db plugin activate woocommerce sisf-sandbox-pay sisf-shop contact-form-7 --quiet
wp db rewrite structure '/%postname%/' --quiet
wp db eval-file /seed/seed.php >/dev/null
wp db eval-file /seed/counts.php >"$work/before.json"
[ "$(json "$work/before.json" '["orders"]')" = 50 ] || fail "seed: $(cat "$work/before.json")"
max=$(json "$work/before.json" '["order_ids"][-1]')

echo "release smoke"
t1=$(now)
SISF_SHOP_BUNDLES="$work/bundles" SISF_SHOP_NETWORK="$net" SISF_SHOP_UPLOADS="$uploads" SISF_SHOP_SECRETS="$secrets" \
	SISF_SHOP_EDGE="$edge" SISF_DOCKER_SOCK="${SISF_DOCKER_SOCK:-$HOME/.orbstack/run/docker.sock}" \
	go -C "$root" test -tags smoke -run TestManagedShopReleasesAndRollsBackOnRealDocker -count=1 -v -timeout 60m \
	./internal/supervisor/release/ 2>&1 | tee "$work/smoke.log" | grep -E '^(=== RUN|--- |\s+managed_app_live_test|ok|FAIL|PASS)' || true
grep -q '^--- PASS: TestManagedShopReleasesAndRollsBackOnRealDocker' "$work/smoke.log" || fail "the smoke test did not pass; see $work/smoke.log"
t2=$(now)

echo "data"
wp db eval-file /seed/counts.php >"$work/after.json"
wp db eval-file /seed/counts.php "$max" >"$work/after-original.json"
before_sha=$(json "$work/before.json" '["orders_sha256"]')
[ "$(json "$work/after-original.json" '["orders_sha256"]')" = "$before_sha" ] || fail "the original orders changed"
[ "$(json "$work/after.json" '["orders"]')" = 51 ] || fail "want the 50 orders and the purchase: $(cat "$work/after.json")"
for k in products customers; do
	[ "$(json "$work/after.json" "[\"$k\"]")" = "$(json "$work/before.json" "[\"$k\"]")" ] || fail "$k changed"
done

echo "backup and restore"
docker exec "$db" sh -c 'set -eu; MYSQL_PWD="$(cat "$MARIADB_PASSWORD_FILE")" exec mariadb-dump --single-transaction --quick --routines --triggers --hex-blob -ushop shop' >"$work/backup.sql"
backup_sha=$(shasum -a 256 "$work/backup.sql" | awk '{print $1}')
start_db "$copy" sisf-smoke-db-copy "$copy"
docker exec -i "$copy" sh -c 'set -eu; MYSQL_PWD="$(cat "$MARIADB_PASSWORD_FILE")" exec mariadb -ushop shop' <"$work/backup.sql"
wp "$copy" eval-file /seed/counts.php >"$work/restored.json"
[ "$(json "$work/restored.json" '["orders_sha256"]')" = "$(json "$work/after.json" '["orders_sha256"]')" ] || fail "the restored copy differs"
rm -f "$work/backup.sql"

python3 -I - "$work" "$backup_sha" "$((t2 - t1))" "$(( $(now) - t0 ))" >>"${SISF_TRIAL_DIR:-"$HOME/.sisf-trial"}/release-smoke.jsonl" <<'PY'
import json, sys, time
work, backup_sha, smoke_ms, total_ms = sys.argv[1:]
load = lambda n: {k: v for k, v in json.load(open(f"{work}/{n}.json")).items() if k != "order_ids"}
log = open(f"{work}/smoke.log").read()
print(json.dumps({"at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "smoke_ms": int(smoke_ms), "total_ms": int(total_ms),
    "before": load("before"), "after": load("after"), "original_orders_unchanged": True,
    "backup_sha256": backup_sha, "restored_matches": True,
    "log": [l.strip() for l in log.splitlines() if "managed_app_live_test" in l]}))
PY
echo "release smoke passed in $(( ($(now) - t0) / 1000 )) s"
[ "$keep" = "--keep" ] || cleanup
