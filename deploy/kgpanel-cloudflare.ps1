<#
.SYNOPSIS
  KGPanel Cloudflare setup (Windows) — one-shot: domain + tunnel + SSL + DNS.
.DESCRIPTION
  Cross-OS twin of deploy/kgpanel-cloudflare.sh (bash). Turns "a fresh box and
  a domain" into "https://your.domain works, SSL included" on native Windows —
  no WSL needed. Runs cloudflared in Docker Desktop (or as a bare process).

.USAGE
  powershell -ExecutionPolicy Bypass -File deploy\kgpanel-cloudflare.ps1 `
      -Domain panel.example.com -CfToken $env:CF_TOKEN -CfAccount ACCOUNT_ID

.PARAMETER Domain      public hostname for your panel (required)
.PARAMETER CfToken     Cloudflare API token (required)
.PARAMETER CfAccount   Cloudflare account ID (required)
.PARAMETER Tunnel      tunnel name to create/use (default kgpanel)
.PARAMETER Backend     internal panel address (default http://localhost:8080)
.PARAMETER Access      email(s) to gate behind Cloudflare Access; repeat allowed
.PARAMETER DryRun      validate everything but change nothing
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$Domain,
  [Parameter(Mandatory=$true)][string]$CfToken,
  [Parameter(Mandatory=$true)][string]$CfAccount,
  [string]$Tunnel = "kgpanel",
  [string]$Backend = "http://localhost:8080",
  [string[]]$Access = @(),
  [switch]$DryRun
)

$ErrorActionPreference = "Stop"
$API = "https://api.cloudflare.com/client/v4"
$Headers = @{ Authorization = "Bearer $CfToken"; "Content-Type" = "application/json" }

function Write-Step([string]$m)  { Write-Host "==> $m" -ForegroundColor Green }
function Write-Warn([string]$m)  { Write-Host "[warn] $m" -ForegroundColor Yellow }

# generic CF call: returns the `result` JSON object, throws on API error
function Invoke-CF {
  param([string]$Method, [string]$Path, [hashtable]$Body = $null)
  $params = @{ Uri = "$API$Path"; Headers = $Headers; Method = $Method }
  if ($null -ne $Body) { $params.Body = ($Body | ConvertTo-Json -Depth 6); $params.ContentType = "application/json" }
  $r = Invoke-RestMethod @params
  if (-not $r.success) {
    $msg = ($r.errors | ForEach-Object { $_.message }) -join "; "
    throw "CF API error on $Method $Path`: $msg"
  }
  return $r.result
}

function Get-TunnelId {
  $tunnels = Invoke-CF GET "/accounts/$CfAccount/cfd_tunnel?is_deleted=false"
  $hit = $tunnels | Where-Object { $_.name -eq $Tunnel } | Select-Object -First 1
  if ($hit) { return $hit.id }
  return $null
}

# ---- 1. validate credentials + zone ---------------------------------------------
Write-Step "checking Cloudflare credentials"
Invoke-CF GET "/user/tokens/verify" | Out-Null
Write-Step "token OK"

$zoneName = $Domain -replace '^[^.]+\.', ''
$zone = Invoke-CF GET "/zones?name=$zoneName" | Select-Object -First 1
if (-not $zone) {
  $zoneName = $Domain.Substring($Domain.IndexOf('.') + 1)  # apex try
  $zone = Invoke-CF GET "/zones?name=$zoneName" | Select-Object -First 1
}
if (-not $zone) { throw "cannot find a Cloudflare zone covering $Domain (do you own $zoneName there?)" }
$zoneId = $zone.id
Write-Step "zone: $zoneName"

# ---- 2. find or create the tunnel -----------------------------------------------
$tunnelId = Get-TunnelId
if ($tunnelId) {
  Write-Step "using existing tunnel '$Tunnel' ($tunnelId)"
} else {
  Write-Step "creating tunnel '$Tunnel'"
  if ($DryRun) { Write-Step "[dry-run] would create tunnel '$Tunnel'"; $tunnelId = "00000000-0000-0000-0000-000000000000" }
  else {
    $t = Invoke-CF POST "/accounts/$CfAccount/cfd_tunnel" @{ name = $Tunnel; config_src = "cloudflare" }
    $tunnelId = $t.id
  }
}
if (-not $tunnelId) { throw "could not determine tunnel id" }

# ---- 3. DNS: CNAME -> tunnel (proxied = SSL served by CF) --------------------------
$recs = Invoke-CF GET "/zones/$zoneId/dns_records?type=CNAME&name=$Domain"
if (-not $recs) {
  Write-Step "adding DNS record: $Domain -> $tunnelId.cfargotunnel.com (proxied)"
  if ($DryRun) { Write-Step "[dry-run] would create the CNAME" }
  else { Invoke-CF POST "/zones/$zoneId/dns_records" @{ type="CNAME"; name=$Domain; content="$tunnelId.cfargotunnel.com"; proxied=$true } | Out-Null }
} else {
  Write-Step "DNS record for $Domain already exists — leaving it (ensure proxied/orange cloud)"
}

# ---- 4. tunnel ingress: domain -> backend -------------------------------------------
$ingress = @(
  @{ hostname = $Domain; service = $Backend },
  @{ service = "http_status:404" }
)
Write-Step "tunnel ingress: $Domain -> $Backend (catch-all 404 last)"
if ($DryRun) { Write-Step "[dry-run] would apply ingress" }
else { Invoke-CF PUT "/accounts/$CfAccount/cfd_tunnel/$tunnelId/configurations" @{ config = @{ ingress = $ingress } } | Out-Null }

# ---- 5. Cloudflare Access (optional) -------------------------------------------------
if ($Access.Count -gt 0 -and -not $DryRun) {
  Write-Step "adding Cloudflare Access app for $Domain (email gate: $($Access -join ', '))"
  $app = Invoke-CF POST "/accounts/$CfAccount/access/apps" @{ name=$Tunnel; domain=$Domain; session_duration="24h" }
  $appId = $app.id
  if ($appId) {
    $includes = @()
    foreach ($e in $Access) { $includes += @{ email = @{ email = $e; identity = "email" } } }
    Invoke-CF POST "/accounts/$CfAccount/access/apps/$appId/policies" @{
      name="email-gate"; decision="allow"; precedence=1
      include=$includes; isolation_required=$false
    } | Out-Null
    Write-Step "Access gate on: only $($Access -join ', ') can reach the panel"
  }
}

if ($DryRun) { Write-Step "[dry-run] everything validated — no changes made. Re-run without -DryRun to apply."; exit 0 }

# ---- 6. run cloudflared ----------------------------------------------------------------
$tunnelTokenResp = Invoke-CF GET "/accounts/$CfAccount/cfd_tunnel/$tunnelId/token"
$tunnelToken = $tunnelTokenResp.token
if (-not $tunnelToken) { throw "could not fetch tunnel token" }

$docker = Get-Command docker -ErrorAction SilentlyContinue
if ($docker -and (docker info *> $null; $LASTEXITCODE -eq 0)) {
  Write-Step "running cloudflared in Docker Desktop"
  docker rm -f cloudflared-kgpanel 2>$null | Out-Null
  docker run -d --name cloudflared-kgpanel --restart unless-stopped `
    cloudflare/cloudflared:latest tunnel --no-autoupdate run --token $tunnelToken
  Write-Step "tunnel container: cloudflared-kgpanel"
} else {
  Write-Step "Docker not available — installing cloudflared as a Windows scheduled service"
  $bits = "$env:ProgramFiles\cloudflared"
  New-Item -ItemType Directory -Force -Path $bits | Out-Null
  $url  = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe"
  Write-Host "downloading cloudflared..."
  Invoke-WebRequest -Uri $url -OutFile "$bits\cloudflared.exe"
  $tokFile = "$bits\kgpanel.token"
  Set-Content -Path $tokFile -Value $tunnelToken -NoNewline
  # register the tunnel run with Task Scheduler (start on boot, run as SYSTEM)
  $action  = New-ScheduledTaskAction -Execute "$bits\cloudflared.exe" `
              -Argument "tunnel --no-autoupdate run --token-file `"$tokFile`""
  $trigger = New-ScheduledTaskTrigger -AtStartup
  $settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable
  Register-ScheduledTask -TaskName "cloudflared-kgpanel" -Action $action -Trigger $trigger -Settings $settings -RunLevel Highest -Force | Out-Null
  Start-ScheduledTask -TaskName "cloudflared-kgpanel"
  Write-Step "scheduled task: cloudflared-kgpanel (starts on boot)"
}

# ---- 7. verify ------------------------------------------------------------------------
Write-Step "waiting for the tunnel to connect..."
Start-Sleep -Seconds 8
try {
  $resp = Invoke-WebRequest -Uri "https://$Domain" -MaximumRedirection 0 -TimeoutSec 15 -UseBasicParsing -ErrorAction SilentlyContinue
  $code = [int]$resp.StatusCode
} catch {
  $code = if ($_.Exception.Response) { [int]$_.Exception.Response.StatusCode } else { 0 }
}
switch ($code) {
  200 { Write-Step "https://$Domain is live (200)" }
  302 { Write-Step "https://$Domain is live (302 redirect)" }
  307 { Write-Step "https://$Domain is live (307 redirect)" }
  401 { Write-Step "https://$Domain is live (401 — Access gate on, expected)" }
  403 { Write-Step "https://$Domain is live (403 — Access gate on, expected)" }
  503 { Write-Warn "https://$Domain answered 503 — tunnel connected but backend not reachable. Is KGPanel up on $Backend?" }
  default { Write-Warn "could not reach https://$Domain yet (HTTP $code). Give it ~30s and re-test." }
}
Write-Step "done. Your panel's public URL: https://$Domain  (SSL automatic via Cloudflare)"
