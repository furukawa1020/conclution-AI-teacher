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
$pcmRingWasmInput = Join-Path $targetRoot "wasm32-unknown-unknown\release\kotae_pcm_ring.wasm"
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

function ConvertTo-Lf {
    param(
        [Parameter(Mandatory)]
        [string] $Text
    )

    return $Text.Replace("`r`n", "`n").Replace("`r", "`n")
}

function Replace-LiteralExactlyOnce {
    param(
        [Parameter(Mandatory)]
        [string] $Text,

        [Parameter(Mandatory)]
        [string] $Needle,

        [Parameter(Mandatory)]
        [AllowEmptyString()]
        [string] $Replacement,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    if ([string]::IsNullOrEmpty($Needle)) {
        throw "The $Boundary bundle pattern must not be empty."
    }
    $firstIndex = $Text.IndexOf($Needle, [System.StringComparison]::Ordinal)
    if ($firstIndex -lt 0) {
        throw "The $Boundary bundle pattern is missing."
    }
    $secondIndex = $Text.IndexOf(
        $Needle,
        $firstIndex + $Needle.Length,
        [System.StringComparison]::Ordinal
    )
    if ($secondIndex -ge 0) {
        throw "The $Boundary bundle pattern is ambiguous."
    }
    return $Text.Substring(0, $firstIndex) +
        $Replacement +
        $Text.Substring($firstIndex + $Needle.Length)
}

function Remove-RegexExactlyOnce {
    param(
        [Parameter(Mandatory)]
        [string] $Text,

        [Parameter(Mandatory)]
        [string] $Pattern,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    $matches = [regex]::Matches(
        $Text,
        $Pattern,
        [System.Text.RegularExpressions.RegexOptions]::CultureInvariant,
        [TimeSpan]::FromSeconds(2)
    )
    if ($matches.Count -ne 1) {
        throw "The $Boundary bundle pattern must occur exactly once."
    }
    $match = $matches[0]
    return $Text.Remove($match.Index, $match.Length)
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
        --package kotae-pcm-ring `
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
if (-not (Test-Path -LiteralPath $pcmRingWasmInput -PathType Leaf)) {
    throw "PCM ring Rust/Wasm build output is missing: $pcmRingWasmInput"
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
$pcmRingBindgenOutput = Join-Path $stagingParent (
    [Guid]::NewGuid().ToString("N") + "-pcm-ring-bindgen"
)
$wasmOutput = Join-Path $stagingRoot "wasm"
$assetsOutput = Join-Path $stagingRoot "assets"
New-Item -ItemType Directory -Force -Path $wasmOutput | Out-Null
New-Item -ItemType Directory -Force -Path $assetsOutput | Out-Null
New-Item -ItemType Directory -Force -Path $pcmRingBindgenOutput | Out-Null
Assert-WorkspacePath `
    -Path ([System.IO.Path]::GetFullPath($pcmRingBindgenOutput)) `
    -Description "PCM ring wasm-bindgen staging directory"
if (
    ((Get-Item -LiteralPath $pcmRingBindgenOutput -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "PCM ring wasm-bindgen staging directory must not be a reparse point."
}

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

    & $wasmBindgen `
        --target web `
        --out-dir $pcmRingBindgenOutput `
        --out-name kotae_pcm_ring `
        --no-typescript `
        $pcmRingWasmInput
    if ($LASTEXITCODE -ne 0) {
        throw "PCM ring wasm-bindgen failed."
    }

    foreach ($name in @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "passkey-policy.mjs",
        "voice-session-policy.mjs",
        "voice-prepare-slo-policy.mjs",
        "voice-start-slo-policy.mjs",
        "voice-stream-policy.mjs"
    )) {
        $source = Join-Path $webSource $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Required web source is missing: $name"
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $stagingRoot $name)
    }

    $ringGeneratedEntries = @(
        Get-ChildItem -LiteralPath $pcmRingBindgenOutput -Recurse -Force
    )
    foreach ($entry in $ringGeneratedEntries) {
        if (
            $entry.PSIsContainer -or
            ($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0
        ) {
            throw "PCM ring wasm-bindgen produced an unexpected directory or reparse point."
        }
    }
    $ringGeneratedPaths = @(
        $ringGeneratedEntries |
            ForEach-Object {
                $_.FullName.
                    Substring($pcmRingBindgenOutput.TrimEnd("\", "/").Length).
                    TrimStart("\", "/").
                    Replace("\", "/")
            } |
            Sort-Object
    )
    if (
        $ringGeneratedPaths.Count -ne 2 -or
        ($ringGeneratedPaths -join ",") -cne
            "kotae_pcm_ring.js,kotae_pcm_ring_bg.wasm"
    ) {
        throw "PCM ring wasm-bindgen output escaped the reviewed two-file boundary."
    }

    $ringGluePath = Join-Path $pcmRingBindgenOutput "kotae_pcm_ring.js"
    $ringWasmPath = Join-Path $pcmRingBindgenOutput "kotae_pcm_ring_bg.wasm"
    $ringRuntimePath = Join-Path $webSource "pcm-ring-worklet-runtime.js"
    $captureSourcePath = Join-Path $webSource "pcm-capture-worklet.js"
    foreach ($inputPath in @(
        $ringGluePath,
        $ringWasmPath,
        $ringRuntimePath,
        $captureSourcePath
    )) {
        if (-not (Test-Path -LiteralPath $inputPath -PathType Leaf)) {
            throw "PCM Worklet bundle input is missing: $inputPath"
        }
        $inputEntry = Get-Item -LiteralPath $inputPath -Force
        if (
            ($inputEntry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
            $inputEntry.Length -le 0 -or
            $inputEntry.Length -gt 1MB
        ) {
            throw "PCM Worklet bundle input is outside the reviewed boundary."
        }
    }

    $strictUtf8 = [System.Text.UTF8Encoding]::new($false, $true)
    try {
        $ringGlue = ConvertTo-Lf -Text (
            [System.IO.File]::ReadAllText($ringGluePath, $strictUtf8)
        )
        $ringRuntime = ConvertTo-Lf -Text (
            [System.IO.File]::ReadAllText($ringRuntimePath, $strictUtf8)
        )
        $captureSource = ConvertTo-Lf -Text (
            [System.IO.File]::ReadAllText($captureSourcePath, $strictUtf8)
        )
    } catch {
        throw "PCM Worklet bundle inputs must be valid UTF-8."
    }

    $ringGlue = Replace-LiteralExactlyOnce `
        -Text $ringGlue `
        -Needle "export class PcmRing" `
        -Replacement "class PcmRing" `
        -Boundary "wasm-bindgen PcmRing export"
    $generatedThrowHandler = @'
        __wbg___wbindgen_throw_344f42d3211c4765: function(arg0, arg1) {
            throw new Error(getStringFromWasm0(arg0, arg1));
        },
'@
    $contentFreeThrowHandler = @'
        __wbg___wbindgen_throw_344f42d3211c4765: function() {
            throw new Error("pcm_ring_wasm_failure");
        },
'@
    $ringGlue = Replace-LiteralExactlyOnce `
        -Text $ringGlue `
        -Needle $generatedThrowHandler `
        -Replacement $contentFreeThrowHandler `
        -Boundary "wasm-bindgen content-free throw import"
    $generatedStringReader = @'
function getStringFromWasm0(ptr, len) {
    return decodeText(ptr >>> 0, len);
}
'@
    $ringGlue = Replace-LiteralExactlyOnce `
        -Text $ringGlue `
        -Needle $generatedStringReader `
        -Replacement "" `
        -Boundary "wasm-bindgen string reader"
    $generatedTextDecoder = @'
let cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
cachedTextDecoder.decode();
const MAX_SAFARI_DECODE_BYTES = 2146435072;
let numBytesDecoded = 0;
function decodeText(ptr, len) {
    numBytesDecoded += len;
    if (numBytesDecoded >= MAX_SAFARI_DECODE_BYTES) {
        cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
        cachedTextDecoder.decode();
        numBytesDecoded = len;
    }
    return cachedTextDecoder.decode(getUint8ArrayMemory0().subarray(ptr, ptr + len));
}
'@
    $ringGlue = Replace-LiteralExactlyOnce `
        -Text $ringGlue `
        -Needle $generatedTextDecoder `
        -Replacement "" `
        -Boundary "wasm-bindgen TextDecoder dependency"
    $ringGlue = Remove-RegexExactlyOnce `
        -Text $ringGlue `
        -Pattern '(?s)\nasync function __wbg_load\(module, imports\) \{.*?\n\}\n\n(?=function initSync\(module\) \{)' `
        -Boundary "wasm-bindgen async loader"
    $ringGlue = Remove-RegexExactlyOnce `
        -Text $ringGlue `
        -Pattern '(?s)\nasync function __wbg_init\(module_or_path\) \{.*?\n\}\n\n(?=export \{ initSync, __wbg_init as default \};)' `
        -Boundary "wasm-bindgen async initializer"
    $ringGlue = Replace-LiteralExactlyOnce `
        -Text $ringGlue `
        -Needle "export { initSync, __wbg_init as default };" `
        -Replacement "" `
        -Boundary "wasm-bindgen final export"

    $runtimeImport = "import {`n  initSync,`n  PcmRing,`n} from `"/wasm/kotae_pcm_ring.js`";"
    $ringRuntime = Replace-LiteralExactlyOnce `
        -Text $ringRuntime `
        -Needle $runtimeImport `
        -Replacement "" `
        -Boundary "PCM ring runtime import"
    $ringRuntime = Replace-LiteralExactlyOnce `
        -Text $ringRuntime `
        -Needle "export function createPcmRing" `
        -Replacement "function createPcmRing" `
        -Boundary "PCM ring runtime export"

    $captureSource = Replace-LiteralExactlyOnce `
        -Text $captureSource `
        -Needle 'import { createPcmRing } from "./pcm-ring-worklet-runtime.js";' `
        -Replacement "" `
        -Boundary "PCM capture runtime import"

    $bundle = @(
        "// Generated deterministically by scripts/build-web.ps1. Do not edit dist.",
        "// BEGIN audited wasm-bindgen sync glue",
        $ringGlue.TrimEnd([char[]] "`n"),
        "// END audited wasm-bindgen sync glue",
        "// BEGIN audited PCM ring runtime",
        $ringRuntime.Trim([char[]] "`n"),
        "// END audited PCM ring runtime",
        "// BEGIN audited PCM capture processor",
        $captureSource.TrimStart([char[]] "`n"),
        "// END audited PCM capture processor"
    ) -join "`n`n"
    $bundle = $bundle.TrimEnd([char[]] "`n") + "`n"

    if (
        $bundle -match '(?m)^\s*(?:import|export)\b' -or
        $bundle.IndexOf("import.meta", [System.StringComparison]::Ordinal) -ge 0 -or
        $bundle.IndexOf("__wbg_load", [System.StringComparison]::Ordinal) -ge 0 -or
        $bundle.IndexOf("__wbg_init", [System.StringComparison]::Ordinal) -ge 0 -or
        $bundle.IndexOf("TextDecoder", [System.StringComparison]::Ordinal) -ge 0 -or
        $bundle.IndexOf("getStringFromWasm0", [System.StringComparison]::Ordinal) -ge 0 -or
        $bundle.IndexOf("decodeText", [System.StringComparison]::Ordinal) -ge 0 -or
        [regex]::Matches(
            $bundle,
            [regex]::Escape("__wbg___wbindgen_throw_344f42d3211c4765")
        ).Count -ne 1 -or
        [regex]::Matches(
            $bundle,
            [regex]::Escape('throw new Error("pcm_ring_wasm_failure");')
        ).Count -ne 1 -or
        [regex]::Matches($bundle, [regex]::Escape("initSync({ module });")).Count -ne 1 -or
        [regex]::Matches(
            $bundle,
            [regex]::Escape('registerProcessor("kotae-pcm-capture", KotaePcmCaptureProcessor);')
        ).Count -ne 1
    ) {
        throw "PCM Worklet bundle is not a single reviewed synchronous module."
    }
    $ringClassIndex = $bundle.IndexOf("class PcmRing", [System.StringComparison]::Ordinal)
    $runtimeIndex = $bundle.IndexOf("function createPcmRing", [System.StringComparison]::Ordinal)
    $captureIndex = $bundle.IndexOf("class KotaePcmCaptureProcessor", [System.StringComparison]::Ordinal)
    $registrationIndex = $bundle.IndexOf(
        'registerProcessor("kotae-pcm-capture", KotaePcmCaptureProcessor);',
        [System.StringComparison]::Ordinal
    )
    if (
        $ringClassIndex -lt 0 -or
        $runtimeIndex -le $ringClassIndex -or
        $captureIndex -le $runtimeIndex -or
        $registrationIndex -le $captureIndex -or
        $bundle.Length -gt 1MB
    ) {
        throw "PCM Worklet bundle order or size is outside the reviewed boundary."
    }

    [System.IO.File]::WriteAllText(
        (Join-Path $stagingRoot "pcm-capture-worklet.js"),
        $bundle,
        [System.Text.UTF8Encoding]::new($false)
    )
    Copy-Item `
        -LiteralPath $ringWasmPath `
        -Destination (Join-Path $wasmOutput "kotae_pcm_ring_bg.wasm")
    Copy-Item -LiteralPath $cssSource -Destination (Join-Path $assetsOutput "main.css")

    $requiredFiles = @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "passkey-policy.mjs",
        "pcm-capture-worklet.js",
        "voice-session-policy.mjs",
        "voice-prepare-slo-policy.mjs",
        "voice-start-slo-policy.mjs",
        "voice-stream-policy.mjs",
        "assets\main.css",
        "wasm\kotae_client.js",
        "wasm\kotae_client_bg.wasm",
        "wasm\kotae_pcm_ring_bg.wasm"
    )
    foreach ($relativePath in $requiredFiles) {
        if (-not (Test-Path -LiteralPath (Join-Path $stagingRoot $relativePath) -PathType Leaf)) {
            throw "Generated web artifact is missing: $relativePath"
        }
    }
    $pcmRingWasmArtifact = Get-Item -LiteralPath (
        Join-Path $stagingRoot "wasm\kotae_pcm_ring_bg.wasm"
    )
    if ($pcmRingWasmArtifact.Length -le 0 -or $pcmRingWasmArtifact.Length -gt 256KB) {
        throw "PCM ring Wasm artifact must stay inside its 256 KiB runtime boundary."
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
    if ($bridge -notmatch [regex]::Escape('from "./voice-start-slo-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice start SLO policy module."
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
                "voice-prepare-slo-policy.mjs",
                "voice-start-slo-policy.mjs",
                "voice-stream-policy.mjs",
                "assets/main.css",
                "wasm/kotae_client.js",
                "wasm/kotae_client_bg.wasm",
                "wasm/kotae_pcm_ring_bg.wasm"
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
    if (Test-Path -LiteralPath $pcmRingBindgenOutput) {
        Remove-Item -LiteralPath $pcmRingBindgenOutput -Recurse -Force
    }
}
} finally {
    $buildLock.Dispose()
}
