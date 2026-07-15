[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('start', 'stop', 'status', 'install-shortcuts', 'uninstall-shortcuts')]
  [string]$Command = 'start',

  [string]$CpaRoot,
  [string]$CpaConfig,
  [int]$CpaPort = 8318,
  [int]$ManagerPort = 18317,
  [int]$ReadyTimeoutSeconds = 90,
  [string]$VerificationModel = 'gpt-5.5',
  [string]$ApiKey,
  [switch]$SkipCompletionCheck,
  [switch]$NoBrowser,
  [switch]$NoPause,
  [switch]$DesktopShortcut,
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$ManagerArguments
)

$ErrorActionPreference = 'Stop'

$ScriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
$Controller = Join-Path $ScriptDir 'cpa-manager-plusctl.ps1'
$RunDir = if ($env:CPA_LAUNCHER_RUN_DIR) { $env:CPA_LAUNCHER_RUN_DIR } else { Join-Path $ScriptDir 'run' }
$LogDir = if ($env:CPA_LAUNCHER_LOG_DIR) { $env:CPA_LAUNCHER_LOG_DIR } else { Join-Path $ScriptDir 'logs' }
$CpaPidFile = Join-Path $RunDir 'cli-proxy-api.pid.json'
$CpaLogFile = Join-Path $LogDir 'cli-proxy-api.log'
$CpaErrorLogFile = Join-Path $LogDir 'cli-proxy-api.err.log'
$ManagerRunDir = if ($env:CPA_MANAGER_PLUS_RUN_DIR) { $env:CPA_MANAGER_PLUS_RUN_DIR } else { Join-Path $ScriptDir 'run' }
$ManagerPidFile = if ($env:CPA_MANAGER_PLUS_PID_FILE) { $env:CPA_MANAGER_PLUS_PID_FILE } else { Join-Path $ManagerRunDir 'cpa-manager-plus.pid' }
$LauncherLockFile = Join-Path $RunDir 'cpa-launcher.lock'

function Resolve-NormalizedPath {
  param([string]$Path)

  if (-not $Path) {
    return $null
  }

  try {
    return [System.IO.Path]::GetFullPath($Path)
  } catch {
    return $Path
  }
}

function Test-ReparsePoint {
  param([string]$Path)

  if (-not (Test-Path -LiteralPath $Path)) {
    return $false
  }

  $item = Get-Item -LiteralPath $Path -Force
  return [bool]($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)
}

function Ensure-PrivateDirectory {
  param([string]$Path)

  if (Test-Path -LiteralPath $Path) {
    if (Test-ReparsePoint -Path $Path) {
      throw "Refusing to use reparse-point runtime directory: $Path"
    }
    if (-not (Get-Item -LiteralPath $Path -Force).PSIsContainer) {
      throw "Refusing to use non-directory runtime path: $Path"
    }
    return
  }

  New-Item -ItemType Directory -Path $Path -Force | Out-Null
  $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
  & icacls.exe $Path '/inheritance:r' "/grant:r" "${identity}:(OI)(CI)F" | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "Failed to secure runtime directory: $Path"
  }
}

function Enter-LauncherLock {
  Ensure-PrivateDirectory -Path $RunDir
  if (Test-Path -LiteralPath $LauncherLockFile -and (Test-ReparsePoint -Path $LauncherLockFile)) {
    throw "Refusing to use reparse-point launcher lock: $LauncherLockFile"
  }

  try {
    return [System.IO.File]::Open(
      $LauncherLockFile,
      [System.IO.FileMode]::OpenOrCreate,
      [System.IO.FileAccess]::ReadWrite,
      [System.IO.FileShare]::None
    )
  } catch [System.IO.IOException] {
    throw 'Another CPA launcher process is already starting or stopping this installation.'
  }
}

function Get-ProcessSnapshot {
  param([int]$ProcessId)

  try {
    $process = Get-Process -Id $ProcessId -ErrorAction Stop
  } catch {
    return $null
  }

  $startTimeUtc = $null
  try {
    $startTimeUtc = $process.StartTime.ToUniversalTime().ToString('o')
  } catch {
  }

  $binaryPath = $null
  try {
    $cimProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $ProcessId" -ErrorAction Stop
    if ($cimProcess.ExecutablePath) {
      $binaryPath = Resolve-NormalizedPath $cimProcess.ExecutablePath
    }
  } catch {
  }

  if (-not $binaryPath) {
    try {
      $binaryPath = Resolve-NormalizedPath $process.MainModule.FileName
    } catch {
    }
  }

  [pscustomobject]@{
    Pid          = $process.Id
    StartTimeUtc = $startTimeUtc
    BinaryPath   = $binaryPath
    Process      = $process
  }
}

function Read-JsonFile {
  param([string]$Path)

  if (-not (Test-Path -LiteralPath $Path)) {
    return $null
  }
  if (Test-ReparsePoint -Path $Path) {
    throw "Refusing to read reparse-point state file: $Path"
  }

  try {
    return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -ErrorAction Stop
  } catch {
    throw "Invalid state file: $Path"
  }
}

function Write-CpaPidRecord {
  param([int]$ProcessId)

  $snapshot = Get-ProcessSnapshot -ProcessId $ProcessId
  if (-not $snapshot -or -not $snapshot.StartTimeUtc -or -not $snapshot.BinaryPath) {
    throw "Unable to capture CLIProxyAPI process identity for PID $ProcessId."
  }

  $record = [ordered]@{
    pid          = $snapshot.Pid
    startTimeUtc = $snapshot.StartTimeUtc
    binaryPath   = $snapshot.BinaryPath
  }
  $record | ConvertTo-Json | Set-Content -LiteralPath $CpaPidFile -Encoding UTF8
}

function Get-ValidatedPidRecord {
  param([string]$Path)

  $record = Read-JsonFile -Path $Path
  if (-not $record) {
    return [pscustomobject]@{ State = 'missing' }
  }

  $processId = 0
  if (-not [int]::TryParse([string]$record.pid, [ref]$processId)) {
    return [pscustomobject]@{ State = 'invalid'; Record = $record }
  }

  $snapshot = Get-ProcessSnapshot -ProcessId $processId
  if (-not $snapshot) {
    return [pscustomobject]@{ State = 'stale'; Record = $record }
  }

  if (-not $record.startTimeUtc -or -not $snapshot.StartTimeUtc -or [string]$record.startTimeUtc -ne $snapshot.StartTimeUtc) {
    return [pscustomobject]@{ State = 'conflict'; Record = $record; Snapshot = $snapshot }
  }

  if (-not $record.binaryPath -or -not $snapshot.BinaryPath -or
      (Resolve-NormalizedPath ([string]$record.binaryPath)) -ine (Resolve-NormalizedPath $snapshot.BinaryPath)) {
    return [pscustomobject]@{ State = 'conflict'; Record = $record; Snapshot = $snapshot }
  }

  return [pscustomobject]@{ State = 'active'; Record = $record; Snapshot = $snapshot }
}

function Get-PortOwnerIds {
  param([int]$Port)

  $connections = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
  if (-not $connections) {
    return @()
  }
  return @($connections | Select-Object -ExpandProperty OwningProcess -Unique)
}

function Assert-PortOwnedBy {
  param(
    [int]$Port,
    [int]$ExpectedProcessId,
    [string]$ServiceName
  )

  $ownerIds = @(Get-PortOwnerIds -Port $Port)
  if ($ownerIds.Count -eq 0) {
    throw "$ServiceName is not listening on port $Port."
  }
  if ($ownerIds -notcontains $ExpectedProcessId) {
    throw "Port $Port is owned by PID(s) $($ownerIds -join ', '), not the validated $ServiceName process PID $ExpectedProcessId."
  }
}

function Wait-PortOwnedBy {
  param(
    [int]$Port,
    [int]$ExpectedProcessId,
    [string]$ServiceName
  )

  $deadline = [DateTime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)
  do {
    $snapshot = Get-ProcessSnapshot -ProcessId $ExpectedProcessId
    if (-not $snapshot) {
      throw "$ServiceName exited before becoming ready."
    }

    $ownerIds = @(Get-PortOwnerIds -Port $Port)
    if ($ownerIds -contains $ExpectedProcessId) {
      return
    }
    if ($ownerIds.Count -gt 0) {
      throw "Port $Port is occupied by unrelated PID(s): $($ownerIds -join ', ')."
    }

    Start-Sleep -Milliseconds 500
  } while ([DateTime]::UtcNow -lt $deadline)

  throw "Timed out waiting for $ServiceName on port $Port."
}

function Find-CpaBinary {
  $names = @('cliproxyapi.exe', 'cli-proxy-api.exe')
  $roots = New-Object System.Collections.Generic.List[string]

  foreach ($candidate in @($CpaRoot, $env:CPA_ROOT, $ScriptDir, (Split-Path -Parent $ScriptDir), (Get-Location).Path)) {
    if ($candidate -and (Test-Path -LiteralPath $candidate)) {
      $normalized = Resolve-NormalizedPath $candidate
      if (-not $roots.Contains($normalized)) {
        $roots.Add($normalized)
      }
    }
  }

  $matches = New-Object System.Collections.Generic.List[string]
  foreach ($root in $roots) {
    foreach ($name in $names) {
      $direct = Join-Path $root $name
      if (Test-Path -LiteralPath $direct -PathType Leaf) {
        $matches.Add((Resolve-NormalizedPath $direct))
      }
    }

    Get-ChildItem -LiteralPath $root -Directory -ErrorAction SilentlyContinue | ForEach-Object {
      foreach ($name in $names) {
        $nested = Join-Path $_.FullName $name
        if (Test-Path -LiteralPath $nested -PathType Leaf) {
          $matches.Add((Resolve-NormalizedPath $nested))
        }
      }
    }
  }

  $uniqueMatches = @($matches | Select-Object -Unique)
  if ($uniqueMatches.Count -eq 0) {
    throw 'CLIProxyAPI executable was not found. Set -CpaRoot or CPA_ROOT to its installation directory.'
  }
  if ($uniqueMatches.Count -gt 1) {
    throw "Multiple CLIProxyAPI executables were found. Set -CpaRoot explicitly:`n  $($uniqueMatches -join "`n  ")"
  }
  return $uniqueMatches[0]
}

function Find-CpaConfig {
  param([string]$BinaryPath)

  foreach ($candidate in @($CpaConfig, $env:CPA_CONFIG, (Join-Path (Split-Path -Parent $BinaryPath) 'config.yaml'))) {
    if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
      return Resolve-NormalizedPath $candidate
    }
  }
  throw 'CLIProxyAPI config.yaml was not found. Set -CpaConfig or CPA_CONFIG.'
}

function Quote-ProcessArgument {
  param([string]$Value)

  if ($null -eq $Value -or $Value.Length -eq 0) {
    return '""'
  }
  if ($Value -notmatch '[\s"]') {
    return $Value
  }

  $escaped = $Value -replace '(\\*)"', '$1$1\"'
  $escaped = $escaped -replace '(\\+)$', '$1$1'
  return '"' + $escaped + '"'
}

function Start-Cpa {
  $binaryPath = Find-CpaBinary
  $configPath = Find-CpaConfig -BinaryPath $binaryPath
  $state = Get-ValidatedPidRecord -Path $CpaPidFile

  if ($state.State -eq 'active') {
    if ((Resolve-NormalizedPath $binaryPath) -ine (Resolve-NormalizedPath $state.Snapshot.BinaryPath)) {
      throw "The PID record points to a different CLIProxyAPI binary: $($state.Snapshot.BinaryPath)"
    }
    Assert-PortOwnedBy -Port $CpaPort -ExpectedProcessId $state.Snapshot.Pid -ServiceName 'CLIProxyAPI'
    return [pscustomobject]@{ Pid = $state.Snapshot.Pid; ConfigPath = $configPath; Started = $false }
  }

  if ($state.State -eq 'conflict' -or $state.State -eq 'invalid') {
    throw "Refusing to overwrite a $($state.State) CLIProxyAPI PID record: $CpaPidFile"
  }
  if ($state.State -eq 'stale') {
    Remove-Item -LiteralPath $CpaPidFile -Force
  }

  $owners = @(Get-PortOwnerIds -Port $CpaPort)
  if ($owners.Count -gt 0) {
    $ownerDescriptions = foreach ($ownerId in $owners) {
      $snapshot = Get-ProcessSnapshot -ProcessId $ownerId
      if ($snapshot) { "PID $ownerId ($($snapshot.BinaryPath))" } else { "PID $ownerId" }
    }
    throw "CLIProxyAPI port $CpaPort is already occupied by $($ownerDescriptions -join ', ')."
  }

  Ensure-PrivateDirectory -Path $RunDir
  Ensure-PrivateDirectory -Path $LogDir
  foreach ($logPath in @($CpaLogFile, $CpaErrorLogFile)) {
    if (Test-Path -LiteralPath $logPath -and (Test-ReparsePoint -Path $logPath)) {
      throw "Refusing to write a reparse-point log file: $logPath"
    }
  }

  $arguments = @('--config', $configPath)
  $argumentLine = ($arguments | ForEach-Object { Quote-ProcessArgument -Value $_ }) -join ' '
  $process = Start-Process -FilePath $binaryPath -ArgumentList $argumentLine -WorkingDirectory (Split-Path -Parent $binaryPath) -RedirectStandardOutput $CpaLogFile -RedirectStandardError $CpaErrorLogFile -WindowStyle Hidden -PassThru
  try {
    Write-CpaPidRecord -ProcessId $process.Id
    Wait-PortOwnedBy -Port $CpaPort -ExpectedProcessId $process.Id -ServiceName 'CLIProxyAPI'
  } catch {
    if (-not $process.HasExited) {
      Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
      [void]$process.WaitForExit(10000)
    }
    Remove-Item -LiteralPath $CpaPidFile -Force -ErrorAction SilentlyContinue
    throw
  }
  return [pscustomobject]@{ Pid = $process.Id; ConfigPath = $configPath; Started = $true }
}

function Get-ManagerState {
  $record = Read-JsonFile -Path $ManagerPidFile
  if (-not $record) {
    return [pscustomobject]@{ State = 'missing' }
  }

  $processId = 0
  if (-not [int]::TryParse([string]$record.pid, [ref]$processId)) {
    return [pscustomobject]@{ State = 'invalid'; Record = $record }
  }
  $snapshot = Get-ProcessSnapshot -ProcessId $processId
  if (-not $snapshot) {
    return [pscustomobject]@{ State = 'stale'; Record = $record }
  }
  if (-not $record.startTimeUtc -or -not $snapshot.StartTimeUtc -or [string]$record.startTimeUtc -ne $snapshot.StartTimeUtc) {
    return [pscustomobject]@{ State = 'conflict'; Record = $record; Snapshot = $snapshot }
  }
  if ($record.binaryPath -and $snapshot.BinaryPath -and
      (Resolve-NormalizedPath ([string]$record.binaryPath)) -ieq (Resolve-NormalizedPath $snapshot.BinaryPath)) {
    return [pscustomobject]@{ State = 'active'; Record = $record; Snapshot = $snapshot }
  }
  return [pscustomobject]@{ State = 'conflict'; Record = $record; Snapshot = $snapshot }
}

function Start-Manager {
  if (-not (Test-Path -LiteralPath $Controller -PathType Leaf)) {
    throw "Manager controller was not found: $Controller"
  }

  $state = Get-ManagerState
  if ($state.State -eq 'active') {
    Assert-PortOwnedBy -Port $ManagerPort -ExpectedProcessId $state.Snapshot.Pid -ServiceName 'CPA Manager Plus'
    return [pscustomobject]@{ Pid = $state.Snapshot.Pid; Started = $false }
  }
  if ($state.State -eq 'conflict' -or $state.State -eq 'invalid') {
    throw "Refusing to start over a $($state.State) Manager PID record: $ManagerPidFile"
  }

  $owners = @(Get-PortOwnerIds -Port $ManagerPort)
  if ($owners.Count -gt 0) {
    throw "CPA Manager Plus port $ManagerPort is occupied by unrelated PID(s): $($owners -join ', ')."
  }

  $controllerArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Controller, 'start') + @($ManagerArguments)
  $argumentLine = ($controllerArgs | ForEach-Object { Quote-ProcessArgument -Value $_ }) -join ' '
  $controllerProcess = Start-Process -FilePath 'powershell.exe' -ArgumentList $argumentLine -WorkingDirectory $ScriptDir -Wait -PassThru
  if ($controllerProcess.ExitCode -ne 0) {
    throw "CPA Manager Plus controller failed with exit code $($controllerProcess.ExitCode)."
  }

  $deadline = [DateTime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)
  do {
    $state = Get-ManagerState
    if ($state.State -eq 'active') {
      Wait-PortOwnedBy -Port $ManagerPort -ExpectedProcessId $state.Snapshot.Pid -ServiceName 'CPA Manager Plus'
      return [pscustomobject]@{ Pid = $state.Snapshot.Pid; Started = $true }
    }
    if ($state.State -eq 'conflict' -or $state.State -eq 'invalid') {
      throw "Manager controller created a $($state.State) PID record."
    }
    Start-Sleep -Milliseconds 500
  } while ([DateTime]::UtcNow -lt $deadline)

  throw 'Timed out waiting for the CPA Manager Plus PID record.'
}

function Get-ApiKeyFromConfig {
  param([string]$ConfigPath)

  if ($ApiKey) {
    return $ApiKey
  }
  if ($env:CPA_LAUNCHER_API_KEY) {
    return $env:CPA_LAUNCHER_API_KEY
  }

  $insideApiKeys = $false
  foreach ($line in Get-Content -LiteralPath $ConfigPath) {
    if ($line -match '^api-keys:\s*(?:#.*)?$') {
      $insideApiKeys = $true
      continue
    }
    if ($insideApiKeys -and $line -match '^\S') {
      break
    }
    if ($insideApiKeys -and $line -match '^\s*-\s*["'']?([^#"'']+?)["'']?\s*(?:#.*)?$') {
      $candidate = $Matches[1].Trim()
      if ($candidate) {
        return $candidate
      }
    }
  }

  throw 'No CLIProxyAPI API key was found. Set -ApiKey or CPA_LAUNCHER_API_KEY, or use -SkipCompletionCheck.'
}

function Test-CpaCompletion {
  param([string]$ConfigPath)

  if ($SkipCompletionCheck) {
    return
  }

  $secret = Get-ApiKeyFromConfig -ConfigPath $ConfigPath
  $headers = @{ Authorization = "Bearer $secret" }
  $body = @{
    model = $VerificationModel
    messages = @(@{ role = 'user'; content = 'hi' })
    stream = $false
  } | ConvertTo-Json -Depth 6

  try {
    $response = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$CpaPort/v1/chat/completions" -Headers $headers -ContentType 'application/json' -Body $body -TimeoutSec $ReadyTimeoutSeconds
  } finally {
    $secret = $null
    $headers = $null
  }

  if (-not $response -or -not $response.choices -or -not $response.choices[0].message) {
    throw "CLIProxyAPI completion verification for model $VerificationModel returned an invalid response."
  }
}

function Wait-ManagerReadiness {
  $deadline = [DateTime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)
  $lastFailure = 'no response'
  do {
    try {
      $response = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$ManagerPort/health/ready" -TimeoutSec 3
      if ($response -and $response.ok) {
        return $response
      }
      $lastFailure = 'the readiness endpoint did not report ok'
    } catch {
      $lastFailure = $_.Exception.Message
    }
    Start-Sleep -Milliseconds 500
  } while ([DateTime]::UtcNow -lt $deadline)

  throw "CPA Manager Plus did not pass its collector/database readiness check: $lastFailure"
}

function Wait-ManagerIngestion {
  param([pscustomobject]$Baseline)

  $deadline = [DateTime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)
  $lastFailure = 'the event counters did not advance'
  do {
    try {
      $status = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$ManagerPort/health/ready" -TimeoutSec 3
      if ([long]$status.events -gt [long]$Baseline.events -and
          [long]$status.totalInserted -gt [long]$Baseline.totalInserted) {
        return
      }
      if ($status.lastErrorPresent) {
        $lastFailure = 'the collector reported an error; inspect authenticated status or logs'
      }
    } catch {
      $lastFailure = $_.Exception.Message
    }
    Start-Sleep -Milliseconds 500
  } while ([DateTime]::UtcNow -lt $deadline)

  throw "CPA Manager Plus did not persist the verification request: $lastFailure"
}

function Stop-ValidatedProcess {
  param(
    [pscustomobject]$State,
    [string]$Name,
    [string]$PidPath
  )

  if ($State.State -eq 'missing' -or $State.State -eq 'stale') {
    if (Test-Path -LiteralPath $PidPath) {
      Remove-Item -LiteralPath $PidPath -Force
    }
    return
  }
  if ($State.State -ne 'active') {
    throw "Refusing to stop $Name with PID state $($State.State)."
  }

  Stop-Process -Id $State.Snapshot.Pid -ErrorAction Stop
  if (-not $State.Snapshot.Process.WaitForExit(10000)) {
    throw "$Name did not stop within 10 seconds."
  }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Stop-Manager {
  if (Test-Path -LiteralPath $Controller -PathType Leaf) {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Controller stop
    if ($LASTEXITCODE -ne 0) {
      throw "CPA Manager Plus controller failed with exit code $LASTEXITCODE."
    }
  }
}

function Stop-All {
  Stop-Manager
  Stop-ValidatedProcess -State (Get-ValidatedPidRecord -Path $CpaPidFile) -Name 'CLIProxyAPI' -PidPath $CpaPidFile
}

function Write-Status {
  $cpaState = Get-ValidatedPidRecord -Path $CpaPidFile
  $managerState = Get-ManagerState
  Write-Host "CLIProxyAPI:      $($cpaState.State)"
  if ($cpaState.State -eq 'active') {
    Write-Host "  PID:            $($cpaState.Snapshot.Pid)"
    Write-Host "  Binary:         $($cpaState.Snapshot.BinaryPath)"
    Write-Host "  Port:           $CpaPort"
  }
  Write-Host "CPA Manager Plus: $($managerState.State)"
  if ($managerState.State -eq 'active') {
    Write-Host "  PID:            $($managerState.Snapshot.Pid)"
    Write-Host "  Binary:         $($managerState.Snapshot.BinaryPath)"
    Write-Host "  Port:           $ManagerPort"
  }
}

function Get-ShortcutPaths {
  $startMenu = Join-Path ([Environment]::GetFolderPath('Programs')) 'CPA.lnk'
  $desktop = Join-Path ([Environment]::GetFolderPath('Desktop')) 'CPA.lnk'
  return [pscustomobject]@{ StartMenu = $startMenu; Desktop = $desktop }
}

function Install-Shortcut {
  param(
    [string]$Path,
    [string]$Arguments
  )

  $shell = New-Object -ComObject WScript.Shell
  $shortcut = $shell.CreateShortcut($Path)
  $shortcut.TargetPath = (Join-Path $PSHOME 'powershell.exe')
  $shortcut.Arguments = $Arguments
  $shortcut.WorkingDirectory = $ScriptDir
  $shortcut.Description = 'Start CLIProxyAPI and CPA Manager Plus'
  $managerBinary = Join-Path $ScriptDir 'cpa-manager-plus.exe'
  if (Test-Path -LiteralPath $managerBinary -PathType Leaf) {
    $shortcut.IconLocation = "$managerBinary,0"
  }
  $shortcut.Save()
}

function Install-Shortcuts {
  $paths = Get-ShortcutPaths
  $arguments = "-NoProfile -ExecutionPolicy Bypass -File $(Quote-ProcessArgument -Value $PSCommandPath) start -NoPause"
  Install-Shortcut -Path $paths.StartMenu -Arguments $arguments
  Write-Host "Installed Start Menu shortcut: $($paths.StartMenu)"
  if ($DesktopShortcut) {
    Install-Shortcut -Path $paths.Desktop -Arguments $arguments
    Write-Host "Installed desktop shortcut: $($paths.Desktop)"
  }
}

function Uninstall-Shortcuts {
  $paths = Get-ShortcutPaths
  foreach ($path in @($paths.StartMenu, $paths.Desktop)) {
    if (Test-Path -LiteralPath $path) {
      Remove-Item -LiteralPath $path -Force
      Write-Host "Removed shortcut: $path"
    }
  }
}

function Invoke-Start {
  $cpa = $null
  $manager = $null
  $managerWasActive = $false
  try {
    $cpa = Start-Cpa
    $managerWasActive = (Get-ManagerState).State -eq 'active'
    $manager = Start-Manager
    $baseline = Wait-ManagerReadiness
    Test-CpaCompletion -ConfigPath $cpa.ConfigPath
    if (-not $SkipCompletionCheck) {
      Wait-ManagerIngestion -Baseline $baseline
    }
  } catch {
    $startError = $_
    $stopManager = $manager -and $manager.Started
    if (-not $stopManager -and -not $managerWasActive) {
      $stopManager = (Get-ManagerState).State -eq 'active'
    }
    if ($stopManager) {
      try {
        Stop-Manager
      } catch {
        Write-Warning "Failed to roll back CPA Manager Plus: $($_.Exception.Message)"
      }
    }
    if ($cpa -and $cpa.Started) {
      try {
        Stop-ValidatedProcess -State (Get-ValidatedPidRecord -Path $CpaPidFile) -Name 'CLIProxyAPI' -PidPath $CpaPidFile
      } catch {
        Write-Warning "Failed to roll back CLIProxyAPI: $($_.Exception.Message)"
      }
    }
    throw $startError
  }

  $panelUri = "http://127.0.0.1:$ManagerPort"
  Write-Host 'CPA services are ready.'
  Write-Host "  Core:  http://127.0.0.1:$CpaPort"
  Write-Host "  Panel: $panelUri"
  if (-not $NoBrowser) {
    Start-Process $panelUri
  }
}

$exitCode = 0
$launcherLock = $null
try {
  if ($Command -eq 'start' -or $Command -eq 'stop') {
    $launcherLock = Enter-LauncherLock
  }
  switch ($Command) {
    'start' { Invoke-Start }
    'stop' { Stop-All }
    'status' { Write-Status }
    'install-shortcuts' { Install-Shortcuts }
    'uninstall-shortcuts' { Uninstall-Shortcuts }
  }
} catch {
  $exitCode = 1
  Write-Error $_
} finally {
  if ($launcherLock) {
    $launcherLock.Dispose()
  }
  if (-not $NoPause -and [Environment]::UserInteractive) {
    Write-Host 'Press any key to close this window...'
    [void][Console]::ReadKey($true)
  }
}

exit $exitCode
