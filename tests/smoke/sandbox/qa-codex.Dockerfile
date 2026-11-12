# The container the smoke run's QA agent, codex, runs in (see
# qa-codex-launcher.sh). Node and the Codex CLI, with bubblewrap for Codex's
# own read-only sandbox, and the tools it reads a workspace with. It holds
# no secret: the operator's codex login is mounted read-only at run time.
FROM node:22-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d
ARG CODEX_VERSION
RUN test -n "$CODEX_VERSION" && npm install -g "@openai/codex@${CODEX_VERSION}" \
	&& apt-get update -qq && apt-get install -y -qq --no-install-recommends bubblewrap git ripgrep ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
USER 1000:1000
CMD ["codex", "--version"]
