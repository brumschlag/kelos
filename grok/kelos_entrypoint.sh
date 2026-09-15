#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for the
# xAI Grok Build CLI (`grok`).
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - M2M_CLIENT_ID env var: Keycloak service-account client id used to mint the
#     LiteLLM/Bedrock access token (required; set by the Task template)
#   - M2M_KEY env var: filesystem path to the mounted M2M private key .key file
#     (required; the grok-m2m-key secret is mounted here)
#   - KELOS_MODEL env var: model name (optional; defaults to grok-4.6)
#   - KELOS_AGENTS_MD env var: user-level instructions (optional)
#   - KELOS_EFFORT env var: reasoning effort hint (optional)
#   - KELOS_SETUP_COMMAND env var: JSON-encoded exec-form setup command (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured
#
# Headless invocation (verified against grok 1.0.30 bundled docs,
# ~/.grok/docs/user-guide/14-headless-mode.md):
#
#   grok -p "$PROMPT" --no-alt-screen --always-approve --output-format json -m "$MODEL"
#
#   --always-approve : auto-approve all tool executions (no confirmation prompts)
#   --no-alt-screen  : run inline, not in the TUI alternate screen (no TTY)
#   --output-format json : emit a single JSON object with response text + usage
#                          (parsed by /kelos/kelos-capture for token accounting)
#   -m               : model id
#
# NOTE: the CLI has no `--no-auto-update` flag in this version; the image
# instead sets `[cli] auto_update = false` in ~/.grok/config.toml, which is the
# documented equivalent.
#
# AUTHENTICATION: this image does NOT use XAI_API_KEY (direct xAI). Instead the
# config.toml below defines a `grok-4.6` model override that points at the
# LiteLLM ingress (https://llm.ai.tellihealth.com/v1, fronting Bedrock) and an
# `auth_provider` command (get-token.py) that mints a short-lived Keycloak
# private_key_jwt access token from the mounted M2M private key. The token
# carries the llm-proxy audience and the service-account email (per-user
# attribution). Auth therefore requires M2M_CLIENT_ID + M2M_KEY, not XAI_API_KEY.

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

MODEL="${KELOS_MODEL:-grok-4.6}"

# --- LiteLLM/Bedrock auth wiring -------------------------------------------
# The M2M identity is required: without it the auth_provider cannot mint a
# token and grok cannot reach the LiteLLM ingress. Fail early with a clear
# message rather than letting grok surface an opaque auth error.
: "${M2M_CLIENT_ID:?M2M_CLIENT_ID is required (Keycloak service-account client id)}"
: "${M2M_KEY:?M2M_KEY is required (path to the mounted M2M private key)}"
if [ ! -r "$M2M_KEY" ]; then
  echo "M2M_KEY=$M2M_KEY is not readable; is the grok-m2m-key secret mounted?" >&2
  exit 1
fi

# Write the grok CLI config that routes the `grok-4.6` catalog entry through
# the LiteLLM/Bedrock ingress via the get-token.py auth_provider. Written at
# runtime (not baked into the image) so the routing/auth wiring lives with the
# entrypoint. HOME is /home/agent in this image; the path is a literal inside
# the container. A quoted heredoc keeps the TOML verbatim (no shell expansion).
GROK_CONFIG_DIR="${HOME:-/home/agent}/.grok"
mkdir -p "$GROK_CONFIG_DIR"
cat >"$GROK_CONFIG_DIR/config.toml" <<'TOML'
[cli]
auto_update = false

[models]
default = "grok-4.6"

[auth_provider.litellm]
command = "python3 /kelos/get-token.py"
token_ttl_secs = 3300

[model."grok-4.6"]
model = "grok-4-6"
base_url = "https://llm.ai.tellihealth.com/v1"
name = "Grok 4.6 (LiteLLM / Bedrock)"
description = "xAI Grok 4.6 via LiteLLM (us.xai.grok-4.6)"
api_backend = "chat_completions"
auth_provider = "litellm"
context_window = 500000
reasoning_summary = "none"
TOML

# Kelos delivers user-level instructions (KELOS_AGENTS_MD) and an optional
# reasoning-effort hint (KELOS_EFFORT). grok's native user-instruction /
# memory file mechanism is not confirmed for headless runs, so — to guarantee
# the instructions reach the model — they are prepended to the prompt itself.
# This is agent-agnostic and does not depend on any grok-specific file layout.
PREAMBLE=""
if [ -n "${KELOS_AGENTS_MD:-}" ]; then
  PREAMBLE="${KELOS_AGENTS_MD}"$'\n\n'
fi
if [ -n "${KELOS_EFFORT:-}" ]; then
  PREAMBLE="${PREAMBLE}# Kelos Effort"$'\n'"Use ${KELOS_EFFORT} reasoning effort for this task."$'\n\n'
fi
if [ -n "$PREAMBLE" ]; then
  PROMPT="${PREAMBLE}${PROMPT}"
fi

ARGS=(
  "-p" "$PROMPT"
  "--no-alt-screen"
  "--always-approve"
  "--output-format" "json"
  "-m" "$MODEL"
)

# Run pre-agent setup command if configured. KELOS_SETUP_COMMAND is the
# JSON-encoded exec-form array from Workspace.spec.setupCommand. A non-zero
# exit aborts the task before the agent starts.
if [ -n "${KELOS_SETUP_COMMAND:-}" ]; then
  printf '\n---KELOS_SETUP_COMMAND_START---\n' >&2
  node -e '
const { spawnSync } = require("child_process");
const cmd = JSON.parse(process.env.KELOS_SETUP_COMMAND);
if (!Array.isArray(cmd) || cmd.length === 0 || cmd.some(a => typeof a !== "string")) {
  console.error("KELOS_SETUP_COMMAND must be a non-empty JSON array of strings");
  process.exit(2);
}
const r = spawnSync(cmd[0], cmd.slice(1), { stdio: "inherit" });
if (r.error) { console.error(r.error.message); process.exit(127); }
process.exit(r.status ?? 1);
'
  SETUP_EXIT_CODE=$?
  if [ "$SETUP_EXIT_CODE" -ne 0 ]; then
    printf '\n---KELOS_SETUP_COMMAND_FAILED--- exit=%s\n' "$SETUP_EXIT_CODE" >&2
    exit "$SETUP_EXIT_CODE"
  fi
  printf '\n---KELOS_SETUP_COMMAND_DONE---\n' >&2
fi

grok "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
