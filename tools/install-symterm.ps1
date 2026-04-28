param(
    [switch]$SkipSetupWizard,
    [ValidateSet('auto', 'yes', 'no')]
    [string]$InstallDaemon = $(if ($env:SYMTERM_INSTALL_DAEMON) { $env:SYMTERM_INSTALL_DAEMON } else { 'auto' })
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$RepoInput = if ($env:SYMTERM_REPO) {
    $env:SYMTERM_REPO
} elseif ($env:SYMTERM_GITHUB_REPO_URL) {
    $env:SYMTERM_GITHUB_REPO_URL
} elseif ($env:SYMTERM_GITHUB_REPO) {
    $env:SYMTERM_GITHUB_REPO
} else {
    'https://github.com/theTd/symterm/'
}

$SymtermVersion = if ($env:SYMTERM_VERSION) { $env:SYMTERM_VERSION } elseif ($env:SYMTERMD_VERSION) { $env:SYMTERMD_VERSION } else { '' }
$SymtermDownloadUrl = $env:SYMTERM_DOWNLOAD_URL
$InstallRoot = if ($env:INSTALL_ROOT) { $env:INSTALL_ROOT } else { Join-Path $env:LOCALAPPDATA 'symterm' }
$BinDir = Join-Path $InstallRoot 'bin'
$ClientBinPath = Join-Path $BinDir 'symterm.exe'

function Resolve-Repo {
    param([string]$InputValue)

    $value = $InputValue.Trim().TrimEnd('/')
    if ($value.EndsWith('.git')) {
        $value = $value.Substring(0, $value.Length - 4)
    }

    if ($value -match '^(https?://github\.com/)(?<rest>.+)$') {
        $slug = $Matches.rest
    } elseif ($value -match '^git@github\.com:(?<rest>.+)$') {
        $slug = $Matches.rest
    } elseif ($value -match '^[^/]+/[^/]+') {
        $slug = $value
    } else {
        throw "Unsupported GitHub repository value: $InputValue"
    }

    $parts = $slug.Split('/', [System.StringSplitOptions]::RemoveEmptyEntries)
    if ($parts.Length -lt 2) {
        throw "Failed to parse GitHub repository from: $InputValue"
    }

    $repoSlug = '{0}/{1}' -f $parts[0], $parts[1]
    [pscustomobject]@{
        Slug = $repoSlug
        Url  = "https://github.com/$repoSlug/"
    }
}

function Resolve-Arch {
    $arch = $env:PROCESSOR_ARCHITECTURE.ToLowerInvariant()
    switch ($arch) {
        'amd64' { 'amd64' }
        'arm64' { 'arm64' }
        default { throw "Unsupported CPU architecture: $arch" }
    }
}

function Get-GitHubHeaders {
    $headers = @{
        'Accept' = 'application/vnd.github+json'
        'User-Agent' = 'symterm-install-script'
    }
    if ($env:GITHUB_TOKEN) {
        $headers['Authorization'] = "Bearer $($env:GITHUB_TOKEN)"
    }
    $headers
}

function Resolve-Version {
    param(
        [string]$RepoSlug,
        [string]$RequestedVersion,
        [string]$RepoUrl
    )

    if ($RequestedVersion) {
        return $RequestedVersion
    }

    $release = Invoke-RestMethod -Headers (Get-GitHubHeaders) -Uri "https://api.github.com/repos/$RepoSlug/releases/latest"
    if (-not $release.tag_name) {
        throw "Failed to resolve the latest GitHub release tag for $RepoUrl"
    }
    [string]$release.tag_name
}

function Get-AssetName {
    param(
        [string]$Component,
        [string]$Version,
        [string]$Arch
    )

    $assetVersion = $Version.TrimStart('v')
    "symterm_{0}_{1}_windows_{2}.zip" -f $Component, $assetVersion, $Arch
}

function Get-AssetUrl {
    param(
        [string]$RepoSlug,
        [string]$Version,
        [string]$Component,
        [string]$Arch
    )

    $assetName = Get-AssetName -Component $Component -Version $Version -Arch $Arch
    "https://github.com/$RepoSlug/releases/download/$Version/$assetName"
}

function Get-SourceName {
    param([string]$SourceUrl)

    $trimmed = $SourceUrl.Split('?', 2)[0].Split('#', 2)[0]
    Split-Path -Leaf $trimmed
}

function Resolve-LocalSourcePath {
    param([string]$SourceUrl)

    if ($SourceUrl -like 'file://localhost/*') {
        return $SourceUrl.Substring('file://localhost'.Length)
    }
    if ($SourceUrl -like 'file:///*') {
        return $SourceUrl.Substring('file://'.Length)
    }
    if ($SourceUrl -like 'file://*') {
        throw "Unsupported file URI host in download source: $SourceUrl"
    }
    $SourceUrl
}

function Download-ToFile {
    param(
        [string]$SourceUrl,
        [string]$DestinationPath
    )

    if ($SourceUrl -match '^https?://') {
        Invoke-WebRequest -Headers (Get-GitHubHeaders) -Uri $SourceUrl -OutFile $DestinationPath | Out-Null
    } else {
        Copy-Item -LiteralPath (Resolve-LocalSourcePath -SourceUrl $SourceUrl) -Destination $DestinationPath -Force
    }
}

function Install-BinaryFromSource {
    param(
        [string]$SourceUrl,
        [string]$ExpectedBinary,
        [string]$DestinationPath
    )

    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    $tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ('symterm-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    try {
        $sourceName = Get-SourceName -SourceUrl $SourceUrl
        $assetPath = Join-Path $tempDir $sourceName
        Download-ToFile -SourceUrl $SourceUrl -DestinationPath $assetPath
        if ($sourceName.EndsWith('.zip')) {
            $extractDir = Join-Path $tempDir 'extracted'
            Expand-Archive -LiteralPath $assetPath -DestinationPath $extractDir -Force
            $binary = Get-ChildItem -LiteralPath $extractDir -Recurse -File | Where-Object { $_.Name -eq $ExpectedBinary } | Select-Object -First 1
            if (-not $binary) {
                throw "Release asset did not contain $ExpectedBinary"
            }
            Copy-Item -LiteralPath $binary.FullName -Destination $DestinationPath -Force
        } else {
            Copy-Item -LiteralPath $assetPath -Destination $DestinationPath -Force
        }
    }
    finally {
        Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Resolve-DaemonChoice {
    param([string]$Mode)

    switch ($Mode.ToLowerInvariant()) {
        'yes' { return $true }
        'no' { return $false }
        'auto' {
            if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) {
                return $false
            }
            $answer = Read-Host 'Install symtermd daemon? [y/N]'
            return $answer -match '^(?i:y|yes)$'
        }
        default { throw "Invalid InstallDaemon value: $Mode" }
    }
}

$repo = Resolve-Repo -InputValue $RepoInput
$arch = Resolve-Arch
$resolvedVersion = Resolve-Version -RepoSlug $repo.Slug -RequestedVersion $SymtermVersion -RepoUrl $repo.Url
$clientSource = if ($SymtermDownloadUrl) {
    $SymtermDownloadUrl
} else {
    Get-AssetUrl -RepoSlug $repo.Slug -Version $resolvedVersion -Component 'symterm' -Arch $arch
}

Install-BinaryFromSource -SourceUrl $clientSource -ExpectedBinary 'symterm.exe' -DestinationPath $ClientBinPath

if (Resolve-DaemonChoice -Mode $InstallDaemon) {
    Write-Warning 'symtermd is not supported on Windows. Skipping daemon install.'
}

Write-Host "symterm $resolvedVersion installed"
Write-Host "repo: $($repo.Url)"
Write-Host "client binary path: $ClientBinPath"
Write-Host 'daemon install skipped: unsupported platform (windows)'

$pathEntries = (($env:PATH -split ';') | Where-Object { $_ })
if (-not ($pathEntries -contains $BinDir)) {
    Write-Warning "$BinDir is not in PATH for this PowerShell session."
}
