$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$policyPath = Join-Path $PSScriptRoot "release-provenance.ps1"
. $policyPath

$expectedCommit = "df6aa9561595f6ecaf57e666a0e8c3e4c745cea2"
$expectedRevision = "main"
$latestReadyRevisionName = "kotae-api-reviewed-release"

function Invoke-ProvenanceAssertion {
    param(
        [Parameter(Mandatory)]
        [AllowNull()]
        [object] $Labels,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    Assert-SourceProvenanceLabels `
        -ExpectedGitCommit $expectedCommit `
        -ExpectedSourceRevision $expectedRevision `
        -Labels $Labels `
        -Boundary $Boundary
}

function Assert-ProvenanceRejected {
    param(
        [Parameter(Mandatory)]
        [string] $Case,

        [Parameter(Mandatory)]
        [AllowNull()]
        [object] $Labels
    )

    $rejected = $false
    try {
        Invoke-ProvenanceAssertion -Labels $Labels -Boundary $Case
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Invalid provenance fixture was accepted: $Case"
    }
}

function Invoke-TrafficAssertion {
    param(
        [Parameter(Mandatory)]
        [AllowNull()]
        [AllowEmptyCollection()]
        [object[]] $Traffic,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    Get-SolePromotedRevisionName `
        -Traffic $Traffic `
        -LatestReadyRevisionName $latestReadyRevisionName `
        -Boundary $Boundary
}

function Assert-TrafficRejected {
    param(
        [Parameter(Mandatory)]
        [string] $Case,

        [Parameter(Mandatory)]
        [AllowNull()]
        [AllowEmptyCollection()]
        [object[]] $Traffic
    )

    $rejected = $false
    try {
        Invoke-TrafficAssertion -Traffic $Traffic -Boundary $Case
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Invalid traffic fixture was accepted: $Case"
    }
}

$validOutput = @(
    Invoke-ProvenanceAssertion `
        -Boundary "valid" `
        -Labels ([pscustomobject] @{
            application = "kotae-ai"
            "source-commit" = $expectedCommit
            "source-revision" = $expectedRevision
        })
)
if ($validOutput.Count -ne 0) {
    throw "Valid provenance assertion must not emit output."
}

Assert-ProvenanceRejected -Case "null" -Labels $null
Assert-ProvenanceRejected `
    -Case "missing" `
    -Labels ([pscustomobject] @{ application = "kotae-ai" })
Assert-ProvenanceRejected `
    -Case "12-character commit" `
    -Labels ([pscustomobject] @{
        "source-commit" = $expectedCommit.Substring(0, 12)
        "source-revision" = $expectedRevision
    })
Assert-ProvenanceRejected `
    -Case "stale 40-character commit" `
    -Labels ([pscustomobject] @{
        "source-commit" = "8fba4487d3031b2b6ee7e98cf1cbf493ae0b7e49"
        "source-revision" = $expectedRevision
    })
Assert-ProvenanceRejected `
    -Case "wrong source revision" `
    -Labels ([pscustomobject] @{
        "source-commit" = $expectedCommit
        "source-revision" = "feature"
    })
Assert-ProvenanceRejected `
    -Case "unknown source label" `
    -Labels ([pscustomobject] @{
        "source-commit" = $expectedCommit
        "source-revision" = $expectedRevision
        "source-build" = "unreviewed"
    })

$validTrafficOutput = @(
    Invoke-TrafficAssertion `
        -Boundary "valid traffic" `
        -Traffic @(
            [pscustomobject] @{
                percent = 100
                revisionName = $latestReadyRevisionName
            }
        )
)
if (
    $validTrafficOutput.Count -ne 1 -or
    [string] $validTrafficOutput[0] -cne $latestReadyRevisionName
) {
    throw "Valid traffic assertion did not return the promoted revision."
}

Assert-TrafficRejected -Case "null traffic" -Traffic $null
Assert-TrafficRejected -Case "empty traffic" -Traffic @()
Assert-TrafficRejected `
    -Case "multiple traffic targets" `
    -Traffic @(
        [pscustomobject] @{
            percent = 100
            revisionName = $latestReadyRevisionName
        },
        [pscustomobject] @{
            percent = 1
            revisionName = "kotae-api-unexpected-extra-release"
        }
    )
Assert-TrafficRejected `
    -Case "tagged traffic" `
    -Traffic @(
        [pscustomobject] @{
            percent = 100
            revisionName = $latestReadyRevisionName
            tag = "candidate"
        }
    )
Assert-TrafficRejected `
    -Case "latest ready mismatch" `
    -Traffic @(
        [pscustomobject] @{
            percent = 100
            revisionName = "kotae-api-previous-release"
        }
    )
Assert-TrafficRejected `
    -Case "percent is not 100" `
    -Traffic @(
        [pscustomobject] @{
            percent = 99
            revisionName = $latestReadyRevisionName
        }
    )
Assert-TrafficRejected `
    -Case "revision name missing" `
    -Traffic @([pscustomobject] @{ percent = 100 })
Assert-TrafficRejected `
    -Case "revision name empty" `
    -Traffic @(
        [pscustomobject] @{
            percent = 100
            revisionName = ""
        }
    )

Write-Output "RELEASE_PROVENANCE_FIXTURES=PASS"
Write-Output "RELEASE_TRAFFIC_FIXTURES=PASS"
