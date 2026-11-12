#!/bin/sh
# The QA agent's launcher of the smoke run: the program the supervisor's
# --qa-agent-binary names. Under --exec docker the supervisor starts no QA
# agent as its own OS user, which can read the trust directory; it starts
# this launcher instead, as it would start codex: codex's arguments, the
# prompt on standard input, JSON lines on standard output.
#
# It runs codex in a Docker container (qa-codex.Dockerfile) that sees:
#   - the QA workspace of the candidate (the -C directory), read-only;
#   - the operator's codex login file, read-only, copied into a private
#     CODEX_HOME inside the container, so the login on the host is never
#     changed;
#   - OPENAI_API_KEY and CODEX_API_KEY, by name, when the supervisor passes
#     them.
# Nothing of the install directory (secrets, state, trust) and nothing else
# of the operator's home is mounted.
#
# Codex keeps its own read-only sandbox for the commands it runs. That
# sandbox (bubblewrap) needs user namespaces, which Docker's default seccomp
# profile blocks, so the container runs with seccomp unconfined; everything
# else is held down: every capability dropped, no-new-privileges, a
# read-only root, a non-root user, memory and process limits. Its network is
# Docker's default bridge, because codex must reach its API: the QA agent's
# egress is not restricted by this launcher.
#
# The harness writes a wrapper that sets, and then execs this script:
#   SISF_SMOKE_DOCKER       absolute path of the docker CLI
#   SISF_SMOKE_QA_IMAGE     the image built from qa-codex.Dockerfile
#   SISF_SMOKE_QA_LABEL     run label; every container carries sisf.smoke=<label>
#   SISF_SMOKE_CODEX_AUTH   absolute path of the codex login file (optional)
#   DOCKER_HOST             the Docker endpoint (optional)
set -eu
docker=${SISF_SMOKE_DOCKER:?}
image=${SISF_SMOKE_QA_IMAGE:?}
label=${SISF_SMOKE_QA_LABEL:?}
auth=${SISF_SMOKE_CODEX_AUTH:-}

# The workspace is the argument after -C; the supervisor always passes an
# absolute path of plain characters.
workspace=/tmp
prev=
for arg in "$@"; do
	if [ "$prev" = "-C" ]; then
		workspace=$arg
	fi
	prev=$arg
done
case $workspace in
/*) ;;
*)
	echo "qa-codex-launcher: the workspace must be an absolute path" >&2
	exit 64
	;;
esac
case $workspace in
*[!A-Za-z0-9_./+-]*)
	echo "qa-codex-launcher: the workspace path has characters this launcher does not mount" >&2
	exit 64
	;;
esac
mount_workspace=
if [ "$workspace" != /tmp ]; then
	mount_workspace=$workspace:$workspace:ro
fi
mount_auth=
if [ -n "$auth" ] && [ -f "$auth" ]; then
	mount_auth=$auth:/run/sisf/codex-auth.json:ro
fi

# Run by the shell inside the container: a private, writable CODEX_HOME
# holding a copy of the login, then codex itself.
# shellcheck disable=SC2016 # expanded inside the container, not here
inside='mkdir -p "$CODEX_HOME" && if [ -f /run/sisf/codex-auth.json ]; then cp /run/sisf/codex-auth.json "$CODEX_HOME/auth.json"; fi && exec codex "$@"'

exec "$docker" run --rm -i --name "$label-qa-$$" --label "sisf.smoke=$label" \
	--cap-drop ALL --security-opt no-new-privileges --security-opt seccomp=unconfined \
	--read-only --tmpfs /tmp:rw,size=512m --tmpfs /home/qa:rw,uid=1000,gid=1000,mode=700,size=256m \
	--user 1000:1000 --memory 4g --pids-limit 512 \
	-e HOME=/home/qa -e CODEX_HOME=/home/qa/.codex -e OPENAI_API_KEY -e CODEX_API_KEY \
	${mount_workspace:+-v} ${mount_workspace:+"$mount_workspace"} \
	${mount_auth:+-v} ${mount_auth:+"$mount_auth"} \
	-w "$workspace" "$image" \
	sh -c "$inside" codex "$@"
