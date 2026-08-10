function Assert-SourceProvenanceLabels {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $ExpectedGitCommit,

        [Parameter(Mandatory)]
        [string] $ExpectedSourceRevision,

        [Parameter(Mandatory)]
        [AllowNull()]
        [object] $Labels,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    if (
        $ExpectedGitCommit -cnotmatch '^[0-9a-f]{40}$' -or
        [string]::IsNullOrWhiteSpace($ExpectedSourceRevision) -or
        [string]::IsNullOrWhiteSpace($Boundary)
    ) {
        throw "Source provenance expectations are invalid."
    }
    if ($null -eq $Labels) {
        throw "$Boundary is missing source provenance labels."
    }

    $sourceCommitProperty = $Labels.PSObject.Properties["source-commit"]
    $sourceRevisionProperty = $Labels.PSObject.Properties["source-revision"]
    $sourceCommit = if ($null -eq $sourceCommitProperty) {
        ""
    } else {
        [string] $sourceCommitProperty.Value
    }
    $sourceRevision = if ($null -eq $sourceRevisionProperty) {
        ""
    } else {
        [string] $sourceRevisionProperty.Value
    }
    $unknownSourceLabels = @(
        $Labels.PSObject.Properties.Name | Where-Object {
            $_ -like "source-*" -and
            $_ -cne "source-commit" -and
            $_ -cne "source-revision"
        }
    )
    if (
        $unknownSourceLabels.Count -ne 0 -or
        $sourceCommit -cnotmatch '^[0-9a-f]{40}$' -or
        $sourceCommit -cne $ExpectedGitCommit -or
        $sourceRevision -cne $ExpectedSourceRevision
    ) {
        throw "$Boundary source provenance does not match the reviewed main commit."
    }
}

function Get-SolePromotedRevisionName {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [AllowNull()]
        [AllowEmptyCollection()]
        [object[]] $Traffic,

        [Parameter(Mandatory)]
        [AllowEmptyString()]
        [string] $LatestReadyRevisionName,

        [Parameter(Mandatory)]
        [string] $Boundary
    )

    if (
        [string]::IsNullOrWhiteSpace($LatestReadyRevisionName) -or
        [string]::IsNullOrWhiteSpace($Boundary)
    ) {
        throw "Promoted traffic expectations are invalid."
    }
    if ($null -eq $Traffic -or $Traffic.Count -ne 1 -or $null -eq $Traffic[0]) {
        throw "$Boundary must contain exactly one promoted revision."
    }

    $entry = $Traffic[0]
    $percentProperty = $entry.PSObject.Properties["percent"]
    $revisionNameProperty = $entry.PSObject.Properties["revisionName"]
    $tagProperty = $entry.PSObject.Properties["tag"]
    $percent = if ($null -eq $percentProperty) {
        ""
    } else {
        [string] $percentProperty.Value
    }
    $revisionName = if ($null -eq $revisionNameProperty) {
        ""
    } else {
        [string] $revisionNameProperty.Value
    }
    $tag = if ($null -eq $tagProperty) {
        ""
    } else {
        [string] $tagProperty.Value
    }
    if (
        $percent -cne "100" -or
        [string]::IsNullOrWhiteSpace($revisionName) -or
        $revisionName -cne $LatestReadyRevisionName -or
        -not [string]::IsNullOrWhiteSpace($tag)
    ) {
        throw "$Boundary is not the sole tagless 100 percent latest-ready revision."
    }

    return $revisionName
}
