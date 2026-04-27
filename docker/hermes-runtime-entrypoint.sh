#!/usr/bin/env bash
# Multica Hermes Cloud Runtime — entrypoint
# Bootstraps daemon auth config from K8s Secret, configures hermes provider,
# then execs `multica daemon start --foreground`.
set -euo pipefail

CONFIG_DIR="${HOME}/.multica"
SECRET_CONFIG="/etc/multica/config.json"

mkdir -p "${CONFIG_DIR}"

# ---------- Multica daemon auth ----------
if [ -f "${SECRET_CONFIG}" ]; then
    cp "${SECRET_CONFIG}" "${CONFIG_DIR}/config.json"
    chmod 600 "${CONFIG_DIR}/config.json"
    echo "[entrypoint] Loaded multica config from ${SECRET_CONFIG}"

    # Sanity check: token must be present
    if ! jq -e '.token | length > 0' "${CONFIG_DIR}/config.json" > /dev/null; then
        echo "[entrypoint] ERROR: config.json has no token" >&2
        exit 1
    fi

elif [ -n "${MULTICA_TOKEN:-}" ]; then
    # Fallback: build config.json from env vars
    cat > "${CONFIG_DIR}/config.json" <<EOF
{
  "server_url": "${MULTICA_SERVER_URL_HTTPS:-https://multica-api.mindops.net}",
  "app_url": "${MULTICA_APP_URL:-https://multica.mindops.net}",
  "workspace_id": "${MULTICA_WORKSPACE_ID:-}",
  "token": "${MULTICA_TOKEN}",
  "watched_workspaces": []
}
EOF
    chmod 600 "${CONFIG_DIR}/config.json"
    echo "[entrypoint] Built multica config from MULTICA_TOKEN env"
else
    echo "[entrypoint] ERROR: ${SECRET_CONFIG} not mounted and MULTICA_TOKEN not set" >&2
    exit 1
fi

# ---------- Hermes runtime install (PVC-backed, idempotent) ----------
# Install hermes-agent into ${HOME}/.local on first start. PVC at ${HOME}/.hermes
# persists state, but the binary itself lives at ${HOME}/.local/bin/hermes which
# is inside the container's writable layer — re-installs on every fresh pod
# (acceptable cost: ~30-60s once per pod restart, matches Multica's lokal flow).
HERMES_HOME="${HOME}/.hermes"
HERMES_BIN="${HOME}/.local/bin/hermes"
mkdir -p "${HERMES_HOME}" "${HOME}/.local/bin"

if [ ! -x "${HERMES_BIN}" ]; then
    echo "[entrypoint] Installing hermes-agent (Nous Research) into ${HOME}/.local ..."
    # Skip optional Playwright browser install — adds ~500MB and fails in
    # restricted networks. Browser tools (web_search via Chromium) won't work,
    # but daemon/ACP/code execution all do — and codex gateway can substitute
    # web search if needed.
    export HERMES_SKIP_PLAYWRIGHT=1
    export HERMES_NO_BROWSER=1

    # Run installer with stdin redirected from /dev/null to suppress
    # interactive setup wizard at the end (it tries to open /dev/tty which
    # doesn't exist in container, causing exit 1 even though binary is installed).
    # We don't `&& exit` on failure — installer return code is unreliable
    # because of the setup wizard step. Truth is: did binary land at HERMES_BIN?
    curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh \
        | HERMES_VERSION="${HERMES_VERSION:-latest}" bash < /dev/null \
        || echo "[entrypoint] WARN: installer returned non-zero (likely setup wizard /dev/tty fail — checking binary anyway)"

    if [ ! -x "${HERMES_BIN}" ]; then
        echo "[entrypoint] ERROR: hermes binary not found at ${HERMES_BIN} after install attempt" >&2
        exit 1
    fi
    echo "[entrypoint] hermes installed: $(${HERMES_BIN} --version 2>&1 | head -1)"
else
    echo "[entrypoint] hermes already installed: $(${HERMES_BIN} --version 2>&1 | head -1)"
fi

# ---------- Hermes provider config (codex.mindops.net gateway, OpenAI-compatible) ----------

# Hermes-agent's OpenAI provider uses OPENAI_BASE_URL + OPENAI_API_KEY env vars
# directly (OpenAI SDK convention). Setting these env vars in K8s deployment is
# usually enough — no `hermes login` needed.
#
# Belt-and-suspenders: still call `hermes login openai` on first run to register
# the provider in hermes config (in case some flows read from config.toml not env).
if [ -n "${OPENAI_API_KEY:-}" ] && [ ! -f "${HERMES_HOME}/.bootstrap-complete" ]; then
    echo "[entrypoint] Bootstrapping hermes-agent provider config (first run, OpenAI-compatible)..."
    echo "[entrypoint] Provider base URL: ${OPENAI_BASE_URL:-(default api.openai.com)}"

    # Try various flag combinations — Hermes CLI flag names vary across versions.
    if /home/multica/.local/bin/hermes login openai --api-key "${OPENAI_API_KEY}" --base-url "${OPENAI_BASE_URL:-}" 2>/dev/null; then
        echo "[entrypoint] hermes login openai (with --base-url) succeeded"
    elif /home/multica/.local/bin/hermes login openai --api-key "${OPENAI_API_KEY}" 2>/dev/null; then
        echo "[entrypoint] hermes login openai --api-key succeeded (base URL via env var)"
    else
        echo "${OPENAI_API_KEY}" | /home/multica/.local/bin/hermes login openai \
            || echo "[entrypoint] WARN: hermes login openai via stdin failed; will rely on OPENAI_* env vars"
    fi

    # Set default model — gateway exposes claude-sonnet-4-5 as model name.
    /home/multica/.local/bin/hermes model openai claude-sonnet-4-5 2>/dev/null \
        || echo "[entrypoint] WARN: hermes model select failed (will fall back to provider default)"

    touch "${HERMES_HOME}/.bootstrap-complete"
fi

# ---------- Start daemon ----------
echo "[entrypoint] PATH=${PATH}"
echo "[entrypoint] hermes binary: $(which hermes 2>/dev/null || echo NOT_FOUND)"
echo "[entrypoint] multica binary: $(which multica 2>/dev/null || echo NOT_FOUND)"
echo "[entrypoint] daemon ID: ${MULTICA_DAEMON_ID:-default}"
echo "[entrypoint] server URL: ${MULTICA_SERVER_URL:-default}"
echo "[entrypoint] Starting multica daemon (foreground)..."

exec /usr/local/bin/multica daemon start --foreground
