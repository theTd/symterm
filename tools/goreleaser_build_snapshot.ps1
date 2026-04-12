<#
.SYNOPSIS
Build Go binary snapshots using GoReleaser with optional custom toolchain.

.DESCRIPTION
Runs 'goreleaser build --snapshot --clean' from the repo root.
Optionally sets GOTOOLCHAIN environment variable for Go version override.
Supports toolchain via parameter or GOTOOLCHAIN_VERSION environment variable.

.PARAMETER Toolchain
Go toolchain version (e.g., 'go1.24', 'go1.23.4').
If not provided, falls back to $env:GOTOOLCHAIN_VERSION, then uses default.

.EXAMPLE
# Use default Go toolchain
.\goreleaser_build_snapshot.ps1

# Specify toolchain via parameter
.\goreleaser_build_snapshot.ps1 -Toolchain go1.24

# Specify via environment variable
$env:GOTOOLCHAIN_VERSION='go1.24'; .\goreleaser_build_snapshot.ps1

.NOTES
Requires goreleaser to be installed and available in PATH.
The GOTOOLCHAIN environment variable is temporarily set during execution and restored.
#>

param(
    [string]$Toolchain = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Resolve toolchain from parameter first, then environment variable, then default
if ([string]::IsNullOrWhiteSpace($Toolchain)) {
    $Toolchain = $env:GOTOOLCHAIN_VERSION
}

# Resolve script and repo paths
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent $scriptDir

# Verify goreleaser is available
$goreleaser = Get-Command goreleaser -ErrorAction SilentlyContinue
if ($null -eq $goreleaser) {
    [Console]::Error.WriteLine("ERROR: goreleaser not found in PATH")
    exit 1
}

# Preserve original GOTOOLCHAIN value for restoration
$previousToolchain = $env:GOTOOLCHAIN

try {
    Push-Location $repoRoot
    
    if ([string]::IsNullOrWhiteSpace($Toolchain)) {
        Write-Host "Using default Go toolchain" -ForegroundColor Cyan
        & $goreleaser.Source build --snapshot --clean
    } else {
        Write-Host "Using GOTOOLCHAIN=$Toolchain" -ForegroundColor Cyan
        $env:GOTOOLCHAIN = $Toolchain
        & $goreleaser.Source build --snapshot --clean
    }
} finally {
    Pop-Location
    
    # Correctly restore or remove the GOTOOLCHAIN environment variable
    if ($null -eq $previousToolchain) {
        Remove-Item env:GOTOOLCHAIN -ErrorAction SilentlyContinue
    } else {
        $env:GOTOOLCHAIN = $previousToolchain
    }
}
