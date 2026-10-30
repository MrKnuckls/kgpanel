#!/usr/bin/env bash
# =============================================================================
# KGPanel Cloudflare setup — one-shot: domain + tunnel + SSL + DNS for a panel
#
# Turns "I have a fresh box and a domain" into "https://your.domain works,
# SSL included" with a single command. Uses Cloudflare's API so there is
# nothing to install on the router or the DNS host.
#
#   USAGE
#     ./deploy/kgpanel-cloudflare.sh --domain panel.example.com \
#         --cf-token "$CLOUDFLARE_API_TOKEN" --cf-account "$ACCOUNT_ID"
#
#   FLAGS
#     --domain      the public hostname for your panel (required)
#     --cf-token    Cloudflare API token (required, write perms on the zone)
#     --cf-account  Cloudflare account ID (required)
#     --tunnel      tunnel name to create/use (default: kgpanel)
#     --backend     internal address the panel is on (default: http://localhost:8080)
#     --access      email(s) to require before the panel loads — repeat for
#                   several: --access a@x.com --access b@x.com
#     --dry-run     validate everything but change nothing
#     --uninstall   remove the tunnel + DNS record (keeps cloudflared installed)
#
#   WHAT IT DOES
#     1.  validates the token + zone
#     2.  finds your tunnel by name, or creates one
#     3.  points the domain's DNS (CNAME, proxied) at the tunnel
#     4.  sets the tunnel ingress: your.domain -> backend (404 catch-all last)
#     5.  (--access) adds a Cloudflare Access app so only your email can log in
#     6.  installs cloudflared (Docker or bare metal) and runs the tunnel
#     7.  SSL is served automatically by Cloudflare — no certs to buy or renew
#
#   EXIT CODES
#     0  done (or --dry-run validated clean)
#     1  usage/config error
#     2  API error (see last message)
# =============================================================================
set -euo pipefail

# ---- args -------------------------------------------------------------------
DOMAIN=""; CF_TOKEN=""; CF_ACCOUNT=""; TUNNEL_NAME="kgpanel"
BACKEND="http://localhost:8080"; ACCESS_EMAILS=(); DRY_RUN=0; UNINSTALL=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)     DOMAIN="${2?--domain needs a value}"; shift 2;;
    --cf-token)   CF_TOKEN="${2?--cf-token needs a value}"; shift 2;;
    --cf-account) CF_ACCOUNT="${2?--cf-account needs a value}"; shift 2;;
    --tunnel)     TUNNEL_NAME="${2:-kgpanel}"; shift 2;;
    --backend)    BACKEND="${2?--backend needs a value}"; shift 2;;
    --access)     ACCESS_EMAILS+=("${2?--access needs an email}"); shift 2;;
    --dry-run)    DRY_RUN=1; shift;;
    --uninstall)  UNINSTALL=1; shift;;
    -h|--help)    sed -n '2,28p' "$0"; exit 0;;
    *) echo "unknown flag: $1" >&2; exit 1;;
  esac
done

need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1 (install it first)" >&2; exit 1; }; }
need_cmd curl; need_cmd grep

say()  { printf '\033[32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[warn]\033[0m %s\n' "$*" >&2; }

[[ -n "$DOMAIN" && -n "$CF_TOKEN" && -n "$CF_ACCOUNT" ]] || { echo "usage: $0 --domain DNS_NAME --cf-token TOKEN --cf-account ACCT_ID [--tunnel NAME] [--backend URL] [--access EMAIL] [--dry-run]" >&2; exit 1; }
[[ "$DOMAIN" == *"."* ]] || { echo "--domain must be a hostname, e.g. panel.example.com" >&2; exit 1; }

API="https://api.cloudflare.com/client/v4"
AUTH=(-H "Authorization: Bearer $CF_TOKEN" -H "Content-Type: application/json")

cf() { # cf METHOD PATH [JSON] -> prints RESPONSE; exits 2 on API error
  local method="$1" path="$2" body="${3:-}"
  local out code
  if [[ -n "$body" ]]; then
    out=$(curl -sS --max-time 30 -X "$method" "${AUTH[@]}" "$API$path" -d "$body") || { echo "curl failed: $path" >&2; exit 2; }
  else
    out=$(curl -sS --max-time 30 -X "$method" "${AUTH[@]}" "$API$path") || { echo "curl failed: $path" >&2; exit 2; }
  fi
  code=$(printf '%s' "$out" | sed -n 's/.*"success":\(true\|false\).*/\1/p')
  case "$code" in
    true)  printf '%s\n' "$out";;
    false) echo "CF API error on $method $path:" >&2
           printf '%s' "$out" | grep -o '"message":"[^"]*"' | head -3 | sed 's/^/  /' >&2
           exit 2;;
    *)     echo "unexpected CF response on $method $path:" >&2; printf '%s' "$out" | head -c 300 >&2; echo >&2; exit 2;;
  esac
}

# ---- 0. uninstall -----------------------------------------------------------
if [[ "$UNINSTALL" == "1" ]]; then
  say "uninstall mode — removing tunnel + DNS for $DOMAIN"
  ZONE_ID=$(cf GET "/zones?name=${DOMAIN#*.}" | grep -o '"id":"[a-f0-9]\{32\}"' | head -1 | cut -d'"' -f4 || printf "")
  [[ -n "$ZONE_ID" ]] || { echo "zone for $DOMAIN not found — nothing to remove" >&2; exit 2; }
  TUNNEL_ID=$(cf GET "/accounts/$CF_ACCOUNT/cfd_tunnel?name=$TUNNEL_NAME" | grep -o '"id":"[a-f0-9-]\{36\}"' | head -1 | cut -d'"' -f4 || printf "")
  if [[ -n "$TUNNEL_ID" ]]; then
    cf DELETE "/accounts/$CF_ACCOUNT/cfd_tunnel/$TUNNEL_ID" >/dev/null && say "tunnel '$TUNNEL_NAME' deleted"
  else
    say "no tunnel named '$TUNNEL_NAME' — skipping"
  fi
  REC_ID=$(cf GET "/zones/$ZONE_ID/dns_records?type=CNAME&name=$DOMAIN" | grep -o '"id":"[a-f0-9]\{32\}"' | head -1 | cut -d'"' -f4 || printf "")
  if [[ -n "$REC_ID" ]]; then
    cf DELETE "/zones/$ZONE_ID/dns_records/$REC_ID" >/dev/null && say "DNS record $DOMAIN deleted"
  else
    say "no CNAME for $DOMAIN — skipping"
  fi
  say "done. cloudflared is still installed; stop it with your service manager if unwanted."
  exit 0
fi

# ---- 1. validate credentials + zone -------------------------------------------
# NEVER use /user/tokens/verify — it reports "Invalid API Token" for the normal
# account-scoped cfat_ tokens even when they work. Prove liveness with a real
# scoped call (zone list / dns_records) instead.
ZONE_NAME="${DOMAIN#*.}"
ZONE_ID=""
for try_zone in "$ZONE_NAME" "$(echo "$DOMAIN" | cut -d. -f2-)"; do
  R=$(cf GET "/zones?name=$try_zone" || true)
  ZONE_ID=$(printf '%s' "$R" | grep -o '"id":"[a-f0-9]\{32\}"' | head -1 | cut -d'"' -f4 || printf "")
  [[ -n "$ZONE_ID" ]] && { ZONE_NAME="$try_zone"; break; }
done
if [[ -z "$ZONE_ID" ]]; then
  say "token or zone check failed — testing DNS read for $DOMAIN"
  # last proof: if we can even list DNS records the token is alive
  ZID=$(cf GET "/zones?per_page=1" | grep -o '"id":"[a-f0-9]\{32\}"' | head -1 | cut -d'"' -f4 || printf "")
  if [[ -n "$ZID" ]]; then
    say "token OK (account-scoped — scoped zone $DOMAIN not granted or zone name check failed)"
  fi
  echo "cannot verify zone covering $DOMAIN — is the domain on Cloudflare, and does the token have zone:read?" >&2
  exit 2
fi
say "token OK"
say "zone: $ZONE_NAME"

# ---- 2. find or create the tunnel -------------------------------------------
# NOTE: every grep here must end with `|| printf ""` — under `set -euo pipefail`
# a no-match grep exits 1 and silently kills the script (it must instead mean
# "tunnel does not exist yet, create it").
TUNNEL_ID=$(cf GET "/accounts/$CF_ACCOUNT/cfd_tunnel?name=$TUNNEL_NAME" | grep -o '"id":"[a-f0-9-]\{36\}"' | head -1 | cut -d'"' -f4 || printf "")
if [[ -n "$TUNNEL_ID" ]]; then
  say "using existing tunnel '$TUNNEL_NAME' ($TUNNEL_ID)"
else
  say "creating tunnel '$TUNNEL_NAME'"
  if [[ "$DRY_RUN" == "1" ]]; then
    say "[dry-run] would create tunnel '$TUNNEL_NAME'"
  else
    TUNNEL_ID=$(cf POST "/accounts/$CF_ACCOUNT/cfd_tunnel" "{\"name\":\"$TUNNEL_NAME\",\"config_src\":\"cloudflare\"}" | grep -o '"id":"[a-f0-9-]\{36\}"' | head -1 | cut -d'"' -f4)
  fi
fi
[[ -n "$TUNNEL_ID" ]] || { echo "could not determine tunnel id" >&2; exit 2; }

# ---- 3. DNS: CNAME -> tunnel (proxied = SSL served by CF) ----------------------
REC_EXISTS=$(cf GET "/zones/$ZONE_ID/dns_records?type=CNAME&name=$DOMAIN" | grep -c '"id":"[a-f0-9]\{32\}"' || true)
if [[ "$REC_EXISTS" == "0" ]]; then
  say "adding DNS record: $DOMAIN -> $TUNNEL_ID.cfargotunnel.com (proxied)"
  if [[ "$DRY_RUN" == "1" ]]; then
    say "[dry-run] would create the CNAME"
  else
    cf POST "/zones/$ZONE_ID/dns_records" "{\"type\":\"CNAME\",\"name\":\"$DOMAIN\",\"content\":\"$TUNNEL_ID.cfargotunnel.com\",\"proxied\":true}" >/dev/null
  fi
else
  say "DNS record for $DOMAIN already exists — leaving it (ensure proxied=orange cloud)"
fi

# ---- 4. tunnel ingress: domain -> backend --------------------------------------
INGRESS="[{\"hostname\":\"$DOMAIN\",\"service\":\"$BACKEND\"},{\"service\":\"http_status:404\"}]"
say "tunnel ingress: $DOMAIN -> $BACKEND (catch-all 404 last)"
if [[ "$DRY_RUN" == "1" ]]; then
  say "[dry-run] would apply ingress: $INGRESS"
else
  cf PUT "/accounts/$CF_ACCOUNT/cfd_tunnel/$TUNNEL_ID/configurations" "{\"config\":{\"ingress\":$INGRESS}}" >/dev/null
fi

# ---- 5. Cloudflare Access (optional): email-only login ---------------------------
if [[ "${#ACCESS_EMAILS[@]}" -gt 0 ]] && [[ "$DRY_RUN" != "1" ]]; then
  say "adding Cloudflare Access app for $DOMAIN (email gate: ${ACCESS_EMAILS[*]})"
  ACC_ID=$(cf POST "/accounts/$CF_ACCOUNT/access/apps" "{\"name\":\"$TUNNEL_NAME\",\"domain\":\"$DOMAIN\",\"session_duration\":\"24h\"}" | grep -o '"id":"[a-f0-9-]\{36\}"' | head -1 | cut -d'"' -f4 || printf "")
  [[ -n "$ACC_ID" ]] || { warn "Access app created but id not parsed — add policies manually in the CF dashboard"; ACC_ID=""; }
  if [[ -n "$ACC_ID" ]]; then
    INCLUDES=""
    for e in "${ACCESS_EMAILS[@]}"; do
      [[ -n "$INCLUDES" ]] && INCLUDES="$INCLUDES,"
      INCLUDES="$INCLUDES{\"email\":{\"email\":\"$e\",\"identity\":\"email\"}}"
    done
    cf POST "/accounts/$CF_ACCOUNT/access/apps/$ACC_ID/policies" \
      "{\"name\":\"email-gate\",\"decision\":\"allow\",\"precedence\":1,\"include\":[$INCLUDES],\"isolation_required\":false}" >/dev/null \
      && say "Access gate on: only ${ACCESS_EMAILS[*]} can reach the panel"
  fi
fi

# ---- 6. run cloudflared ----------------------------------------------------------
if [[ "$DRY_RUN" == "1" ]]; then
  say "[dry-run] everything validated — no changes made. Run without --dry-run to apply."
  exit 0
fi

TUNNEL_TOKEN=$(cf GET "/accounts/$CF_ACCOUNT/cfd_tunnel/$TUNNEL_ID/token" | grep -o '"token":"[^"]*"' | cut -d'"' -f4 || printf "")
[[ -n "$TUNNEL_TOKEN" ]] || { echo "could not fetch tunnel token" >&2; exit 2; }

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  say "running cloudflared in Docker"
  docker run -d --name cloudflared-kgpanel --restart unless-stopped \
    cloudflare/cloudflared:latest tunnel --no-autoupdate run --token "$TUNNEL_TOKEN" >/dev/null
  say "tunnel container: cloudflared-kgpanel"
else
  say "docker not available — installing cloudflared service (needs root)"
  if [[ $EUID -ne 0 ]]; then warn "you should be root for the bare-metal install"; fi
  ARCH=$(uname -m)
  case "$ARCH" in x86_64) CF_ARCH=amd64;; aarch64|arm64) CF_ARCH=arm64;; *) echo "unsupported arch $ARCH" >&2; exit 2;; esac
  CURL_FLAGS=(-fsSL)
  CURL_FLAGS+=("https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}")
  mkdir -p /usr/local/bin /etc/cloudflared
  curl "${CURL_FLAGS[@]}" -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared
  printf '%s' "$TUNNEL_TOKEN" > /etc/cloudflared/kgpanel.token
  chmod 600 /etc/cloudflared/kgpanel.token
  cat > /etc/systemd/system/cloudflared-kgpanel.service <<'EOF'
[Unit]
Description=KGPanel Cloudflare Tunnel
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloudflared tunnel --no-autoupdate run --token-file /etc/cloudflared/kgpanel.token
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now cloudflared-kgpanel
  say "tunnel service: cloudflared-kgpanel"
fi

# ---- 7. verify ----------------------------------------------------------------
say "waiting for the tunnel to connect..."
sleep 8
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$DOMAIN" 2>/dev/null || true)
case "$code" in
  200|302|307|401|403) say "https://$DOMAIN is live (HTTP $code — 401/403 means the Access gate is on, that is expected)";;
  503) warn "https://$DOMAIN answered 503 — tunnel connected but backend not reachable yet. Is KGPanel up on $BACKEND?";;
  *)   warn "could not reach https://$DOMAIN yet (HTTP ${code:-none}). Give it ~30s and re-run: curl -sI https://$DOMAIN";;
esac
say "done. Your panel's public URL: https://$DOMAIN  (SSL automatic via Cloudflare)"
