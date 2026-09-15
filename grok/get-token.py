#!/usr/bin/env python3
"""Mint a passwordless M2M access token from Keycloak via private_key_jwt.

Used as the grok CLI `auth_provider` command: it prints a short-lived OAuth
access token to stdout, which the CLI sends as the Bearer token to the LiteLLM
ingress (llm.ai.tellihealth.com), which fronts Bedrock.

The client authenticates by signing a short-lived JWT assertion with its RSA
private key. No client secret is involved. Keycloak verifies the signature
against the public certificate registered on the client. Identity (and thus
per-user Bedrock attribution) follows the client's service-account email.

Environment:
    M2M_CLIENT_ID   Keycloak client id of the service identity
                    (default: cli-kelos-grok)
    M2M_KEY         path to the RSA private key .key file
                    (default: <dir>/<M2M_CLIENT_ID>.key)

Usage:
    python3 get-token.py            # prints an access_token to stdout
"""
import jwt, time, uuid, json, urllib.request, urllib.parse, urllib.error, os, sys

ISS = "https://auth.ai.tellihealth.com/realms/software-engineering"
TOKEN_EP = f"{ISS}/protocol/openid-connect/token"
CLIENT_ID = os.environ.get("M2M_CLIENT_ID", "cli-kelos-grok")
KEY = os.environ.get("M2M_KEY", os.path.join(os.path.dirname(__file__), f"{CLIENT_ID}.key"))

if not os.path.exists(KEY):
    sys.stderr.write(
        f"M2M private key not found at {KEY!r}. "
        "Set M2M_KEY to the mounted key path and M2M_CLIENT_ID to the client id.\n"
    )
    sys.exit(1)

now = int(time.time())
with open(KEY, encoding="utf-8") as f:
    private_key = f.read()

assertion = jwt.encode(
    {"iss": CLIENT_ID, "sub": CLIENT_ID, "aud": ISS,
     "jti": str(uuid.uuid4()), "iat": now, "exp": now + 300},
    private_key, algorithm="RS256")

data = urllib.parse.urlencode({
    "grant_type": "client_credentials",
    "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
    "client_assertion": assertion,
}).encode()

try:
    with urllib.request.urlopen(urllib.request.Request(TOKEN_EP, data=data), timeout=30) as resp:
        tok = json.load(resp)
except urllib.error.HTTPError as e:
    sys.stderr.write(f"HTTP {e.code}: {e.read().decode()}\n"); sys.exit(1)

print(tok["access_token"])
