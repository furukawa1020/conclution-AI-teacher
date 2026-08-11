[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
$policyPath = Join-Path $PSScriptRoot "rust-wasm-toolchain.ps1"
. $policyPath

$configuration = Get-ReleaseToolchainConfiguration
$platformKey = Get-ReleaseToolchainPlatformKey
$platform = $configuration.wasmBindgen.platforms.PSObject.Properties[
    $platformKey
].Value
$toolRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace ".tools"))
$versionRoot = Join-Path $toolRoot "wasm-bindgen-$($configuration.wasmBindgen.version)"
$installRoot = Join-Path $versionRoot $platformKey
$executablePath = [System.IO.Path]::GetFullPath(
    (Join-Path $installRoot ([string] $platform.executableName))
)

if (Test-Path -LiteralPath $executablePath -PathType Leaf) {
    $verified = Assert-WasmBindgenExecutable `
        -WasmBindgenPath $executablePath `
        -Configuration $configuration
    Write-Output "WASM_BINDGEN=$verified"
    return
}
if (Test-Path -LiteralPath $installRoot) {
    throw "Refusing to replace a partial or unreviewed wasm-bindgen installation."
}

$cacheRoot = [System.IO.Path]::GetFullPath(
    (Join-Path (Join-Path $workspace ".cache") "toolchain-install")
)
New-Item -ItemType Directory -Force -Path $cacheRoot | Out-Null
if (
    ((Get-Item -LiteralPath $cacheRoot -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Toolchain installation cache must not be a reparse point."
}
$stagingRoot = Join-Path $cacheRoot ([Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $stagingRoot | Out-Null
$archivePath = Join-Path $stagingRoot ([string] $platform.archiveName)
$extractRoot = Join-Path $stagingRoot "extract"
New-Item -ItemType Directory -Path $extractRoot | Out-Null

try {
    $curl = Get-Command `
        "curl.exe" `
        -CommandType Application `
        -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $curl) {
        $curl = Get-Command `
            "curl" `
            -CommandType Application `
            -ErrorAction Stop |
            Select-Object -First 1
    }
    & $curl.Source `
        --fail `
        --location `
        --silent `
        --show-error `
        --retry 3 `
        --output $archivePath `
        ([string] $platform.source)
    if ($LASTEXITCODE -ne 0) {
        throw "wasm-bindgen release archive download failed."
    }
    $actualArchiveSha256 = (
        Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath
    ).Hash.ToLowerInvariant()
    if ($actualArchiveSha256 -cne [string] $platform.archiveSha256) {
        throw "wasm-bindgen release archive SHA-256 does not match the reviewed asset."
    }

    $tar = Get-Command `
        "tar" `
        -CommandType Application `
        -ErrorAction Stop |
        Select-Object -First 1
    $archiveEntries = @(& $tar.Source -tzf $archivePath)
    if ($LASTEXITCODE -ne 0 -or $archiveEntries.Count -ne 7) {
        throw "wasm-bindgen release archive shape is outside the reviewed boundary."
    }
    $archiveRootName = [System.IO.Path]::GetFileNameWithoutExtension(
        [System.IO.Path]::GetFileNameWithoutExtension([string] $platform.archiveName)
    )
    $expectedEntries = @(
        "$archiveRootName/",
        "$archiveRootName/LICENSE-APACHE",
        "$archiveRootName/LICENSE-MIT",
        "$archiveRootName/README.md",
        "$archiveRootName/$($platform.executableName)"
    )
    $unexpectedEntries = @($archiveEntries | Where-Object {
        $_ -notin $expectedEntries -and
        $_ -cne "$archiveRootName/wasm-bindgen-test-runner$([System.IO.Path]::GetExtension([string] $platform.executableName))" -and
        $_ -cne "$archiveRootName/wasm2es6js$([System.IO.Path]::GetExtension([string] $platform.executableName))"
    })
    if ($unexpectedEntries.Count -ne 0) {
        throw "wasm-bindgen release archive contains an unexpected path."
    }

    & $tar.Source -xzf $archivePath -C $extractRoot
    if ($LASTEXITCODE -ne 0) {
        throw "wasm-bindgen release archive extraction failed."
    }
    $extractedRoot = Join-Path $extractRoot $archiveRootName
    $extractedEntries = @(Get-ChildItem -LiteralPath $extractedRoot -Recurse -Force)
    if (
        $extractedEntries.Count -ne 6 -or
        @($extractedEntries | Where-Object {
            $_.PSIsContainer -or
            ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0
        }).Count -ne 0
    ) {
        throw "wasm-bindgen release archive extracted outside the reviewed file boundary."
    }
    $extractedExecutable = [System.IO.Path]::GetFullPath(
        (Join-Path $extractedRoot ([string] $platform.executableName))
    )
    $null = Assert-WasmBindgenExecutable `
        -WasmBindgenPath $extractedExecutable `
        -Configuration $configuration

    New-Item -ItemType Directory -Force -Path $versionRoot | Out-Null
    if (
        ((Get-Item -LiteralPath $versionRoot -Force).Attributes -band
            [System.IO.FileAttributes]::ReparsePoint) -ne 0
    ) {
        throw "wasm-bindgen install parent must not be a reparse point."
    }
    Move-Item -LiteralPath $extractedRoot -Destination $installRoot
    $verified = Assert-WasmBindgenExecutable `
        -WasmBindgenPath $executablePath `
        -Configuration $configuration
    Write-Output "WASM_BINDGEN=$verified"
} finally {
    if (Test-Path -LiteralPath $stagingRoot) {
        Remove-Item -LiteralPath $stagingRoot -Recurse -Force
    }
}
