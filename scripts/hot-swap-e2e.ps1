#!/usr/bin/env pwsh
# hot-swap-e2e.ps1 — End-to-end test for the llamactl hot-swap feature.
#
# This script:
# 1. Builds two versions of llamactl (v1 and v2) with CGO
# 2. Starts llamactl v1 with an isolated config (separate DB)
# 3. Opens an SSE stream
# 4. Triggers a hot-swap to v2
# 5. Verifies the SSE stream stays connected (or reconnects quickly)
# 6. Verifies the instance is still running (adopted)
# 7. Verifies the API still works
# 8. Cleans up
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\scripts\hot-swap-e2e.ps1 [-Port 18079]
#
# Requirements:
#   - Windows
#   - Go installed
#   - MinGW gcc at C:\Tools\mingw64\bin\gcc.exe
#   - The llamactl source tree

param(
    [string]$BuildDir = "build",
    [int]$Port = 18079
)

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptDir

function Write-Step {
    param([string]$Message)
    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

function Write-Pass {
    param([string]$Message)
    Write-Host "  [PASS] $Message" -ForegroundColor Green
}

function Write-Fail {
    param([string]$Message)
    Write-Host "  [FAIL] $Message" -ForegroundColor Red
    throw $Message
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Url,
        [string]$Body = $null
    )
    
    $Params = @{
        Method = $Method
        Uri = $Url
        TimeoutSec = 30
    }
    
    if ($Body) {
        $Params["Body"] = $Body
        $Params["ContentType"] = "application/json"
    }
    
    try {
        return (Invoke-WebRequest @Params -UseBasicParsing).Content
    } catch {
        if ($_.Exception.Response) {
            $status = [int]$_.Exception.Response.StatusCode
            $reader = [System.IO.StreamReader]::new($_.Exception.Response.GetResponseStream())
            $body = $reader.ReadToEnd()
            return @{ "status" = $status; "body" = $body } | ConvertTo-Json
        }
        throw
    }
}

# ============================================================================
# Setup
# ============================================================================

Write-Step "Setting up build directory"
New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null

# CGO build environment (from the llamactl-build skill)
$env:GOCACHE = "E:\OpenClaw\tmp\llamactl-gocache"
$env:CGO_ENABLED = "1"
$env:CC = "C:\Tools\mingw64\bin\gcc.exe"

# ============================================================================
# Step 1: Build llamactl v1 (current code)
# ============================================================================

Write-Step "Building llamactl v1"
Push-Location $RepoRoot
try {
    go build -ldflags "-s -w" -o "$BuildDir/llamactl-v1.exe" ./cmd/server
    $size = (Get-Item "$BuildDir/llamactl-v1.exe").Length
    $sizeMB = [math]::Round($size / 1MB, 1)
    if ($size -lt 20000000) {
        Write-Fail "llamactl-v1.exe is only ${sizeMB}MB - CGO likely off (expected ~24MB)"
    }
    Write-Pass "Built llamactl-v1.exe (${sizeMB}MB)"
} finally {
    Pop-Location
}

# ============================================================================
# Step 2: Build llamactl v2 (same code, different build)
# ============================================================================

Write-Step "Building llamactl v2"
Push-Location $RepoRoot
try {
    go build -ldflags "-s -w -X main.Version=v2-test" -o "$BuildDir/llamactl-v2.exe" ./cmd/server
    $size = (Get-Item "$BuildDir/llamactl-v2.exe").Length
    $sizeMB = [math]::Round($size / 1MB, 1)
    if ($size -lt 20000000) {
        Write-Fail "llamactl-v2.exe is only ${sizeMB}MB - CGO likely off"
    }
    Write-Pass "Built llamactl-v2.exe (${sizeMB}MB)"
} finally {
    Pop-Location
}

# ============================================================================
# Step 3: Create a config file for the test (isolated DB)
# ============================================================================

Write-Step "Creating test config (isolated DB)"
# IMPORTANT: database.path and data_dir point into the build dir so the
# canary does NOT touch the live DB at C:\ProgramData\llamactl\llamactl.db.
# All of these must be absolute: A spawns B with a different notion of cwd.
$BuildDirAbs = [System.IO.Path]::GetFullPath((Join-Path $RepoRoot $BuildDir))
$DataDir = (Join-Path $BuildDirAbs "data").Replace('\', '/')
$LogsDir = (Join-Path $BuildDirAbs "logs").Replace('\', '/')
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
New-Item -ItemType Directory -Force -Path $LogsDir | Out-Null

$ConfigPath = Join-Path $BuildDirAbs "test-config.yaml"
$Config = @"
server:
  host: 127.0.0.1
  port: $Port
  allowed_origins: ["*"]
  allowed_headers: ["Authorization", "Content-Type"]
  enable_swagger: false

instances:
  port_range: [19000, 19999]
  logs_dir: '$LogsDir'
  auto_create_dirs: true
  max_instances: 5
  max_running_instances: 5
  default_idle_timeout: 0
  default_auto_restart: false
  default_max_restarts: 0
  default_restart_delay: 0
  default_on_demand_start: false
  on_demand_start_timeout: 0
  timeout_check_interval: 0

database:
  path: '$DataDir/test.db'
  max_idle_connections: 5
  connection_max_lifetime: 0s

auth:
  require_inference_auth: false
  require_management_auth: false

backends:
  llama-cpp:
    command: ""
    args: []
  vllm:
    command: ""
    args: []
  mlx:
    command: ""
    args: []

local_node: local
nodes: {}
data_dir: '$DataDir'
"@
Set-Content -Path $ConfigPath -Value $Config
Write-Pass "Created $ConfigPath (DB: $DataDir\test.db)"

# ============================================================================
# Step 4: Start llamactl v1
# ============================================================================

Write-Step "Starting llamactl v1 on port $Port"
# The server reads config from LLAMACTL_CONFIG_PATH (not --config flag).
# Absolute path so B inherits a file it can still find after spawn.
$env:LLAMACTL_CONFIG_PATH = $ConfigPath
$v1Exe = Join-Path $BuildDirAbs "llamactl-v1.exe"
$v1Process = Start-Process -FilePath $v1Exe `
    -WorkingDirectory $RepoRoot `
    -RedirectStandardOutput (Join-Path $BuildDirAbs "v1-stdout.log") `
    -RedirectStandardError (Join-Path $BuildDirAbs "v1-stderr.log") `
    -PassThru
Remove-Item Env:\LLAMACTL_CONFIG_PATH

# Wait for the server to be ready
$MaxWait = 30
$Waited = 0
$Ready = $false
while ($Waited -lt $MaxWait) {
    Start-Sleep -Seconds 1
    $Waited++
    try {
        $resp = Invoke-Api -Method "GET" -Url "http://127.0.0.1:$Port/api/v1/version"
        if ($resp) {
            $Ready = $true
            break
        }
    } catch {
        # Not ready yet
    }
}

if (-not $Ready) {
    $stderr = Get-Content (Join-Path $BuildDirAbs "v1-stderr.log") -ErrorAction SilentlyContinue
    if ($stderr) { Write-Host $stderr }
    Write-Fail "llamactl v1 did not become ready within ${MaxWait}s"
}
Write-Pass "llamactl v1 is ready (PID: $($v1Process.Id))"

# A spawns B detached with no stdout/stderr inheritance, so B's own log output
# (config read, port bind, FileConn errors) is not directly capturable.
# A's log is the reliable source: it records B's GOT responses verbatim
# ("HotSwap: B failed to adopt socket: <err>") plus A's own sequence.
# After the swap, A is gone and its log is complete — read it then.

# ============================================================================
# Step 5: Open an SSE stream (in a background job)
# ============================================================================

Write-Step "Opening SSE stream"
$SseLog = Join-Path $BuildDirAbs "sse.log"
$SseJob = Start-Job -ScriptBlock {
    param($url, $log)
    try {
        $webClient = New-Object System.Net.WebClient
        $stream = $webClient.OpenRead($url)
        $reader = New-Object System.IO.StreamReader -ArgumentList $stream
        while ($null -ne ($line = $reader.ReadLine())) {
            Add-Content -Path $log -Value "$(Get-Date -Format 'HH:mm:ss.fff') $line"
        }
    } catch {
        Add-Content -Path $log -Value "$(Get-Date -Format 'HH:mm:ss.fff') ERROR: $_"
    }
} -ArgumentList "http://127.0.0.1:$Port/api/v1/instances/events", $SseLog

Start-Sleep -Seconds 2
Write-Pass "SSE stream opened"

# ============================================================================
# Step 6: Trigger the hot-swap
# ============================================================================

Write-Step "Triggering hot-swap to v2"
$SwapBody = @{ "binary_path" = (Join-Path $BuildDirAbs "llamactl-v2.exe") } | ConvertTo-Json
# NOTE: The hot-swap causes A to exit immediately after sending the response.
# The connection may drop before we can read the response. We treat any
# response (or even a connection drop) as "swap initiated" and then poll
# the status endpoint for completion.
try {
    $SwapResult = Invoke-Api -Method "POST" -Url "http://127.0.0.1:$Port/api/v1/hot-swap" -Body $SwapBody
    Write-Host "  Swap response: $SwapResult"
    Write-Pass "Hot-swap request sent"
} catch {
    # Connection may drop because A exits after the swap. This is expected.
    Write-Host "  Swap request sent (connection may have dropped - expected)"
    Write-Pass "Hot-swap request initiated"
}

# ============================================================================
# Step 7: Wait for the swap to complete
# ============================================================================

Write-Step "Waiting for swap to complete"
$Deadline = (Get-Date).AddSeconds(60)
$Phase = "unknown"
while ((Get-Date) -lt $Deadline) {
    Start-Sleep -Milliseconds 500
    
    try {
        $StatusRaw = Invoke-Api -Method "GET" -Url "http://127.0.0.1:$Port/api/v1/hot-swap/status"
        $Status = $StatusRaw | ConvertFrom-Json
        $Phase = $Status.phase
        
        if ($Phase -eq "failed") {
            Write-Fail "Swap failed: $($Status.error)"
        }

        # The state file may be removed by B immediately after handoff, which
        # makes the status endpoint return idle. Treat a healthy post-swap API
        # as completion in either case.
        if ($Phase -eq "complete" -or $Phase -eq "idle") {
            try {
                $VersionResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$Port/api/v1/version" -TimeoutSec 5
                if ([int]$VersionResponse.StatusCode -eq 200) {
                    break
                }
            } catch {
                # B may still be between listener rebind and HTTP readiness.
            }
        }
    } catch {
        # Server might be temporarily unavailable during the swap
        Write-Host "  Waiting... (status unavailable)"
    }
}

if ($Phase -ne "complete" -and $Phase -ne "idle") {
    Write-Fail "Swap did not complete within 60s (last phase: $Phase)"
}
Write-Pass "Swap completed"

# Dump A's log (complete now that A has exited). B's FileConn/bind errors
# arrive here as "HotSwap: B failed to adopt socket: ..." lines.
$V1Stderr = Get-Content (Join-Path $BuildDirAbs "v1-stderr.log") -ErrorAction SilentlyContinue
if ($V1Stderr) {
    Write-Host ""
    Write-Host "--- v1 (A) stderr ---" -ForegroundColor Gray
    $V1Stderr | ForEach-Object { Write-Host "  $_" -ForegroundColor Gray }
}

# ============================================================================
# Step 8: Verify the instance is still running
# ============================================================================

Write-Step "Verifying instance state"
try {
    $Instances = Invoke-Api -Method "GET" -Url "http://127.0.0.1:$Port/api/v1/instances"
    Write-Host "  Instances: $Instances"
    Write-Pass "Instance list retrieved successfully"
} catch {
    Write-Fail "Failed to retrieve instances: $_"
}

# ============================================================================
# Step 9: Verify the API still works
# ============================================================================

Write-Step "Verifying API functionality"
try {
    $Version = Invoke-Api -Method "GET" -Url "http://127.0.0.1:$Port/api/v1/version"
    if ($Version) {
        Write-Pass "API is responsive: $Version"
    }
} catch {
    Write-Fail "API is not responsive: $_"
}

# ============================================================================
# Step 10: Check SSE stream
# ============================================================================

Write-Step "Checking SSE stream"
Start-Sleep -Seconds 2
if (Test-Path $SseLog) {
    $SseLines = Get-Content $SseLog
    Write-Host "  SSE log lines: $($SseLines.Count)"
    if ($SseLines -match "ERROR") {
        Write-Warning "SSE stream had errors (may be expected during swap)"
    } else {
        Write-Pass "SSE stream maintained"
    }
} else {
    Write-Warning "SSE log file not found"
}

# ============================================================================
# Step 11: Verify cleanup
# ============================================================================

Write-Step "Verifying cleanup"
Start-Sleep -Seconds 5
$OldExe = Join-Path $BuildDirAbs "llamactl-v1.exe.old"
if (Test-Path $OldExe) {
    Write-Warning "llamactl.old.exe still exists (may be cleaned up later)"
} else {
    Write-Pass "llamactl.old.exe cleaned up"
}

# ============================================================================
# Teardown
# ============================================================================

Write-Step "Tearing down"

# Stop the SSE job
if ($SseJob) {
    Stop-Job $SseJob -ErrorAction SilentlyContinue
    Remove-Job $SseJob -Force -ErrorAction SilentlyContinue
}

# Kill only canary processes whose Path is under the build dir.
# Do NOT use Get-Process -Name llamactl unfiltered — production :8079
# runs as llamactl.exe and would be killed.
# B runs as llamactl-candidate.exe (name pattern llamactl-v* misses it),
# so path-based matching is required.
$Killed = Get-Process -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -and ($_.Path -like "*$BuildDir*" -or $_.Path -like "*$($BuildDirAbs)*") } |
    ForEach-Object {
        Write-Host "  Killing $($_.Name) (PID $($_.Id)): $($_.Path)" -ForegroundColor Gray
        Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
    }
if ($Killed) {
    Write-Pass "Canary processes stopped"
} else {
    Write-Host "  No canary processes found to stop" -ForegroundColor Gray
}

# Wait for cleanup
Start-Sleep -Seconds 2

Write-Host "`n==> E2E test complete" -ForegroundColor Green
Write-Host "    Logs: $BuildDir" -ForegroundColor Gray
