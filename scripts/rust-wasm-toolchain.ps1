$script:ReleaseToolchainWorkspace = Split-Path -Parent $PSScriptRoot
$script:ReleaseToolchainIdentityPath = Join-Path `
    (Join-Path $script:ReleaseToolchainWorkspace "config") `
    "release-toolchain.json"

function Assert-ExactObjectProperties {
    param(
        [Parameter(Mandatory)]
        [object] $Value,

        [Parameter(Mandatory)]
        [string[]] $Expected,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    if ($null -eq $Value) {
        throw "$Boundary is missing."
    }
    $actualNames = @($Value.PSObject.Properties.Name | Sort-Object)
    $expectedNames = @($Expected | Sort-Object)
    if (($actualNames -join ",") -cne ($expectedNames -join ",")) {
        throw "$Boundary properties are outside the reviewed boundary."
    }
}

function Get-ReleaseToolchainPlatformKey {
    if (
        [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne
            [System.Runtime.InteropServices.Architecture]::X64
    ) {
        throw "Rust/Wasm release tools support only reviewed x86_64 hosts."
    }
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Windows
        )) {
        return "windows-x86_64"
    }
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Linux
        )) {
        return "linux-x86_64"
    }
    throw "Rust/Wasm release tools support only reviewed Windows and Linux hosts."
}

function Get-PathStringComparison {
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Windows
        )) {
        return [System.StringComparison]::OrdinalIgnoreCase
    }
    return [System.StringComparison]::Ordinal
}

function Assert-CanonicalLeafPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $Path,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    if (
        [string]::IsNullOrWhiteSpace($Path) -or
        -not [System.IO.Path]::IsPathRooted($Path)
    ) {
        throw "$Boundary must use an absolute path."
    }
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $comparison = Get-PathStringComparison
    $inputPath = $Path.TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    $normalizedPath = $fullPath.TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    if (-not [string]::Equals($inputPath, $normalizedPath, $comparison)) {
        throw "$Boundary path must already be canonical."
    }
    if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
        throw "$Boundary does not exist as a regular file."
    }

    $currentPath = $fullPath
    while (-not [string]::IsNullOrWhiteSpace($currentPath)) {
        $entry = Get-Item -LiteralPath $currentPath -Force
        $linkTypeProperty = $entry.PSObject.Properties["LinkType"]
        $isLink = (
            ($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
            (
                $null -ne $linkTypeProperty -and
                [string] $linkTypeProperty.Value -in @("SymbolicLink", "Junction")
            )
        )
        if ($isLink) {
            throw "$Boundary path crosses a symbolic link or reparse point."
        }

        $trimmed = $currentPath.TrimEnd(
            [System.IO.Path]::DirectorySeparatorChar,
            [System.IO.Path]::AltDirectorySeparatorChar
        )
        $parentPath = [System.IO.Path]::GetDirectoryName($trimmed)
        if (
            [string]::IsNullOrWhiteSpace($parentPath) -or
            [string]::Equals($parentPath, $currentPath, $comparison)
        ) {
            break
        }
        $currentPath = $parentPath
    }
    return $fullPath
}

function Select-CanonicalApplicationPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [AllowNull()]
        [AllowEmptyCollection()]
        [string[]] $CandidatePaths,

        [AllowNull()]
        [AllowEmptyCollection()]
        [string[]] $PreferredPaths = @(),

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    $comparison = Get-PathStringComparison
    $canonicalCandidates = @()
    foreach ($candidatePath in @($CandidatePaths)) {
        if (
            [string]::IsNullOrWhiteSpace($candidatePath) -or
            -not [System.IO.Path]::IsPathRooted($candidatePath)
        ) {
            throw "$Boundary returned an invalid application path."
        }
        $canonicalCandidate = [System.IO.Path]::GetFullPath($candidatePath)
        $duplicate = $false
        foreach ($existingPath in $canonicalCandidates) {
            if ([string]::Equals($canonicalCandidate, $existingPath, $comparison)) {
                $duplicate = $true
                break
            }
        }
        if (-not $duplicate) {
            $canonicalCandidates += $canonicalCandidate
        }
    }
    if ($canonicalCandidates.Count -eq 0) {
        throw "$Boundary is not installed."
    }

    $selectedPath = $null
    foreach ($preferredPath in @($PreferredPaths)) {
        if ([string]::IsNullOrWhiteSpace($preferredPath)) {
            continue
        }
        if (-not [System.IO.Path]::IsPathRooted($preferredPath)) {
            throw "$Boundary preferred path must be absolute."
        }
        $canonicalPreferred = [System.IO.Path]::GetFullPath($preferredPath)
        foreach ($candidatePath in $canonicalCandidates) {
            if ([string]::Equals($canonicalPreferred, $candidatePath, $comparison)) {
                $selectedPath = $candidatePath
                break
            }
        }
        if ($null -ne $selectedPath) {
            break
        }
    }

    if ($null -eq $selectedPath) {
        foreach ($candidatePath in $canonicalCandidates) {
            if (
                $null -eq $selectedPath -or
                [string]::Compare($candidatePath, $selectedPath, $comparison) -lt 0
            ) {
                $selectedPath = $candidatePath
            }
        }
    }
    return Assert-CanonicalLeafPath -Path $selectedPath -Boundary $Boundary
}

function Get-ReleaseToolchainConfiguration {
    [CmdletBinding()]
    param(
        [string] $IdentityPath = $script:ReleaseToolchainIdentityPath
    )

    $identityFullPath = [System.IO.Path]::GetFullPath($IdentityPath)
    $null = Assert-CanonicalLeafPath `
        -Path $identityFullPath `
        -Boundary "Release toolchain identity"
    $entry = Get-Item -LiteralPath $identityFullPath -Force
    if ($entry.Length -le 0 -or $entry.Length -gt 32KB) {
        throw "Release toolchain identity size is outside the reviewed boundary."
    }
    try {
        $identity = [System.IO.File]::ReadAllText(
            $identityFullPath,
            [System.Text.UTF8Encoding]::new($false, $true)
        ) | ConvertFrom-Json -ErrorAction Stop
    } catch {
        throw "Release toolchain identity must be valid UTF-8 JSON."
    }

    Assert-ExactObjectProperties `
        -Value $identity `
        -Expected @("schemaVersion", "rust", "wasmBindgen") `
        -Boundary "Release toolchain identity"
    Assert-ExactObjectProperties `
        -Value $identity.rust `
        -Expected @(
            "toolchain",
            "rustcCommit",
            "cargoCommit",
            "target",
            "channelManifestSource",
            "channelManifestSha256",
            "hosts"
        ) `
        -Boundary "Rust release identity"
    Assert-ExactObjectProperties `
        -Value $identity.rust.hosts `
        -Expected @("windows-x86_64", "linux-x86_64") `
        -Boundary "Rust host identity"
    Assert-ExactObjectProperties `
        -Value $identity.wasmBindgen `
        -Expected @("version", "releasePage", "platforms") `
        -Boundary "wasm-bindgen release identity"
    Assert-ExactObjectProperties `
        -Value $identity.wasmBindgen.platforms `
        -Expected @("windows-x86_64", "linux-x86_64") `
        -Boundary "wasm-bindgen platform identity"

    if (
        [int] $identity.schemaVersion -ne 1 -or
        [string] $identity.rust.toolchain -cne "1.93.0" -or
        [string] $identity.rust.rustcCommit -cnotmatch '^[0-9a-f]{40}$' -or
        [string] $identity.rust.cargoCommit -cnotmatch '^[0-9a-f]{40}$' -or
        [string] $identity.rust.target -cne "wasm32-unknown-unknown" -or
        [string] $identity.rust.channelManifestSource -cne
            "https://static.rust-lang.org/dist/channel-rust-1.93.0.toml" -or
        [string] $identity.rust.channelManifestSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string] $identity.wasmBindgen.version -cne "0.2.126" -or
        [string] $identity.wasmBindgen.releasePage -cne
            "https://github.com/wasm-bindgen/wasm-bindgen/releases/tag/0.2.126"
    ) {
        throw "Release toolchain identity is outside the reviewed versions."
    }

    foreach ($platformKey in @("windows-x86_64", "linux-x86_64")) {
        $platform = $identity.wasmBindgen.platforms.PSObject.Properties[$platformKey].Value
        Assert-ExactObjectProperties `
            -Value $platform `
            -Expected @(
                "archiveName",
                "archiveSha256",
                "executableName",
                "executableSha256",
                "source"
            ) `
            -Boundary "wasm-bindgen $platformKey identity"
        if (
            [string] $platform.archiveSha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string] $platform.executableSha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string] $platform.source -cne (
                "https://github.com/wasm-bindgen/wasm-bindgen/releases/download/0.2.126/" +
                [string] $platform.archiveName
            ) -or
            [System.IO.Path]::GetFileName([string] $platform.executableName) -cne
                [string] $platform.executableName
        ) {
            throw "wasm-bindgen $platformKey identity is invalid."
        }
    }
    return $identity
}

function Assert-RustToolchainFile {
    param(
        [Parameter(Mandatory)]
        [string] $WorkspaceRoot
    )

    $toolchainPath = [System.IO.Path]::GetFullPath(
        (Join-Path $WorkspaceRoot "rust-toolchain.toml")
    )
    $null = Assert-CanonicalLeafPath `
        -Path $toolchainPath `
        -Boundary "Rust toolchain file"
    $actual = [System.IO.File]::ReadAllText(
        $toolchainPath,
        [System.Text.UTF8Encoding]::new($false, $true)
    ).Replace("`r`n", "`n").Replace("`r", "`n").TrimEnd() + "`n"
    $expected = @'
[toolchain]
channel = "1.93.0"
profile = "minimal"
targets = ["wasm32-unknown-unknown"]
'@.Replace("`r`n", "`n").Replace("`r", "`n").TrimEnd() + "`n"
    if ($actual -cne $expected) {
        throw "rust-toolchain.toml does not match the reviewed release toolchain."
    }
}

function Invoke-ToolIdentityText {
    param(
        [Parameter(Mandatory)]
        [System.Management.Automation.ApplicationInfo] $Command,

        [Parameter(Mandatory)]
        [string[]] $Arguments,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    $lines = @(& $Command.Source @Arguments 2>$null)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "$Boundary identity command failed."
    }
    $text = ($lines -join "`n").Trim()
    if ([string]::IsNullOrWhiteSpace($text) -or $text.Length -gt 8KB) {
        throw "$Boundary identity output is outside the reviewed boundary."
    }
    return $text
}

function ConvertFrom-VerboseVersionText {
    param(
        [Parameter(Mandatory)]
        [string] $Text,

        [Parameter(Mandatory)]
        [string] $CommandName
    )

    $lines = @($Text -split "`n")
    if ($lines.Count -lt 4) {
        throw "$CommandName verbose version output is incomplete."
    }
    $properties = @{}
    foreach ($line in $lines | Select-Object -Skip 1) {
        $separator = $line.IndexOf(":", [System.StringComparison]::Ordinal)
        if ($separator -le 0) {
            continue
        }
        $name = $line.Substring(0, $separator).Trim()
        if ($properties.ContainsKey($name)) {
            throw "$CommandName verbose version contains a duplicate property."
        }
        $properties[$name] = $line.Substring($separator + 1).Trim()
    }
    return [pscustomobject]@{
        Header = $lines[0].Trim()
        Properties = $properties
    }
}

function Assert-WasmBindgenExecutable {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $WasmBindgenPath,

        [object] $Configuration = $(Get-ReleaseToolchainConfiguration)
    )

    $platformKey = Get-ReleaseToolchainPlatformKey
    $platform = $Configuration.wasmBindgen.platforms.PSObject.Properties[
        $platformKey
    ].Value
    $fullPath = [System.IO.Path]::GetFullPath($WasmBindgenPath)
    $null = Assert-CanonicalLeafPath `
        -Path $fullPath `
        -Boundary "wasm-bindgen executable"
    if ([System.IO.Path]::GetFileName($fullPath) -cne [string] $platform.executableName) {
        throw "wasm-bindgen executable name does not match the reviewed platform asset."
    }
    $actualSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $fullPath).
        Hash.ToLowerInvariant()
    if ($actualSha256 -cne [string] $platform.executableSha256) {
        throw "wasm-bindgen executable SHA-256 does not match the reviewed asset."
    }
    $command = Get-Command $fullPath -CommandType Application -ErrorAction Stop
    $version = Invoke-ToolIdentityText `
        -Command $command `
        -Arguments @("--version") `
        -Boundary "wasm-bindgen"
    if ($version -cne "wasm-bindgen $($Configuration.wasmBindgen.version)") {
        throw "wasm-bindgen version does not match the reviewed asset."
    }
    return $fullPath
}

function Assert-ReleaseToolchainProvenance {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [object] $Toolchain,

        [object] $Configuration = $(Get-ReleaseToolchainConfiguration)
    )

    Assert-ExactObjectProperties `
        -Value $Toolchain `
        -Expected @(
            "platform",
            "rustToolchain",
            "rustcCommit",
            "cargoCommit",
            "rustHost",
            "rustTarget",
            "rustChannelManifestSource",
            "rustChannelManifestSha256",
            "wasmBindgenVersion",
            "wasmBindgenArchiveName",
            "wasmBindgenArchiveSha256",
            "wasmBindgenExecutableSha256",
            "wasmBindgenSource"
        ) `
        -Boundary "Rust/Wasm release provenance"
    $platformKey = [string] $Toolchain.platform
    if ($platformKey -notin @("windows-x86_64", "linux-x86_64")) {
        throw "Rust/Wasm release provenance platform is not reviewed."
    }
    $platform = $Configuration.wasmBindgen.platforms.PSObject.Properties[
        $platformKey
    ].Value
    $allowedHosts = @(
        $Configuration.rust.hosts.PSObject.Properties[$platformKey].Value
    )
    if (
        [string] $Toolchain.rustToolchain -cne [string] $Configuration.rust.toolchain -or
        [string] $Toolchain.rustcCommit -cne [string] $Configuration.rust.rustcCommit -or
        [string] $Toolchain.cargoCommit -cne [string] $Configuration.rust.cargoCommit -or
        $allowedHosts -cnotcontains [string] $Toolchain.rustHost -or
        [string] $Toolchain.rustTarget -cne [string] $Configuration.rust.target -or
        [string] $Toolchain.rustChannelManifestSource -cne
            [string] $Configuration.rust.channelManifestSource -or
        [string] $Toolchain.rustChannelManifestSha256 -cne
            [string] $Configuration.rust.channelManifestSha256 -or
        [string] $Toolchain.wasmBindgenVersion -cne
            [string] $Configuration.wasmBindgen.version -or
        [string] $Toolchain.wasmBindgenArchiveName -cne [string] $platform.archiveName -or
        [string] $Toolchain.wasmBindgenArchiveSha256 -cne [string] $platform.archiveSha256 -or
        [string] $Toolchain.wasmBindgenExecutableSha256 -cne
            [string] $platform.executableSha256 -or
        [string] $Toolchain.wasmBindgenSource -cne [string] $platform.source
    ) {
        throw "Rust/Wasm release provenance does not match the reviewed identity."
    }
}

function Assert-ReleaseToolchain {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $WorkspaceRoot,

        [Parameter(Mandatory)]
        [string] $CargoPath,

        [Parameter(Mandatory)]
        [string] $WasmBindgenPath,

        [object] $Configuration = $(Get-ReleaseToolchainConfiguration)
    )

    $workspace = [System.IO.Path]::GetFullPath($WorkspaceRoot)
    Assert-RustToolchainFile -WorkspaceRoot $workspace
    $cargoCommand = Get-Command $CargoPath -CommandType Application -ErrorAction Stop
    $rustcCommand = Get-Command "rustc" -CommandType Application -ErrorAction Stop
    $cargoPath = [System.IO.Path]::GetFullPath($cargoCommand.Source)
    $rustcPath = [System.IO.Path]::GetFullPath($rustcCommand.Source)
    $null = Assert-CanonicalLeafPath -Path $cargoPath -Boundary "Cargo executable"
    $null = Assert-CanonicalLeafPath -Path $rustcPath -Boundary "rustc executable"
    $wasmBindgen = Assert-WasmBindgenExecutable `
        -WasmBindgenPath $WasmBindgenPath `
        -Configuration $Configuration

    $toolchainArgument = "+$($Configuration.rust.toolchain)"
    Push-Location $workspace
    try {
        $rustcText = Invoke-ToolIdentityText `
            -Command $rustcCommand `
            -Arguments @($toolchainArgument, "--version", "--verbose") `
            -Boundary "rustc"
        $cargoText = Invoke-ToolIdentityText `
            -Command $cargoCommand `
            -Arguments @($toolchainArgument, "--version", "--verbose") `
            -Boundary "Cargo"
        $targetLibdirText = Invoke-ToolIdentityText `
            -Command $rustcCommand `
            -Arguments @(
                $toolchainArgument,
                "--print",
                "target-libdir",
                "--target",
                [string] $Configuration.rust.target
            ) `
            -Boundary "Rust target"
    } finally {
        Pop-Location
    }

    $rustc = ConvertFrom-VerboseVersionText -Text $rustcText -CommandName "rustc"
    $cargo = ConvertFrom-VerboseVersionText -Text $cargoText -CommandName "Cargo"
    $platformKey = Get-ReleaseToolchainPlatformKey
    $allowedHosts = @(
        $Configuration.rust.hosts.PSObject.Properties[$platformKey].Value
    )
    if (
        $rustc.Header -cnotmatch '^rustc 1\.93\.0 \([0-9a-f]+ 2026-01-19\)$' -or
        [string] $rustc.Properties["release"] -cne [string] $Configuration.rust.toolchain -or
        [string] $rustc.Properties["commit-hash"] -cne
            [string] $Configuration.rust.rustcCommit -or
        $allowedHosts -cnotcontains [string] $rustc.Properties["host"] -or
        $cargo.Header -cnotmatch '^cargo 1\.93\.0 \([0-9a-f]+ 2025-12-15\)$' -or
        [string] $cargo.Properties["release"] -cne [string] $Configuration.rust.toolchain -or
        [string] $cargo.Properties["commit-hash"] -cne
            [string] $Configuration.rust.cargoCommit -or
        [string] $cargo.Properties["host"] -cne [string] $rustc.Properties["host"]
    ) {
        throw "Rust release toolchain version, commit, or host does not match the reviewed identity."
    }
    $targetLibdir = [System.IO.Path]::GetFullPath($targetLibdirText.Trim())
    if (
        -not (Test-Path -LiteralPath $targetLibdir -PathType Container) -or
        @(Get-ChildItem -LiteralPath $targetLibdir -Filter "libcore-*.rlib" -File).Count -ne 1
    ) {
        throw "The reviewed Rust Wasm target is not installed."
    }

    $platform = $Configuration.wasmBindgen.platforms.PSObject.Properties[
        $platformKey
    ].Value
    $provenance = [ordered]@{
        platform = $platformKey
        rustToolchain = [string] $Configuration.rust.toolchain
        rustcCommit = [string] $Configuration.rust.rustcCommit
        cargoCommit = [string] $Configuration.rust.cargoCommit
        rustHost = [string] $rustc.Properties["host"]
        rustTarget = [string] $Configuration.rust.target
        rustChannelManifestSource = [string] $Configuration.rust.channelManifestSource
        rustChannelManifestSha256 = [string] $Configuration.rust.channelManifestSha256
        wasmBindgenVersion = [string] $Configuration.wasmBindgen.version
        wasmBindgenArchiveName = [string] $platform.archiveName
        wasmBindgenArchiveSha256 = [string] $platform.archiveSha256
        wasmBindgenExecutableSha256 = [string] $platform.executableSha256
        wasmBindgenSource = [string] $platform.source
    }
    Assert-ReleaseToolchainProvenance `
        -Toolchain ([pscustomobject] $provenance) `
        -Configuration $Configuration
    return [pscustomobject] $provenance
}
