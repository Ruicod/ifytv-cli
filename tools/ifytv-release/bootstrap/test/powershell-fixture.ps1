$ErrorActionPreference = 'Stop'
$installer = Join-Path $env:IFYTV_INSTALL_HOME 'install.ps1'
function Get-ItemProperty {
    param($LiteralPath, $Name, $ErrorAction)
    if ($Name -ne 'PROCESSOR_ARCHITECTURE') { throw "Unexpected property $Name" }
    [pscustomobject]@{ PROCESSOR_ARCHITECTURE = $env:IFYTV_TEST_ARCH }
}
function Invoke-WebRequest {
    param($Uri, $OutFile, [switch]$UseBasicParsing, $ErrorAction)
    [IO.File]::WriteAllText((Join-Path $env:IFYTV_INSTALL_HOME 'url'), [string]$Uri)
    [IO.File]::Copy($env:IFYTV_TEST_ZIP, $OutFile)
}
if ($env:IFYTV_TEST_PIPE -eq 'true') {
    try {
        Get-Content -Raw -LiteralPath $installer | Invoke-Expression
    } catch {
        [Console]::Error.WriteLine($_.Exception.Message)
    }
    [IO.File]::WriteAllText((Join-Path $env:IFYTV_INSTALL_HOME 'continued'), '')
} else {
    & $installer --agent codex 'one with spaces'
}
exit $LASTEXITCODE
