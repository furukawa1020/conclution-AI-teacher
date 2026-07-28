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

$workspace = Split-Path -Parent $PSScriptRoot
$publicRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace $PublicDirectory))
$gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))

if (-not (Test-Path -LiteralPath $publicRoot -PathType Container)) {
    throw "Hosting public directory does not exist: $publicRoot"
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
    $site = Invoke-FirebaseJson `
        -Method Get `
        -Uri "$apiRoot/projects/$ProjectId/sites/$SiteId" `
        -Headers $headers

    $fileHashes = [ordered]@{}
    $gzipByHash = @{}
    $files = @(Get-ChildItem -LiteralPath $publicRoot -Recurse -File | Sort-Object FullName)
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

    $csp = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self' 'wasm-unsafe-eval' https://www.gstatic.com https://www.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; script-src-attr 'none'; style-src 'self'; style-src-attr 'none'; img-src 'self' data:; font-src 'self'; connect-src 'self' https://*.googleapis.com; frame-src https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; worker-src 'self'; manifest-src 'self'; upgrade-insecure-requests"
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
                    "Cache-Control" = "no-cache"
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
