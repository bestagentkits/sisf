#!/usr/bin/env bash
# Entry point of the SISF trial container: create the install once, publish
# /healthz through nginx, then run the Standalone factory in the foreground.
#
# Profile, chosen by the environment (names only; values are never printed):
#   GH_TOKEN set      development install (--dev) against SISF_REPO, the
#                     factory acting as the token's account through gh.
#   GH_TOKEN not set  simulation profile: fake GitHub, fake sign-in, scripted
#                     agent; proves install and health with no credential.
set -euo pipefail

SISF_REPO="${SISF_REPO:-bestagentkits/sisf-sandbox}"
SISF_HOME="${SISF_HOME:-/data/factory}"
SRC=/opt/sisf/src
export SISF_HOME GOCACHE="${GOCACHE:-/data/cache/go-build}"

if [[ "$SISF_REPO" != "bestagentkits/sisf-sandbox" ]]; then
  echo "trial: refusing SISF_REPO other than bestagentkits/sisf-sandbox" >&2
  exit 2
fi

echo "trial: $(sisf version)"
echo "trial: $(uname -sm), docker CLI: $(command -v docker || echo absent), docker socket: $([[ -S /var/run/docker.sock ]] && echo present || echo absent)"
data_dir="$(dirname "$SISF_HOME")"
if [[ ! -w "$data_dir" ]]; then
  echo "trial: $data_dir is not writable by uid $(id -u); give the volume to uid 10001 (or group 10001) and start again" >&2
  exit 1
fi
mkdir -p "$GOCACHE"

if [[ ! -f "$SISF_HOME/factory.yaml" ]]; then
  cd "$SRC"
  if [[ -n "${GH_TOKEN:-}" ]]; then
    echo "trial: creating a development install for $SISF_REPO (GH_TOKEN is set)"
    sisf init --dev --mode standalone --dir "$SISF_HOME" --repo "$SISF_REPO" \
      --github-credential gh-cli --budget-cap "${SISF_BUDGET_CAP:-5}" \
      --alert-threshold "${SISF_ALERT_THRESHOLD:-4}"
  else
    echo "trial: creating a simulation install for $SISF_REPO (GH_TOKEN is not set)"
    sisf init --profile sim --mode standalone --dir "$SISF_HOME" --repo "$SISF_REPO" \
      --confirm-unlimited --no-alert
  fi
fi

nginx -c "$SRC/deploy/tose/nginx.conf"
cd "$SRC"
exec sisf up --dir "$SISF_HOME"
