param(
    [string]$CsvPath = "$env:USERPROFILE\Downloads\messages.csv",
    [string[]]$Recipient = @(),
    [string]$ExpectedRecipientsPath = "",
    [string]$OutDir = "ses-audit-output"
)

$ErrorActionPreference = "Stop"

function Normalize-Email {
    param([string]$Value)
    if ($null -eq $Value) {
        return ""
    }
    return $Value.Trim().ToLowerInvariant()
}

function Is-Truthy {
    param([object]$Value)
    if ($null -eq $Value) {
        return $false
    }
    $text = ($Value -as [string]).Trim().ToLowerInvariant()
    return $text -in @("true", "1", "yes", "y")
}

function To-Text {
    param([object]$Value)
    if ($null -eq $Value) {
        return ""
    }
    return (($Value -as [string]).Trim())
}

function Classify-Bounce {
    param($Row)

    $event = (To-Text $Row.last_delivery_event).ToUpperInvariant()
    $subType = To-Text $Row.bounce_sub_type
    $diagnostic = To-Text $Row.diagnostic_code
    $combined = ($subType + " " + $diagnostic).ToLowerInvariant()

    if (-not (Is-Truthy $Row.bounced) -and $event -notmatch "BOUNCE|REJECT|FAIL") {
        return ""
    }

    if ($combined -match "\b4\d\d\b|temporar|transient|mailboxfull|mailbox full|try again|greylist|throttl|rate") {
        return "soft"
    }

    if ($combined -match "\b5\d\d\b|noemail|suppressed|permanent|user unknown|does not exist|invalid|contentrejected|attachmentrejected") {
        return "hard"
    }

    return "unknown"
}

function Get-ExpectedRecipients {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return @()
    }
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Expected recipients file not found: $Path"
    }

    $firstLine = Get-Content -LiteralPath $Path -TotalCount 1
    if ($firstLine -match ",") {
        $rows = Import-Csv -LiteralPath $Path
        $candidateColumns = @("email", "recipient", "destination", "captain_email", "captainemail")
        $out = foreach ($row in $rows) {
            foreach ($col in $candidateColumns) {
                if ($row.PSObject.Properties.Name -contains $col -and $row.$col) {
                    Normalize-Email $row.$col
                    break
                }
            }
        }
        return @($out | Where-Object { $_ -match "@" } | Sort-Object -Unique)
    }

    return @(Get-Content -LiteralPath $Path | ForEach-Object { Normalize-Email $_ } | Where-Object { $_ -match "@" } | Sort-Object -Unique)
}

if (-not (Test-Path -LiteralPath $CsvPath)) {
    throw "SES messages CSV not found: $CsvPath"
}

$rows = @(Import-Csv -LiteralPath $CsvPath)
if ($rows.Count -eq 0) {
    throw "SES messages CSV contains no rows: $CsvPath"
}

$requiredColumns = @("messageid", "sendtimestamp", "destination", "subject", "last_delivery_event", "bounce_sub_type", "diagnostic_code", "delivered", "bounced", "complained")
$missingColumns = @($requiredColumns | Where-Object { $rows[0].PSObject.Properties.Name -notcontains $_ })
if ($missingColumns.Count -gt 0) {
    throw "CSV is missing expected SES column(s): $($missingColumns -join ', ')"
}

$resolvedOutDir = if ([System.IO.Path]::IsPathRooted($OutDir)) { $OutDir } else { Join-Path (Get-Location) $OutDir }
New-Item -ItemType Directory -Force -Path $resolvedOutDir | Out-Null

$enriched = foreach ($row in $rows) {
    $deliveryEvent = (To-Text $row.last_delivery_event).ToUpperInvariant()
    $bounceClass = Classify-Bounce $row
    [pscustomobject]@{
        sendtimestamp = $row.sendtimestamp
        destination = Normalize-Email $row.destination
        subject = $row.subject
        last_delivery_event = $row.last_delivery_event
        last_engagement_event = $row.last_engagement_event
        delivered = Is-Truthy $row.delivered
        bounced = Is-Truthy $row.bounced
        complained = Is-Truthy $row.complained
        opened = Is-Truthy $row.opened
        clicked = Is-Truthy $row.clicked
        bounce_class = $bounceClass
        bounce_sub_type = $row.bounce_sub_type
        diagnostic_code = $row.diagnostic_code
        messageid = $row.messageid
        needs_attention = (
            (Is-Truthy $row.bounced) -or
            (Is-Truthy $row.complained) -or
            $deliveryEvent -notin @("", "DELIVERY")
        )
    }
}

$problems = @($enriched | Where-Object { $_.needs_attention })
$softBounces = @($enriched | Where-Object { $_.bounce_class -eq "soft" })
$hardBounces = @($enriched | Where-Object { $_.bounce_class -eq "hard" })
$unknownBounces = @($enriched | Where-Object { $_.bounce_class -eq "unknown" })
$complaints = @($enriched | Where-Object { $_.complained })
$undelivered = @($enriched | Where-Object { -not $_.delivered -or $_.last_delivery_event -ne "DELIVERY" })

$enriched | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "all-messages-normalized.csv")
$problems | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "needs-attention.csv")
$softBounces | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "soft-bounces.csv")
$hardBounces | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "hard-bounces.csv")
$unknownBounces | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "unknown-bounces.csv")
$complaints | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "complaints.csv")
$undelivered | Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "not-delivered.csv")

$expectedRecipients = Get-ExpectedRecipients $ExpectedRecipientsPath
if ($expectedRecipients.Count -gt 0) {
    $sentRecipients = @($enriched | ForEach-Object { $_.destination } | Sort-Object -Unique)
    $missingExpected = @($expectedRecipients | Where-Object { $sentRecipients -notcontains $_ })
    $missingExpected | ForEach-Object { [pscustomobject]@{ recipient = $_ } } |
        Export-Csv -NoTypeInformation -Path (Join-Path $resolvedOutDir "missing-expected-recipients.csv")
}

Write-Host ""
Write-Host "SES message audit"
Write-Host "CSV: $CsvPath"
Write-Host "Output: $resolvedOutDir"
Write-Host ""
Write-Host ("Total messages:      {0}" -f $enriched.Count)
Write-Host ("Delivered:           {0}" -f @($enriched | Where-Object { $_.delivered }).Count)
Write-Host ("Needs attention:     {0}" -f $problems.Count)
Write-Host ("Soft bounces:        {0}" -f $softBounces.Count)
Write-Host ("Hard bounces:        {0}" -f $hardBounces.Count)
Write-Host ("Unknown bounces:     {0}" -f $unknownBounces.Count)
Write-Host ("Complaints:          {0}" -f $complaints.Count)
Write-Host ("Opened:              {0}" -f @($enriched | Where-Object { $_.opened }).Count)
Write-Host ("Clicked:             {0}" -f @($enriched | Where-Object { $_.clicked }).Count)

if ($expectedRecipients.Count -gt 0) {
    Write-Host ("Expected recipients: {0}" -f $expectedRecipients.Count)
    Write-Host ("Missing expected:    {0}" -f $missingExpected.Count)
}

if ($problems.Count -gt 0) {
    Write-Host ""
    Write-Host "Problem rows:"
    $problems |
        Select-Object sendtimestamp,destination,subject,last_delivery_event,bounce_class,bounce_sub_type,diagnostic_code |
        Format-Table -Wrap -AutoSize
}

if ($Recipient.Count -gt 0) {
    Write-Host ""
    $recipientSearches = @($Recipient | ForEach-Object { ($_ -split ",") } | ForEach-Object { Normalize-Email $_ } | Where-Object { $_ -ne "" } | Sort-Object -Unique)
    foreach ($needle in $recipientSearches) {
        $normalizedNeedle = Normalize-Email $needle
        $matches = @($enriched | Where-Object { $_.destination -eq $normalizedNeedle })
        Write-Host ("Recipient search: {0} ({1} message(s))" -f $normalizedNeedle, $matches.Count)
        if ($matches.Count -gt 0) {
            $matches |
                Select-Object sendtimestamp,destination,subject,last_delivery_event,last_engagement_event,bounce_class,diagnostic_code |
                Format-Table -Wrap -AutoSize
        }
    }
}

Write-Host ""
Write-Host "Files written:"
Get-ChildItem -LiteralPath $resolvedOutDir -Filter "*.csv" |
    Sort-Object Name |
    ForEach-Object { Write-Host ("- {0}" -f $_.FullName) }
