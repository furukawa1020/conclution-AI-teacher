[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $WasmBindgenPath,

    [string] $CargoPath = "cargo"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "rust-wasm-toolchain.ps1")

function Assert-Rejected {
    param(
        [Parameter(Mandatory)]
        [string] $Case,

        [Parameter(Mandatory)]
        [scriptblock] $Operation
    )

    $rejected = $false
    try {
        & $Operation
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Invalid Rust/Wasm toolchain fixture was accepted: $Case"
    }
}

$configuration = Get-ReleaseToolchainConfiguration
$toolchain = Assert-ReleaseToolchain `
    -WorkspaceRoot $workspace `
    -CargoPath $CargoPath `
    -WasmBindgenPath ([System.IO.Path]::GetFullPath($WasmBindgenPath)) `
    -Configuration $configuration
Assert-ReleaseToolchainProvenance `
    -Toolchain $toolchain `
    -Configuration $configuration

$badProvenance = [ordered]@{}
foreach ($property in $toolchain.PSObject.Properties) {
    $badProvenance[$property.Name] = $property.Value
}
$badProvenance.wasmBindgenExecutableSha256 = "0" * 64
Assert-Rejected -Case "provenance digest mismatch" -Operation {
    Assert-ReleaseToolchainProvenance `
        -Toolchain ([pscustomobject] $badProvenance) `
        -Configuration $configuration
}

$fixtureRoot = [System.IO.Path]::GetFullPath(
    (Join-Path (
        [System.IO.Path]::GetTempPath()
    ) ("kotae-toolchain-path-" + [Guid]::NewGuid().ToString("N")))
)
$realRoot = Join-Path $fixtureRoot "real"
$linkRoot = Join-Path $fixtureRoot "link"
New-Item -ItemType Directory -Path $realRoot -Force | Out-Null
$fixtureLeaf = Join-Path $realRoot "fixture.bin"
[System.IO.File]::WriteAllBytes($fixtureLeaf, [byte[]] @(1, 2, 3, 4))

try {
    $platformIdentity = $configuration.wasmBindgen.platforms.PSObject.Properties[
        [string] $toolchain.platform
    ].Value
    $mutatedExecutable = Join-Path $realRoot ([string] $platformIdentity.executableName)
    Copy-Item `
        -LiteralPath ([System.IO.Path]::GetFullPath($WasmBindgenPath)) `
        -Destination $mutatedExecutable
    $mutatedStream = [System.IO.File]::Open(
        $mutatedExecutable,
        [System.IO.FileMode]::Open,
        [System.IO.FileAccess]::ReadWrite,
        [System.IO.FileShare]::None
    )
    try {
        $firstByte = $mutatedStream.ReadByte()
        if ($firstByte -lt 0) {
            throw "Mutated executable fixture is unexpectedly empty."
        }
        $mutatedStream.Position = 0
        $mutatedStream.WriteByte([byte] ($firstByte -bxor 1))
    } finally {
        $mutatedStream.Dispose()
    }
    Assert-Rejected -Case "executable digest mismatch" -Operation {
        Assert-WasmBindgenExecutable `
            -WasmBindgenPath ([System.IO.Path]::GetFullPath($mutatedExecutable)) `
            -Configuration $configuration
    }

    $canonicalLeaf = [System.IO.Path]::GetFullPath($fixtureLeaf)
    $actualCanonical = Assert-CanonicalLeafPath `
        -Path $canonicalLeaf `
        -Boundary "Canonical path fixture"
    if ($actualCanonical -cne $canonicalLeaf) {
        throw "Canonical path fixture changed unexpectedly."
    }
    $fallbackLeaf = Join-Path $realRoot "z-fixture.bin"
    [System.IO.File]::WriteAllBytes($fallbackLeaf, [byte[]] @(5, 6, 7, 8))
    $selectedPreferred = Select-CanonicalApplicationPath `
        -CandidatePaths @($fallbackLeaf, $canonicalLeaf, $fallbackLeaf) `
        -PreferredPaths @($canonicalLeaf) `
        -Boundary "Preferred application fixture"
    if ($selectedPreferred -cne $canonicalLeaf) {
        throw "Preferred application path was not selected deterministically."
    }
    $selectedFallback = Select-CanonicalApplicationPath `
        -CandidatePaths @($fallbackLeaf, $canonicalLeaf, $fallbackLeaf) `
        -Boundary "Fallback application fixture"
    if ($selectedFallback -cne $canonicalLeaf) {
        throw "Application fallback ordering is not deterministic."
    }
    Assert-Rejected -Case "zero application candidates" -Operation {
        Select-CanonicalApplicationPath `
            -CandidatePaths @() `
            -Boundary "Missing application fixture"
    }
    Assert-Rejected -Case "relative path" -Operation {
        Assert-CanonicalLeafPath `
            -Path ".\fixture.bin" `
            -Boundary "Relative path fixture"
    }
    Assert-Rejected -Case "non-canonical parent segment" -Operation {
        $parentSegmentPath = Join-Path (
            Join-Path (
                Join-Path $realRoot "child"
            ) ".."
        ) "fixture.bin"
        Assert-CanonicalLeafPath `
            -Path $parentSegmentPath `
            -Boundary "Parent segment fixture"
    }

    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Windows
        )) {
        New-Item -ItemType Junction -Path $linkRoot -Target $realRoot | Out-Null
    } else {
        New-Item -ItemType SymbolicLink -Path $linkRoot -Target $realRoot | Out-Null
    }
    Assert-Rejected -Case "symlink or reparse ancestor" -Operation {
        Assert-CanonicalLeafPath `
            -Path ([System.IO.Path]::GetFullPath((Join-Path $linkRoot "fixture.bin"))) `
            -Boundary "Linked path fixture"
    }
    Assert-Rejected -Case "selected application link ancestor" -Operation {
        Select-CanonicalApplicationPath `
            -CandidatePaths @(
                [System.IO.Path]::GetFullPath((Join-Path $linkRoot "fixture.bin")),
                $canonicalLeaf
            ) `
            -PreferredPaths @(
                [System.IO.Path]::GetFullPath((Join-Path $linkRoot "fixture.bin"))
            ) `
            -Boundary "Linked application fixture"
    }
} finally {
    if (Test-Path -LiteralPath $linkRoot) {
        [System.IO.Directory]::Delete($linkRoot, $false)
    }
    if (Test-Path -LiteralPath $fixtureRoot) {
        Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
    }
}

Write-Output "RUST_WASM_TOOLCHAIN_FIXTURES=PASS"
Write-Output "RUST_WASM_TOOLCHAIN_PLATFORM=$($toolchain.platform)"
