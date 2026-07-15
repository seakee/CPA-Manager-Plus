[CmdletBinding()]
param(
  [string]$CPAPath,
  [string]$ManagerPath,
  [string]$CPAConfigPath,
  [int]$CPAPort = 8318,
  [int]$ManagerPort = 18317,
  [int]$TimeoutSeconds = 90,
  [switch]$NoBrowser,
  [switch]$NoPause,
  [switch]$Configure,
  [switch]$InstallShortcuts,
  [switch]$UninstallShortcuts,
  [switch]$DesktopShortcut,
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$ManagerArguments
)

$ErrorActionPreference = 'Stop'

$AppName = 'CPA Launcher'
$ScriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
$StateRoot = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) $AppName
$RunDir = Join-Path $StateRoot 'run'
$LogDir = Join-Path $StateRoot 'logs'
$SecretFile = Join-Path $StateRoot 'secrets.json'
$LockFile = Join-Path $RunDir 'launcher.lock'
$CPAPidFile = Join-Path $RunDir 'cpa-core.pid.json'
$CPALogFile = Join-Path $LogDir 'cpa-core.log'
$CPAErrLogFile = Join-Path $LogDir 'cpa-core.err.log'
$ManagerRunDir = Join-Path $RunDir 'manager'
$ManagerLogDir = Join-Path $LogDir 'manager'
$PowerShellPath = Join-Path $PSHOME 'powershell.exe'

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

function Set-PrivateAcl {
  param(
    [string]$Path,
    [switch]$Directory
  )

  if (-not (Test-Path -LiteralPath $Path)) {
    return
  }

  $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
  $acl = if ($Directory) {
    New-Object System.Security.AccessControl.DirectorySecurity
  } else {
    New-Object System.Security.AccessControl.FileSecurity
  }
  $inheritance = if ($Directory) {
    [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
  } else {
    [System.Security.AccessControl.InheritanceFlags]::None
  }
  $acl.SetOwner($identity)
  $acl.SetAccessRuleProtection($true, $false)
  $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
    $identity,
    [System.Security.AccessControl.FileSystemRights]::FullControl,
    $inheritance,
    [System.Security.AccessControl.PropagationFlags]::None,
    [System.Security.AccessControl.AccessControlType]::Allow
  )
  [void]$acl.AddAccessRule($rule)
  if ($Directory) {
    [System.IO.Directory]::SetAccessControl($Path, $acl)
  } else {
    [System.IO.File]::SetAccessControl($Path, $acl)
  }
}

function Ensure-PrivateDirectory {
  param([string]$Path)

  if (Test-Path -LiteralPath $Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
      throw "Refusing unsafe launcher directory: $Path"
    }
  } else {
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
  }
  Set-PrivateAcl -Path $Path -Directory
}

function Get-PlainText {
  param([Security.SecureString]$Value)

  if (-not $Value) {
    return ''
  }
  $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
  try {
    return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
  } finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
  }
}

function Save-LauncherSecrets {
  Ensure-PrivateDirectory -Path $StateRoot
  $cpaSecret = Read-Host 'CPA API key used for the authenticated readiness check' -AsSecureString
  $managerSecret = Read-Host 'CPA Manager Plus admin or external management key' -AsSecureString
  $record = [ordered]@{
    cpaApiKey = ConvertFrom-SecureString $cpaSecret
    managerKey = ConvertFrom-SecureString $managerSecret
  }
  $record | ConvertTo-Json | Set-Content -LiteralPath $SecretFile -Encoding UTF8
  Set-PrivateAcl -Path $SecretFile
  Write-Host "Encrypted launcher credentials saved for the current Windows user."
}

function Read-LauncherSecrets {
  $result = [ordered]@{
    CPAApiKey = [string]$env:CPA_API_KEY
    ManagerKey = [string]$env:CPA_MANAGER_PLUS_ADMIN_KEY
  }
  if (-not (Test-Path -LiteralPath $SecretFile)) {
    return [pscustomobject]$result
  }
  try {
    $saved = Get-Content -LiteralPath $SecretFile -Raw | ConvertFrom-Json
    if (-not $result.CPAApiKey -and $saved.cpaApiKey) {
      $result.CPAApiKey = Get-PlainText (ConvertTo-SecureString ([string]$saved.cpaApiKey))
    }
    if (-not $result.ManagerKey -and $saved.managerKey) {
      $result.ManagerKey = Get-PlainText (ConvertTo-SecureString ([string]$saved.managerKey))
    }
  } catch {
    throw "Unable to decrypt $SecretFile for the current Windows user: $($_.Exception.Message)"
  }
  return [pscustomobject]$result
}

function Find-Executable {
  param(
    [string]$ExplicitPath,
    [string]$EnvironmentPath,
    [string[]]$Names,
    [string[]]$Roots
  )

  foreach ($candidate in @($ExplicitPath, $EnvironmentPath)) {
    if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
      return Resolve-NormalizedPath $candidate
    }
  }

  foreach ($root in $Roots) {
    if (-not $root -or -not (Test-Path -LiteralPath $root -PathType Container)) {
      continue
    }
    foreach ($name in $Names) {
      $candidate = Join-Path $root $name
      if (Test-Path -LiteralPath $candidate -PathType Leaf) {
        return Resolve-NormalizedPath $candidate
      }
    }
    foreach ($child in Get-ChildItem -LiteralPath $root -Directory -Force -ErrorAction SilentlyContinue) {
      if ($child.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        continue
      }
      foreach ($name in $Names) {
        $candidate = Join-Path $child.FullName $name
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
          return Resolve-NormalizedPath $candidate
        }
      }
    }
  }

  foreach ($name in $Names) {
    $command = Get-Command $name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command -and $command.Source) {
      return Resolve-NormalizedPath $command.Source
    }
  }
  return $null
}

function Get-ProcessPath {
  param([int]$ProcessId)

  try {
    $cim = Get-CimInstance Win32_Process -Filter "ProcessId = $ProcessId" -ErrorAction Stop
    if ($cim.ExecutablePath) {
      return Resolve-NormalizedPath $cim.ExecutablePath
    }
  } catch {
  }
  try {
    return Resolve-NormalizedPath (Get-Process -Id $ProcessId -ErrorAction Stop).MainModule.FileName
  } catch {
    return $null
  }
}

function Get-PortOwnerPids {
  param([int]$Port)

  return @(
    Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue |
      Select-Object -ExpandProperty OwningProcess -Unique
  )
}

function Get-VerifiedCPAPid {
  param([string]$ExpectedPath)

  if (-not (Test-Path -LiteralPath $CPAPidFile -PathType Leaf)) {
    return 0
  }
  try {
    $record = Get-Content -LiteralPath $CPAPidFile -Raw | ConvertFrom-Json
    $process = Get-Process -Id ([int]$record.pid) -ErrorAction Stop
    $startTime = $process.StartTime.ToUniversalTime().ToString('o')
    $actualPath = Get-ProcessPath -ProcessId $process.Id
    if ($startTime -ne [string]$record.startTimeUtc -or $actualPath -ine (Resolve-NormalizedPath $ExpectedPath)) {
      throw "CPA PID record conflicts with PID $($process.Id)."
    }
    return [int]$process.Id
  } catch [Microsoft.PowerShell.Commands.ProcessCommandException] {
    Remove-Item -LiteralPath $CPAPidFile -Force -ErrorAction SilentlyContinue
    return 0
  }
}

function Assert-PortOwner {
  param(
    [int]$Port,
    [string]$ExpectedPath,
    [string]$ServiceName
  )

  $owners = @(Get-PortOwnerPids -Port $Port)
  if ($owners.Count -eq 0) {
    return 0
  }
  foreach ($owner in $owners) {
    $actualPath = Get-ProcessPath -ProcessId $owner
    if (-not $actualPath -or $actualPath -ine (Resolve-NormalizedPath $ExpectedPath)) {
      throw "$ServiceName port $Port is owned by PID $owner at '$actualPath', not '$ExpectedPath'."
    }
  }
  return [int]$owners[0]
}

function Wait-HttpReady {
  param(
    [string]$URL,
    [int]$Timeout,
    [hashtable]$Headers
  )

  $deadline = [DateTime]::UtcNow.AddSeconds($Timeout)
  $lastError = $null
  while ([DateTime]::UtcNow -lt $deadline) {
    try {
      return Invoke-RestMethod -Uri $URL -Method Get -Headers $Headers -TimeoutSec 5
    } catch {
      $lastError = $_.Exception.Message
      Start-Sleep -Milliseconds 500
    }
  }
  throw "Timed out waiting for $URL. Last error: $lastError"
}

function Start-CPAService {
  param(
    [string]$Binary,
    [string]$ConfigPath
  )

  $owner = Assert-PortOwner -Port $CPAPort -ExpectedPath $Binary -ServiceName 'CPA core'
  if ($owner) {
    Write-Host "CPA core is already running (PID $owner)."
    return
  }
  $recordedPID = Get-VerifiedCPAPid -ExpectedPath $Binary
  if ($recordedPID) {
    Write-Host "CPA core process is starting (PID $recordedPID)."
    return
  }

  $arguments = @()
  if ($ConfigPath) {
    $arguments += @('--config', $ConfigPath)
  }
  $process = Start-Process -FilePath $Binary -ArgumentList $arguments -WorkingDirectory (Split-Path -Parent $Binary) -RedirectStandardOutput $CPALogFile -RedirectStandardError $CPAErrLogFile -WindowStyle Hidden -PassThru
  $record = [ordered]@{
    pid = $process.Id
    startTimeUtc = $process.StartTime.ToUniversalTime().ToString('o')
    binaryPath = Resolve-NormalizedPath $Binary
  }
  $record | ConvertTo-Json -Compress | Set-Content -LiteralPath $CPAPidFile -Encoding UTF8
  Set-PrivateAcl -Path $CPAPidFile
  Write-Host "Started CPA core (PID $($process.Id))."
}

function Start-ManagerService {
  param(
    [string]$Binary,
    [string[]]$Arguments
  )

  $owner = Assert-PortOwner -Port $ManagerPort -ExpectedPath $Binary -ServiceName 'CPA Manager Plus'
  if ($owner) {
    Write-Host "CPA Manager Plus is already running (PID $owner)."
    return
  }

  $controller = Join-Path (Split-Path -Parent $Binary) 'cpa-manager-plusctl.ps1'
  if (-not (Test-Path -LiteralPath $controller -PathType Leaf)) {
    throw "Missing Manager Plus controller: $controller"
  }

  $previousBin = $env:CPA_MANAGER_PLUS_BIN
  $previousRunDir = $env:CPA_MANAGER_PLUS_RUN_DIR
  $previousLogDir = $env:CPA_MANAGER_PLUS_LOG_DIR
  try {
    $env:CPA_MANAGER_PLUS_BIN = $Binary
    $env:CPA_MANAGER_PLUS_RUN_DIR = $ManagerRunDir
    $env:CPA_MANAGER_PLUS_LOG_DIR = $ManagerLogDir
    & $PowerShellPath -NoProfile -ExecutionPolicy Bypass -File $controller start @Arguments
    if ($LASTEXITCODE -ne 0) {
      throw "cpa-manager-plusctl.ps1 failed with exit code $LASTEXITCODE"
    }
  } finally {
    $env:CPA_MANAGER_PLUS_BIN = $previousBin
    $env:CPA_MANAGER_PLUS_RUN_DIR = $previousRunDir
    $env:CPA_MANAGER_PLUS_LOG_DIR = $previousLogDir
  }
}

function Invoke-CPACompletion {
  param([string]$ApiKey)

  $headers = @{ Authorization = "Bearer $ApiKey" }
  $body = @{
    model = 'gpt-5.5'
    messages = @(@{ role = 'user'; content = 'hi' })
    stream = $false
  } | ConvertTo-Json -Depth 5
  $response = Invoke-RestMethod -Uri "http://127.0.0.1:$CPAPort/v1/chat/completions" -Method Post -Headers $headers -ContentType 'application/json' -Body $body -TimeoutSec $TimeoutSeconds
  if (-not $response.choices -or -not $response.choices[0].message.content) {
    throw 'CPA completion returned no assistant content.'
  }
}

function Get-ManagerStatus {
  param([string]$ManagerKey)

  return Invoke-RestMethod -Uri "http://127.0.0.1:$ManagerPort/status" -Method Get -Headers @{ Authorization = "Bearer $ManagerKey" } -TimeoutSec 10
}

function Test-BusinessReadiness {
  param(
    [string]$CPAApiKey,
    [string]$ManagerKey
  )

  if (-not $CPAApiKey -or -not $ManagerKey) {
    throw "Business readiness requires both credentials. Run '$($MyInvocation.MyCommand.Name) -Configure' once, or set CPA_API_KEY and CPA_MANAGER_PLUS_ADMIN_KEY."
  }

  $baseline = Get-ManagerStatus -ManagerKey $ManagerKey
  if ($baseline.collector.lastError) {
    throw "Manager collector reports an error before the readiness request: $($baseline.collector.lastError)"
  }
  $baselineEvents = [int64]$baseline.events
  $baselineInserted = [int64]$baseline.collector.totalInserted

  Invoke-CPACompletion -ApiKey $CPAApiKey

  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  do {
    Start-Sleep -Milliseconds 500
    $current = Get-ManagerStatus -ManagerKey $ManagerKey
    if ($current.collector.lastError) {
      throw "Manager collector failed after the readiness request: $($current.collector.lastError)"
    }
    if ([int64]$current.events -gt $baselineEvents -and [int64]$current.collector.totalInserted -gt $baselineInserted) {
      Write-Host 'Authenticated CPA completion and Manager SQLite ingestion succeeded.'
      return
    }
  } while ([DateTime]::UtcNow -lt $deadline)

  throw 'CPA completion succeeded, but Manager Plus did not ingest a new SQLite usage event before the timeout.'
}

function Get-ShortcutPaths {
  $startMenuDir = Join-Path ([Environment]::GetFolderPath('Programs')) 'CPA'
  [pscustomobject]@{
    StartMenuDir = $startMenuDir
    StartMenu = Join-Path $startMenuDir 'Start CPA.lnk'
    Desktop = Join-Path ([Environment]::GetFolderPath('DesktopDirectory')) 'Start CPA.lnk'
  }
}

function Install-LauncherShortcuts {
  param(
    [string]$IconPath,
    [switch]$IncludeDesktop
  )

  $paths = Get-ShortcutPaths
  New-Item -ItemType Directory -Path $paths.StartMenuDir -Force | Out-Null
  $targets = @($paths.StartMenu)
  if ($IncludeDesktop) {
    $targets += $paths.Desktop
  }
  $shell = New-Object -ComObject WScript.Shell
  foreach ($target in $targets) {
    $shortcut = $shell.CreateShortcut($target)
    $shortcut.TargetPath = $PowerShellPath
    $shortcut.Arguments = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`" -NoPause"
    $shortcut.WorkingDirectory = $ScriptDir
    $shortcut.IconLocation = "$IconPath,0"
    $shortcut.Description = 'Start CPA core and CPA Manager Plus'
    $shortcut.Save()
  }
  Write-Host 'CPA shortcuts installed. Use Win+S and search for Start CPA.'
}

function Uninstall-LauncherShortcuts {
  $paths = Get-ShortcutPaths
  Remove-Item -LiteralPath $paths.StartMenu -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $paths.Desktop -Force -ErrorAction SilentlyContinue
  if (Test-Path -LiteralPath $paths.StartMenuDir) {
    $remaining = Get-ChildItem -LiteralPath $paths.StartMenuDir -Force -ErrorAction SilentlyContinue
    if (-not $remaining) {
      Remove-Item -LiteralPath $paths.StartMenuDir -Force
    }
  }
  Write-Host 'CPA shortcuts removed.'
}

$lockHandle = $null
try {
  Ensure-PrivateDirectory -Path $StateRoot
  Ensure-PrivateDirectory -Path $RunDir
  Ensure-PrivateDirectory -Path $LogDir
  Ensure-PrivateDirectory -Path $ManagerRunDir
  Ensure-PrivateDirectory -Path $ManagerLogDir

  if ($Configure) {
    Save-LauncherSecrets
    return
  }
  if ($UninstallShortcuts) {
    Uninstall-LauncherShortcuts
    return
  }

  $rootCandidates = @($ScriptDir, (Split-Path -Parent $ScriptDir))
  $managerBinary = Find-Executable -ExplicitPath $ManagerPath -EnvironmentPath $env:CPA_MANAGER_PLUS_BIN -Names @('cpa-manager-plus.exe') -Roots $rootCandidates
  if (-not $managerBinary) {
    throw 'Unable to locate cpa-manager-plus.exe. Use -ManagerPath or CPA_MANAGER_PLUS_BIN.'
  }
  $cpaBinary = Find-Executable -ExplicitPath $CPAPath -EnvironmentPath $env:CPA_CORE_BIN -Names @('cliproxyapi.exe', 'cli-proxy-api.exe') -Roots $rootCandidates
  if (-not $cpaBinary) {
    throw 'Unable to locate cliproxyapi.exe or cli-proxy-api.exe. Use -CPAPath or CPA_CORE_BIN.'
  }

  if ($InstallShortcuts) {
    Install-LauncherShortcuts -IconPath $managerBinary -IncludeDesktop:$DesktopShortcut
    return
  }

  $lockHandle = [System.IO.File]::Open($LockFile, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
  $resolvedConfig = $CPAConfigPath
  if (-not $resolvedConfig -and $env:CPA_CONFIG_PATH) {
    $resolvedConfig = $env:CPA_CONFIG_PATH
  }
  if (-not $resolvedConfig) {
    $candidateConfig = Join-Path (Split-Path -Parent $cpaBinary) 'config.yaml'
    if (Test-Path -LiteralPath $candidateConfig -PathType Leaf) {
      $resolvedConfig = Resolve-NormalizedPath $candidateConfig
    }
  }

  $secrets = Read-LauncherSecrets
  if (-not $secrets.CPAApiKey -or -not $secrets.ManagerKey) {
    throw "Readiness credentials are missing. Run '$($MyInvocation.MyCommand.Name) -Configure' once, or set CPA_API_KEY and CPA_MANAGER_PLUS_ADMIN_KEY."
  }

  Start-CPAService -Binary $cpaBinary -ConfigPath $resolvedConfig
  Wait-HttpReady -URL "http://127.0.0.1:$CPAPort/v1/models" -Timeout $TimeoutSeconds -Headers @{ Authorization = "Bearer $($secrets.CPAApiKey)" } | Out-Null
  Start-ManagerService -Binary $managerBinary -Arguments $ManagerArguments
  Wait-HttpReady -URL "http://127.0.0.1:$ManagerPort/health" -Timeout $TimeoutSeconds -Headers @{} | Out-Null

  Test-BusinessReadiness -CPAApiKey $secrets.CPAApiKey -ManagerKey $secrets.ManagerKey

  $panelURL = "http://127.0.0.1:$ManagerPort/management.html"
  Write-Host "CPA core: $cpaBinary"
  Write-Host "CPA Manager Plus: $managerBinary"
  Write-Host "Panel: $panelURL"
  if (-not $NoBrowser) {
    Start-Process $panelURL
  }
} catch {
  Write-Error $_
  exit 1
} finally {
  if ($lockHandle) {
    $lockHandle.Dispose()
  }
  if (-not $NoPause -and $Host.Name -notmatch 'ServerRemoteHost') {
    Write-Host 'Press any key to close this window...'
    [void]$Host.UI.RawUI.ReadKey('NoEcho,IncludeKeyDown')
  }
}
