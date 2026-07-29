[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")]
    [string] $ProjectId,

    [string] $SiteId = "kotae-ai",

    [string] $PublicDirectory = "dist/web",

    [string] $RunService = "kotae-api",

    [string] $RunRegion = "asia-northeast1",

    [string] $GcloudPath = ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
Add-Type -AssemblyName System.Net.Http

$expectedProjectId = "kotae-ai-u22-2026"
$expectedProjectNumber = "551920539470"
$expectedAppId = "1:551920539470:web:6518baf6d84d7ab89eb01f"
$expectedSiteId = "kotae-ai"
$expectedDefaultUrl = "https://kotae-ai.web.app"
$expectedRunService = "kotae-api"
$expectedRunRegion = "asia-northeast1"

$workspace = Split-Path -Parent $PSScriptRoot
$publicRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace $PublicDirectory))
$expectedPublicRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace "dist\web"))
$gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))

if ($ProjectId -cne $expectedProjectId) {
    throw "Refusing to deploy project '$ProjectId'; expected '$expectedProjectId'."
}
if ($SiteId -cne $expectedSiteId) {
    throw "Refusing to deploy Hosting site '$SiteId'; expected '$expectedSiteId'."
}
if ($RunService -cne $expectedRunService -or $RunRegion -cne $expectedRunRegion) {
    throw "Refusing an unexpected Cloud Run target; expected $expectedRunService in $expectedRunRegion."
}
if (-not [string]::Equals(
        $publicRoot.TrimEnd("\", "/"),
        $expectedPublicRoot.TrimEnd("\", "/"),
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
    throw "Refusing to publish outside the generated dist/web directory."
}
if (-not (Test-Path -LiteralPath $publicRoot -PathType Container)) {
    throw "Hosting public directory does not exist: $publicRoot"
}
if (
    ((Get-Item -LiteralPath $publicRoot -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Hosting public directory must not be a reparse point."
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "Google Cloud CLI does not exist: $gcloud"
}

function Get-GzipBytes {
    param(
        [Parameter(Mandatory)]
        [string] $Path
    )

    $inputBytes = [System.IO.File]::ReadAllBytes($Path)
    $output = [System.IO.MemoryStream]::new()
    try {
        $gzip = [System.IO.Compression.GZipStream]::new(
            $output,
            [System.IO.Compression.CompressionLevel]::Optimal,
            $true
        )
        try {
            $gzip.Write($inputBytes, 0, $inputBytes.Length)
        } finally {
            $gzip.Dispose()
        }
        # Prevent PowerShell from unrolling byte[] into an object pipeline.
        return ,$output.ToArray()
    } finally {
        $output.Dispose()
    }
}

function ConvertTo-Sha256Hex {
    param(
        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($sha256.ComputeHash($Bytes))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
}

function Invoke-FirebaseJson {
    param(
        [Parameter(Mandatory)]
        [ValidateSet("Get", "Post", "Patch")]
        [string] $Method,

        [Parameter(Mandatory)]
        [string] $Uri,

        [Parameter(Mandatory)]
        [hashtable] $Headers,

        [object] $Body
    )

    $parameters = @{
        Method      = $Method
        Uri         = $Uri
        Headers     = $Headers
        ContentType = "application/json"
    }
    if ($PSBoundParameters.ContainsKey("Body")) {
        $parameters.Body = $Body | ConvertTo-Json -Depth 20 -Compress
    }
    return Invoke-RestMethod @parameters
}

function Invoke-BinaryUpload {
    param(
        [Parameter(Mandatory)]
        [string] $Uri,

        [Parameter(Mandatory)]
        [string] $AccessToken,

        [Parameter(Mandatory)]
        [string] $QuotaProject,

        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )

    $client = [System.Net.Http.HttpClient]::new()
    $request = [System.Net.Http.HttpRequestMessage]::new(
        [System.Net.Http.HttpMethod]::Post,
        $Uri
    )
    $content = [System.Net.Http.ByteArrayContent]::new($Bytes)
    $response = $null
    try {
        $request.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new(
            "Bearer",
            $AccessToken
        )
        $request.Headers.Add("x-goog-user-project", $QuotaProject)
        $content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new(
            "application/octet-stream"
        )
        $request.Content = $content

        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            $detail = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            throw "Firebase Hosting upload failed with HTTP $([int]$response.StatusCode): $detail"
        }
    } finally {
        $content.Dispose()
        $request.Dispose()
        if ($null -ne $response) {
            $response.Dispose()
        }
        $client.Dispose()
    }
}

function Assert-HostingArtifact {
    param(
        [Parameter(Mandatory)]
        [string] $Root
    )

    $requiredFiles = @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "voice-session-policy.mjs",
        "assets\main.css",
        "wasm\kotae_client.js",
        "wasm\kotae_client_bg.wasm"
    )
    foreach ($relativePath in $requiredFiles) {
        $requiredPath = Join-Path $Root $relativePath
        if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
            throw "Required Hosting artifact is missing: $relativePath"
        }
    }

    $totalBytes = 0L
    $entries = @(Get-ChildItem -LiteralPath $Root -Recurse -Force)
    foreach ($entry in $entries) {
        if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Hosting artifacts must not contain reparse points: $($entry.FullName)"
        }
        if ($entry.PSIsContainer) {
            continue
        }
        $relativePath = $entry.FullName.
            Substring($Root.TrimEnd("\", "/").Length).
            TrimStart("\", "/").
            Replace("\", "/")
        $allowedPath = (
            $relativePath -in @(
                "index.html",
                "bootstrap.js",
                "firebase-bridge.js",
                "voice-session-policy.mjs",
                "assets/main.css",
                "wasm/kotae_client.js",
                "wasm/kotae_client_bg.wasm"
            ) -or
            $relativePath -match '^wasm/snippets/[A-Za-z0-9._/-]+\.js$'
        )
        if (-not $allowedPath) {
            throw "Unexpected Hosting artifact: $relativePath"
        }
        if ($entry.Length -gt 15MB) {
            throw "Hosting artifact exceeds the 15 MiB safety limit: $($entry.FullName)"
        }
        if ($entry.Length -gt ([long]::MaxValue - $totalBytes)) {
            throw "Hosting artifact size overflow."
        }
        $totalBytes += $entry.Length
    }
    if ($totalBytes -gt 25MB) {
        throw "Hosting artifacts exceed the 25 MiB aggregate safety limit."
    }

    $bridgePath = Join-Path $Root "firebase-bridge.js"
    $bridge = [System.IO.File]::ReadAllText($bridgePath, [System.Text.Encoding]::UTF8)
    $siteKeyMatch = [regex]::Match(
        $bridge,
        '(?m)^\s*const\s+RECAPTCHA_SITE_KEY\s*=\s*"(?<key>[^"]+)";\s*$'
    )
    if (
        -not $siteKeyMatch.Success -or
        $siteKeyMatch.Groups["key"].Value -eq "__RECAPTCHA_SITE_KEY__" -or
        $siteKeyMatch.Groups["key"].Value.Length -lt 20
    ) {
        throw "firebase-bridge.js does not contain a configured reCAPTCHA Enterprise site key."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-session-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice session policy module."
    }
    if (
        $bridge -notmatch [regex]::Escape("const EXPECTED_PROJECT_ID = `"$expectedProjectId`";") -or
        $bridge -notmatch [regex]::Escape("const EXPECTED_APP_ID = `"$expectedAppId`";") -or
        $bridge -notmatch [regex]::Escape("const EXPECTED_MESSAGING_SENDER_ID = `"$expectedProjectNumber`";")
    ) {
        throw "firebase-bridge.js is not bound to the expected Firebase project."
    }

    $bootstrap = [System.IO.File]::ReadAllText(
        (Join-Path $Root "bootstrap.js"),
        [System.Text.Encoding]::UTF8
    )
    $bridgeImport = $bootstrap.IndexOf('import("/firebase-bridge.js")', [System.StringComparison]::Ordinal)
    $wasmImport = $bootstrap.IndexOf('import("/wasm/kotae_client.js")', [System.StringComparison]::Ordinal)
    if ($bridgeImport -lt 0 -or $wasmImport -lt 0 -or $bridgeImport -gt $wasmImport) {
        throw "bootstrap.js must load firebase-bridge.js before the Rust/Wasm module."
    }

    $index = [System.IO.File]::ReadAllText(
        (Join-Path $Root "index.html"),
        [System.Text.Encoding]::UTF8
    )
    if (
        [regex]::Matches($index, '<script\b', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase).Count -ne 1 -or
        $index -notmatch '<script\s+type="module"\s+src="/bootstrap\.js"></script>'
    ) {
        throw "index.html must contain only the external bootstrap.js module script."
    }
}

Assert-HostingArtifact -Root $publicRoot

$token = ((& $gcloud auth print-access-token) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($token)) {
    throw "Could not obtain a Google Cloud access token."
}

$headers = @{
    Authorization         = "Bearer $token"
    "x-goog-user-project" = $ProjectId
}
$apiRoot = "https://firebasehosting.googleapis.com/v1beta1"
$versionName = $null

try {
    $cloudProject = Invoke-RestMethod `
        -Method Get `
        -Uri "https://cloudresourcemanager.googleapis.com/v3/projects/$ProjectId" `
        -Headers $headers
    if (
        $cloudProject.projectId -cne $expectedProjectId -or
        $cloudProject.name -cne "projects/$expectedProjectNumber" -or
        $cloudProject.state -cne "ACTIVE"
    ) {
        throw "Google Cloud project identity check failed."
    }

    $site = Invoke-FirebaseJson `
        -Method Get `
        -Uri "$apiRoot/projects/$ProjectId/sites/$SiteId" `
        -Headers $headers
    $validSiteNames = @(
        "projects/$expectedProjectId/sites/$expectedSiteId",
        "projects/$expectedProjectNumber/sites/$expectedSiteId"
    )
    if (
        $validSiteNames -notcontains $site.name -or
        $site.defaultUrl.TrimEnd("/") -cne $expectedDefaultUrl
    ) {
        throw "Firebase Hosting site identity check failed."
    }

    $fileHashes = [ordered]@{}
    $gzipByHash = @{}
    $files = @(Get-ChildItem -LiteralPath $publicRoot -Recurse -File -Force | Sort-Object FullName)
    if ($files.Count -eq 0) {
        throw "Hosting public directory is empty."
    }

    foreach ($file in $files) {
        $relative = $file.FullName.Substring($publicRoot.Length).TrimStart("\", "/").Replace("\", "/")
        $gzipBytes = Get-GzipBytes -Path $file.FullName
        $hash = ConvertTo-Sha256Hex -Bytes $gzipBytes
        $fileHashes["/$relative"] = $hash
        $gzipByHash[$hash] = $gzipBytes
    }

    $version = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/projects/-/sites/$SiteId/versions" `
        -Headers $headers `
        -Body @{
            status = "CREATED"
            labels = @{
                "deployment-tool" = "kotae-secure-rest"
            }
        }
    $versionName = $version.name
    if ([string]::IsNullOrWhiteSpace($versionName)) {
        throw "Firebase Hosting did not return a version name."
    }

    $populate = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/$versionName`:populateFiles" `
        -Headers $headers `
        -Body @{ files = $fileHashes }

    $requiredHashes = @()
    $requiredHashesProperty = $populate.PSObject.Properties["uploadRequiredHashes"]
    if ($null -ne $requiredHashesProperty) {
        $requiredHashes = @($requiredHashesProperty.Value)
    }
    foreach ($hash in $requiredHashes) {
        if ([string]::IsNullOrWhiteSpace($hash)) {
            continue
        }
        if (-not $gzipByHash.ContainsKey($hash)) {
            throw "Firebase requested an unknown file hash."
        }
        $uploadUri = "$($populate.uploadUrl.TrimEnd("/"))/$hash"
        Invoke-BinaryUpload `
            -Uri $uploadUri `
            -AccessToken $token `
            -QuotaProject $ProjectId `
            -Bytes $gzipByHash[$hash]
    }

    $csp = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self' 'wasm-unsafe-eval' https://www.gstatic.com/firebasejs/12.16.0/ https://www.gstatic.com/recaptcha/ https://www.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; script-src-attr 'none'; style-src 'self'; style-src-attr 'unsafe-hashes' 'sha256-biLFinpqYMtWHmXfkA1BPeCY0/fNt46SAZ+BBk5YUog=' 'sha256-aqNNdDLnnrDOnTNdkJpYlAxKVJtLt9CtFLklmInuUAE=' 'sha256-ZdHxw9eWtnxUb3mk6tBS+gIiVUPE3pGM470keHPDFlE='; img-src 'self' data:; font-src 'self'; connect-src 'self' https://identitytoolkit.googleapis.com https://securetoken.googleapis.com https://content-firebaseappcheck.googleapis.com https://www.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; frame-src https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; worker-src 'self'; manifest-src 'self'; upgrade-insecure-requests"
    $hostingConfig = @{
        cleanUrls = $true
        trailingSlashBehavior = "REMOVE"
        headers = @(
            @{
                glob = "**"
                headers = @{
                    "Content-Security-Policy" = $csp
                    "Cross-Origin-Opener-Policy" = "same-origin"
                    "Cross-Origin-Resource-Policy" = "same-origin"
                    "Permissions-Policy" = "camera=(), geolocation=(), microphone=(self), payment=(), usb=()"
                    "Referrer-Policy" = "no-referrer"
                    "X-Content-Type-Options" = "nosniff"
                    "Cache-Control" = "no-store"
                }
            }
        )
        rewrites = @(
            @{
                glob = "/api/**"
                run = @{
                    serviceId = $RunService
                    region = $RunRegion
                }
            },
            @{
                glob = "**"
                path = "/index.html"
            }
        )
    }

    $encodedMask = [System.Uri]::EscapeDataString("status,config")
    $null = Invoke-FirebaseJson `
        -Method Patch `
        -Uri "$apiRoot/$versionName`?updateMask=$encodedMask" `
        -Headers $headers `
        -Body @{
            status = "FINALIZED"
            config = $hostingConfig
        }

    $encodedVersion = [System.Uri]::EscapeDataString($versionName)
    $release = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/projects/-/sites/$SiteId/channels/live/releases?versionName=$encodedVersion" `
        -Headers $headers `
        -Body @{
            message = "KOTAE AI secure Rust/Wasm launch"
        }

    Write-Output "HOSTING_URL=$($site.defaultUrl)"
    Write-Output "HOSTING_VERSION=$versionName"
    Write-Output "HOSTING_RELEASE=$($release.name)"
} catch {
    if (-not [string]::IsNullOrWhiteSpace($versionName)) {
        try {
            $null = Invoke-FirebaseJson `
                -Method Patch `
                -Uri "$apiRoot/$versionName`?updateMask=status" `
                -Headers $headers `
                -Body @{ status = "ABANDONED" }
        } catch {
            Write-Warning "Could not abandon failed Hosting version $versionName."
        }
    }
    throw
}
