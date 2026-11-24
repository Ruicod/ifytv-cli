# The download is parsed as one block before any installation work starts.
& {
    param([bool]$InvokedAsFile, [string[]]$InstallArguments)
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    $PSNativeCommandUseErrorActionPreference = $false
    $global:LASTEXITCODE = 1
    $version = '@@VERSION@@'
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'ifytv: unsupported operating system for the Windows installer'
    }
    # The machine environment describes native Windows even in an emulated shell.
    try {
        $key = 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment'
        $machine = (Get-ItemProperty -LiteralPath $key -Name PROCESSOR_ARCHITECTURE).PROCESSOR_ARCHITECTURE
    } catch {
        $machine = $env:PROCESSOR_ARCHITEW6432
        if (-not $machine) { $machine = $env:PROCESSOR_ARCHITECTURE }
    }
    switch ($machine) {
        'AMD64' { $arch = 'amd64'; $expected = '@@SHA256_WINDOWS_AMD64@@' }
        'ARM64' { $arch = 'arm64'; $expected = '@@SHA256_WINDOWS_ARM64@@' }
        default { throw "ifytv: unsupported Windows architecture '$machine'" }
    }

    $url = "https://github.com/Ruicod/ifytv-cli/releases/download/v$version/ifytv-$version-windows-$arch.zip"
    $stage = Join-Path ([IO.Path]::GetTempPath()) ('ifytv-install-' + [guid]::NewGuid().ToString('N'))
    $archive = Join-Path $stage 'ifytv.zip'
    $program = Join-Path $stage 'ifytv.exe'
    New-Item -ItemType Directory -Path $stage | Out-Null
    try {
        $legacy = $PSVersionTable.PSVersion.Major -eq 5
        if ($legacy) {
            $savedTls = [Net.ServicePointManager]::SecurityProtocol
            [Net.ServicePointManager]::SecurityProtocol = $savedTls -bor [Net.SecurityProtocolType]::Tls12
        }
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $archive
        } finally {
            if ($legacy) { [Net.ServicePointManager]::SecurityProtocol = $savedTls }
        }
        $sha = [Security.Cryptography.SHA256]::Create()
        $stream = $null
        try {
            $stream = [IO.File]::OpenRead($archive)
            $digest = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '')
        } finally {
            if ($null -ne $stream) { $stream.Dispose() }
            $sha.Dispose()
        }
        if ($digest -ne $expected) {
            throw 'ifytv: SHA-256 mismatch for the downloaded release'
        }

        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $zip = [IO.Compression.ZipFile]::OpenRead($archive)
        try {
            $entry = $zip.GetEntry('ifytv.exe')
            if ($null -eq $entry) { throw 'ifytv: release archive is missing ifytv.exe' }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $program)
        } finally {
            $zip.Dispose()
        }
        & $program install @InstallArguments
        $code = $LASTEXITCODE
    } finally {
        [IO.File]::Delete($program)
        [IO.File]::Delete($archive)
        [IO.Directory]::Delete($stage)
    }
    if ($code -ne 0) {
        if ($InvokedAsFile) { exit $code }
        throw "ifytv install exited with code $code"
    }
} ([bool]($PSCommandPath -and $MyInvocation.InvocationName -ne '.')) $args
