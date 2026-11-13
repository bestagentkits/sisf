#!/bin/sh
# Runs one onboarding cycle of the trial shop and records how long each step
# took: adopt the Compose project, assemble the content bundle, load it into
# a digest-named volume, start the shop with fresh secrets, install and seed
# it, check the seeded counts and run every check, purchase included. The
# shop of a cycle is disposable: it is removed at the end unless --keep.
#
# Usage: bin/onboard-cycle.sh <cycle> [--keep]
# Environment: SISF (the sisf binary, default: sisf on PATH), SHOP_PORT
# (default 7481), SISF_TRIAL_DIR (where results go, default
# ~/.sisf-trial). Secrets are generated per cycle, kept in a 0700 directory
# and never printed.
set -eu
cycle=${1:?usage: onboard-cycle.sh <cycle> [--keep]}
keep=${2:-}
app=$(cd "$(dirname "$0")/.." && pwd)
sisf=${SISF:-sisf}
port=${SHOP_PORT:-7481}
trial=${SISF_TRIAL_DIR:-"$HOME/.sisf-trial"}
proj="sisf-trial-c$cycle"
work="$trial/$proj"
url="http://127.0.0.1:$port"
alpine=alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
cli=wordpress:cli-php8.3@sha256:a4fff017eb08c03187e90fe52d4bff8d3a4d4a5103de05829a6e747c4ca3056e
uploads="$proj-uploads"

now() { perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; }
step_start=0
begin() { step_start=$(now); printf '%-10s ' "$1"; }
end() { ms=$(( $(now) - step_start )); echo "$ms ms"; eval "t_$1=$ms"; }
fail() { echo "cycle $cycle failed: $*" >&2; exit 1; }

rm -rf "$work"
mkdir -p "$work"
chmod 700 "$work"
t0=$(now)

begin adopt
"$sisf" adopt --stdout --name shop "$app" >"$work/sisf-app.yaml"
# Adoption is deterministic: it must reproduce the committed Spec.
diff -q "$work/sisf-app.yaml" "$app/sisf-app.yaml" >/dev/null || fail "adopt differs from the committed sisf-app.yaml"
end adopt

begin bundle
"$sisf" app bundle --spec "$app/sisf-app.yaml" --out "$work/bundle.tar" >"$work/bundle.txt"
digest=$(awk '{print $1}' "$work/bundle.txt" | sed 's/^sha256://')
echo "$digest" | grep -Eq '^[0-9a-f]{64}$' || fail "bundle digest: $(cat "$work/bundle.txt")"
bundle_vol="sisf-bundle-$(echo "$digest" | cut -c1-16)"
end bundle

begin load
if ! docker volume inspect "$bundle_vol" >/dev/null 2>&1; then
	docker volume create --label sisf.bundle="sha256:$digest" "$bundle_vol" >/dev/null
	docker run --rm -i --network none -v "$bundle_vol:/b" "$alpine" tar -x -C /b <"$work/bundle.tar"
fi
docker volume create "$uploads" >/dev/null
docker run --rm --network none -v "$uploads:/u" "$alpine" chown 33:33 /u
end load

mkdir -p "$work/secrets"
openssl rand -hex 24 >"$work/secrets/db_password"
openssl rand -hex 32 >"$work/secrets/wp_secret"
# The containers run as other users; the 0700 parent keeps the files private.
chmod 644 "$work/secrets/db_password" "$work/secrets/wp_secret"

cat >"$work/override.yaml" <<EOF
services:
  wordpress:
    ports: !override ["127.0.0.1:$port:80"]
    environment:
      WORDPRESS_HOME: $url
    volumes: !override
      - bundle:/var/www/html:ro
      - uploads:/var/www/html/wp-content/uploads
volumes:
  bundle: {external: true, name: $bundle_vol}
  uploads: {external: true, name: $uploads}
secrets:
  db_password: {file: $work/secrets/db_password}
  wp_secret: {file: $work/secrets/wp_secret}
EOF
compose() { docker compose -p "$proj" -f "$app/compose.yaml" -f "$work/override.yaml" "$@"; }
wp() {
	docker run --rm --network "${proj}_default" --user 33:33 --read-only --tmpfs /tmp \
		-e HOME=/tmp -e WORDPRESS_DB_HOST=db -e WORDPRESS_HOME="$url" \
		-e WORDPRESS_DB_PASSWORD_FILE=/run/secrets/db_password -e WORDPRESS_SECRET_FILE=/run/secrets/wp_secret \
		-v "$work/secrets:/run/secrets:ro" -v "$bundle_vol:/var/www/html:ro" \
		-v "$uploads:/var/www/html/wp-content/uploads" -v "$app/seed:/seed:ro" \
		"$cli" "$@"
}

begin start
compose up -d --quiet-pull >/dev/null 2>&1 || fail "compose up"
i=0
until curl -fsS -o /dev/null --max-time 10 "$url/healthz"; do
	i=$((i + 1))
	[ $i -lt 120 ] || fail "the shop did not become healthy"
	sleep 2
done
end start

begin install
wp core install --url="$url" --title="SISF trial shop" --admin_user=owner --admin_email=owner@example.test \
	--admin_password="$(openssl rand -hex 16)" --skip-email --quiet
wp plugin activate woocommerce sisf-sandbox-pay sisf-shop contact-form-7 --quiet
wp rewrite structure '/%postname%/' --quiet
end install

begin seed
wp eval-file /seed/seed.php >/dev/null
end seed

begin verify
wp eval-file /seed/counts.php >"$work/counts.json"
counts=$(sed -E 's/"order_ids":\[[^]]*\],?//' "$work/counts.json")
echo "$counts" | grep -q '"products":100' || fail "products: $counts"
echo "$counts" | grep -q '"customers":20' || fail "customers: $counts"
echo "$counts" | grep -q '"orders":50' || fail "orders: $counts"
end verify

begin checks
checks_ok=true
"$sisf" app check --spec "$app/sisf-app.yaml" --url "$url" --writes --json >"$work/checks.jsonl" || checks_ok=false
end checks

total=$(( $(now) - t0 ))
mkdir -p "$trial"
printf '{"cycle":%s,"at":"%s","bundle":"sha256:%s","ms":{"adopt":%s,"bundle":%s,"load":%s,"start":%s,"install":%s,"seed":%s,"verify":%s,"checks":%s,"total":%s},"checks_passed":%s,"load_avg":"%s","counts":%s,"checks":[%s]}\n' \
	"$cycle" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$digest" "$t_adopt" "$t_bundle" "$t_load" "$t_start" "$t_install" "$t_seed" "$t_verify" "$t_checks" "$total" \
	"$checks_ok" "$(sysctl -n vm.loadavg | tr -d '{}' | xargs)" "$counts" "$(paste -sd, "$work/checks.jsonl")" >>"$trial/cycles.jsonl"
echo "total      $total ms, checks passed: $checks_ok"

if [ "$keep" != "--keep" ]; then
	compose down -v >/dev/null 2>&1
	docker volume rm "$uploads" >/dev/null
	rm -rf "$work/secrets"
fi
