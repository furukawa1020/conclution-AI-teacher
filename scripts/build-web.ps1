[CmdletBinding()]
param(
    [string] $CargoPath = "cargo",

    [string] $WasmBindgenPath = "",

    [ValidatePattern("^[0-9a-fA-F]{40}$")]
    [string] $ExpectedGitCommit = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
$targetRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace ".cache\cargo-target"))
$distRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace "dist\web"))
$webSource = [System.IO.Path]::GetFullPath((Join-Path $workspace "apps\client\web"))
$cssSource = [System.IO.Path]::GetFullPath((Join-Path $workspace "apps\client\assets\main.css"))
$wasmInput = Join-Path $targetRoot "wasm32-unknown-unknown\release\kotae-client.wasm"
$releaseManifestName = ".kotae-release-manifest.json"

function Assert-WorkspacePath {
    param(
        [Parameter(Mandatory)]
        [string] $Path,

        [Parameter(Mandatory)]
        [string] $Description
    )

    $workspacePrefix = $workspace.TrimEnd("\", "/") + [System.IO.Path]::DirectorySeparatorChar
    if (
        -not $Path.StartsWith($workspacePrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        [string]::Equals(
            $Path.TrimEnd("\", "/"),
            $workspace.TrimEnd("\", "/"),
            [System.StringComparison]::OrdinalIgnoreCase
        )
    ) {
        throw "$Description must stay inside the workspace."
    }
}

Assert-WorkspacePath -Path $targetRoot -Description "Cargo target directory"
Assert-WorkspacePath -Path $distRoot -Description "Web distribution directory"
Assert-WorkspacePath -Path $webSource -Description "Web source directory"

function Invoke-ReleaseGitText {
    param(
        [Parameter(Mandatory)]
        [System.Management.Automation.ApplicationInfo] $GitCommand,

        [Parameter(Mandatory)]
        [string[]] $CommandArguments,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    $lines = @(& $GitCommand.Source -C $workspace @CommandArguments 2>$null)
    if ($LASTEXITCODE -ne 0) {
        throw "Git failed while $Operation."
    }
    return ($lines -join [System.Environment]::NewLine).Trim()
}

function Assert-ReleaseSourceState {
    param(
        [Parameter(Mandatory)]
        [System.Management.Automation.ApplicationInfo] $GitCommand,

        [Parameter(Mandatory)]
        [string] $ExpectedCommit,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    $head = Invoke-ReleaseGitText `
        -GitCommand $GitCommand `
        -CommandArguments @("rev-parse", "--verify", "HEAD") `
        -Operation $Operation
    if ($head -cne $ExpectedCommit.ToLowerInvariant()) {
        throw "Release source changed while $Operation; expected $ExpectedCommit, got $head."
    }
    $status = Invoke-ReleaseGitText `
        -GitCommand $GitCommand `
        -CommandArguments @("status", "--porcelain=v1", "--untracked-files=all") `
        -Operation $Operation
    if (-not [string]::IsNullOrWhiteSpace($status)) {
        throw "Release builds require a clean tracked and untracked working tree."
    }
}

if (-not (Test-Path -LiteralPath $webSource -PathType Container)) {
    throw "Web source directory does not exist: $webSource"
}
if (-not (Test-Path -LiteralPath $cssSource -PathType Leaf)) {
    throw "CSS source does not exist: $cssSource"
}

$buildLockRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace ".cache"))
Assert-WorkspacePath -Path $buildLockRoot -Description "Web build lock directory"
New-Item -ItemType Directory -Force -Path $buildLockRoot | Out-Null
if (
    ((Get-Item -LiteralPath $buildLockRoot -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Web build lock directory must not be a reparse point."
}
$buildLockPath = Join-Path $buildLockRoot "web-build.lock"
try {
    $buildLock = [System.IO.File]::Open(
        $buildLockPath,
        [System.IO.FileMode]::OpenOrCreate,
        [System.IO.FileAccess]::ReadWrite,
        [System.IO.FileShare]::None
    )
} catch {
    throw "Another web build is already using the shared target or dist directory."
}

try {
$releaseGit = $null
if (-not [string]::IsNullOrWhiteSpace($ExpectedGitCommit)) {
    $ExpectedGitCommit = $ExpectedGitCommit.ToLowerInvariant()
    $releaseGit = Get-Command "git" -CommandType Application -ErrorAction Stop
    $repositoryRoot = Invoke-ReleaseGitText `
        -GitCommand $releaseGit `
        -CommandArguments @("rev-parse", "--show-toplevel") `
        -Operation "checking the release repository root"
    if (-not [string]::Equals(
            [System.IO.Path]::GetFullPath($repositoryRoot).TrimEnd("\", "/"),
            [System.IO.Path]::GetFullPath($workspace).TrimEnd("\", "/"),
            [System.StringComparison]::OrdinalIgnoreCase
        )) {
        throw "Release builds must run from the expected repository root."
    }
    Assert-ReleaseSourceState `
        -GitCommand $releaseGit `
        -ExpectedCommit $ExpectedGitCommit `
        -Operation "starting the web release build"
}

$cargoCommand = Get-Command $CargoPath -CommandType Application -ErrorAction Stop

if ([string]::IsNullOrWhiteSpace($WasmBindgenPath)) {
    $workspaceWasmBindgen = Join-Path $workspace ".tools\wasm-bindgen-0.2.126\wasm-bindgen.exe"
    $profileRoots = @(
        [Environment]::GetFolderPath("UserProfile"),
        $env:USERPROFILE
    ) |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        Select-Object -Unique
    $userWasmBindgen = $profileRoots |
        ForEach-Object {
            Join-Path $_ ".dx\tools\wasm-bindgen-0.2.126\wasm-bindgen.exe"
        } |
        Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
        Select-Object -First 1
    if (Test-Path -LiteralPath $workspaceWasmBindgen -PathType Leaf) {
        $WasmBindgenPath = $workspaceWasmBindgen
    } elseif (-not [string]::IsNullOrWhiteSpace($userWasmBindgen)) {
        $WasmBindgenPath = $userWasmBindgen
    } else {
        $wasmBindgenCommand = Get-Command "wasm-bindgen" -CommandType Application -ErrorAction SilentlyContinue
        if ($null -eq $wasmBindgenCommand) {
            throw "wasm-bindgen 0.2.126 is required. Run the verified Dioxus tool installation first."
        }
        $WasmBindgenPath = $wasmBindgenCommand.Source
    }
}
$wasmBindgen = [System.IO.Path]::GetFullPath($WasmBindgenPath)
if (-not (Test-Path -LiteralPath $wasmBindgen -PathType Leaf)) {
    throw "wasm-bindgen does not exist: $wasmBindgen"
}
$wasmBindgenVersion = ((& $wasmBindgen --version) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $wasmBindgenVersion -notmatch 'wasm-bindgen\s+0\.2\.126(?:\s|$)') {
    throw "Expected wasm-bindgen 0.2.126, got '$wasmBindgenVersion'."
}

New-Item -ItemType Directory -Force -Path $targetRoot | Out-Null
if (
    ((Get-Item -LiteralPath $targetRoot -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Cargo target directory must not be a reparse point."
}
$previousTargetDirectory = $env:CARGO_TARGET_DIR
try {
    $env:CARGO_TARGET_DIR = $targetRoot
    & $cargoCommand.Source build `
        --manifest-path (Join-Path $workspace "Cargo.toml") `
        --package kotae-client `
        --target wasm32-unknown-unknown `
        --release `
        --locked
    if ($LASTEXITCODE -ne 0) {
        throw "Rust/Wasm release build failed."
    }
} finally {
    $env:CARGO_TARGET_DIR = $previousTargetDirectory
}

if (-not (Test-Path -LiteralPath $wasmInput -PathType Leaf)) {
    throw "Rust/Wasm build output is missing: $wasmInput"
}

$stagingParent = Join-Path $workspace ".cache\web-staging"
Assert-WorkspacePath -Path ([System.IO.Path]::GetFullPath($stagingParent)) -Description "Web staging directory"
New-Item -ItemType Directory -Force -Path $stagingParent | Out-Null
if (
    ((Get-Item -LiteralPath $stagingParent -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Web staging directory must not be a reparse point."
}
$stagingRoot = Join-Path $stagingParent ([Guid]::NewGuid().ToString("N"))
$wasmOutput = Join-Path $stagingRoot "wasm"
$assetsOutput = Join-Path $stagingRoot "assets"
New-Item -ItemType Directory -Force -Path $wasmOutput | Out-Null
New-Item -ItemType Directory -Force -Path $assetsOutput | Out-Null

try {
    & $wasmBindgen `
        --target web `
        --out-dir $wasmOutput `
        --out-name kotae_client `
        --no-typescript `
        $wasmInput
    if ($LASTEXITCODE -ne 0) {
        throw "wasm-bindgen failed."
    }

    foreach ($name in @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "passkey-policy.mjs",
        "pcm-capture-worklet.js",
        "voice-session-policy.mjs",
        "voice-stream-policy.mjs"
    )) {
        $source = Join-Path $webSource $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Required web source is missing: $name"
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $stagingRoot $name)
    }
    Copy-Item -LiteralPath $cssSource -Destination (Join-Path $assetsOutput "main.css")

    $requiredFiles = @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "passkey-policy.mjs",
        "pcm-capture-worklet.js",
        "voice-session-policy.mjs",
        "voice-stream-policy.mjs",
        "assets\main.css",
        "wasm\kotae_client.js",
        "wasm\kotae_client_bg.wasm"
    )
    foreach ($relativePath in $requiredFiles) {
        if (-not (Test-Path -LiteralPath (Join-Path $stagingRoot $relativePath) -PathType Leaf)) {
            throw "Generated web artifact is missing: $relativePath"
        }
    }

    $bridge = [System.IO.File]::ReadAllText(
        (Join-Path $stagingRoot "firebase-bridge.js"),
        [System.Text.Encoding]::UTF8
    )
    $siteKeyMatch = [regex]::Match(
        $bridge,
        '(?m)^\s*const\s+RECAPTCHA_SITE_KEY\s*=\s*"(?<key>[^"]+)";\s*$'
    )
    if (
        -not $siteKeyMatch.Success -or
        $siteKeyMatch.Groups["key"].Value -eq "__RECAPTCHA_SITE_KEY__" -or
        $siteKeyMatch.Groups["key"].Value.Length -lt 20
    ) {
        throw "Refusing to build with an unconfigured reCAPTCHA Enterprise site key."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-session-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice session policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-stream-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice stream policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./passkey-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited passkey policy module."
    }

    $bootstrap = [System.IO.File]::ReadAllText(
        (Join-Path $stagingRoot "bootstrap.js"),
        [System.Text.Encoding]::UTF8
    )
    $bridgeImport = $bootstrap.IndexOf('import("/firebase-bridge.js")', [System.StringComparison]::Ordinal)
    $wasmImport = $bootstrap.IndexOf('import("/wasm/kotae_client.js")', [System.StringComparison]::Ordinal)
    if ($bridgeImport -lt 0 -or $wasmImport -lt 0 -or $bridgeImport -gt $wasmImport) {
        throw "bootstrap.js must initialize Firebase before Rust/Wasm."
    }

    $index = [System.IO.File]::ReadAllText(
        (Join-Path $stagingRoot "index.html"),
        [System.Text.Encoding]::UTF8
    )
    if (
        [regex]::Matches($index, '<script\b', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase).Count -ne 1 -or
        $index -notmatch '<script\s+type="module"\s+src="/bootstrap\.js"></script>'
    ) {
        throw "index.html must contain only the external bootstrap.js module script."
    }

    $totalBytes = 0L
    $releaseArtifacts = @()
    foreach ($entry in @(Get-ChildItem -LiteralPath $stagingRoot -Recurse -Force | Sort-Object FullName)) {
        if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Generated web artifacts must not contain reparse points."
        }
        if ($entry.PSIsContainer) {
            continue
        }
        $relativePath = $entry.FullName.
            Substring($stagingRoot.TrimEnd("\", "/").Length).
            TrimStart("\", "/").
            Replace("\", "/")
        $allowedPath = (
            $relativePath -in @(
                "index.html",
                "bootstrap.js",
                "firebase-bridge.js",
                "passkey-policy.mjs",
                "pcm-capture-worklet.js",
                "voice-session-policy.mjs",
                "voice-stream-policy.mjs",
                "assets/main.css",
                "wasm/kotae_client.js",
                "wasm/kotae_client_bg.wasm"
            ) -or
            $relativePath -match '^wasm/snippets/[A-Za-z0-9._/-]+\.js$'
        )
        if (-not $allowedPath) {
            throw "Unexpected generated web artifact: $relativePath"
        }
        if ($entry.Length -gt 15MB) {
            throw "Generated web artifact exceeds the 15 MiB safety limit: $($entry.FullName)"
        }
        if ($entry.Length -gt ([long]::MaxValue - $totalBytes)) {
            throw "Generated web artifact size overflow."
        }
        $totalBytes += $entry.Length
        $releaseArtifacts += [ordered]@{
            path = $relativePath
            sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $entry.FullName).Hash.ToLowerInvariant()
            bytes = [long] $entry.Length
        }
    }
    if ($totalBytes -gt 25MB) {
        throw "Generated web artifacts exceed the 25 MiB aggregate safety limit."
    }

    if ($null -ne $releaseGit) {
        Assert-ReleaseSourceState `
            -GitCommand $releaseGit `
            -ExpectedCommit $ExpectedGitCommit `
            -Operation "finalizing the web release build"
        $manifestPath = Join-Path $stagingRoot $releaseManifestName
        $manifest = [ordered]@{
            schemaVersion = 1
            sourceCommit = $ExpectedGitCommit
            artifacts = @($releaseArtifacts)
        }
        $manifestJson = $manifest | ConvertTo-Json -Depth 6
        [System.IO.File]::WriteAllText(
            $manifestPath,
            $manifestJson + [System.Environment]::NewLine,
            [System.Text.UTF8Encoding]::new($false)
        )
        if ((Get-Item -LiteralPath $manifestPath).Length -gt 64KB) {
            throw "Web release manifest exceeds the 64 KiB safety limit."
        }
    }

    $distParent = Split-Path -Parent $distRoot
    New-Item -ItemType Directory -Force -Path $distParent | Out-Null
    if (
        ((Get-Item -LiteralPath $distParent -Force).Attributes -band
            [System.IO.FileAttributes]::ReparsePoint) -ne 0
    ) {
        throw "Web distribution parent must not be a reparse point."
    }
    if (Test-Path -LiteralPath $distRoot) {
        if (
            ((Get-Item -LiteralPath $distRoot -Force).Attributes -band
                [System.IO.FileAttributes]::ReparsePoint) -ne 0
        ) {
            throw "Refusing to replace a dist/web reparse point."
        }
        Remove-Item -LiteralPath $distRoot -Recurse -Force
    }
    Move-Item -LiteralPath $stagingRoot -Destination $distRoot
    Write-Output "WEB_DIST=$distRoot"
    if ($null -ne $releaseGit) {
        Write-Output "WEB_RELEASE_COMMIT=$ExpectedGitCommit"
    }
} finally {
    if (Test-Path -LiteralPath $stagingRoot) {
        Remove-Item -LiteralPath $stagingRoot -Recurse -Force
    }
}
} finally {
    $buildLock.Dispose()
}
