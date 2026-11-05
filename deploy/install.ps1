<#
  工作台账 work-ledger 一键部署脚本（Windows，需“以管理员身份运行” PowerShell）

  默认安装：
    irm https://github.tool.do/yooer/work-ledger/raw/main/deploy/install.ps1 | iex

  带参数安装：
    & ([scriptblock]::Create((irm https://github.tool.do/yooer/work-ledger/raw/main/deploy/install.ps1))) -Listen 0.0.0.0:8080 -AdminPassword '至少8位'

  已安装时再次运行会自动转为升级（等同 wlctl upgrade）。
#>
param(
    [string]$Version = $(if ($env:WL_VERSION) { $env:WL_VERSION } else { 'latest' }),
    [string]$Listen = $(if ($env:WL_LISTEN) { $env:WL_LISTEN } else { '127.0.0.1:8080' }),
    [string]$AdminUser = 'admin',
    [string]$AdminPassword = $env:ADMIN_PASSWORD,
    [string]$HomeDir = 'C:\ProgramData\work-ledger',
    [string]$BaseUrl = $(if ($env:WL_BASE_URL) { $env:WL_BASE_URL } else { 'https://github.tool.do/yooer/work-ledger' }),
    [switch]$Demo
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'

function Info($m) { Write-Host "==> $m" -ForegroundColor Green }
function Die($m) { Write-Host "[错误] $m" -ForegroundColor Red; exit 1 }

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { Die '请以管理员身份运行 PowerShell。' }
if (-not [Environment]::Is64BitOperatingSystem) { Die '仅支持 64 位 Windows。' }

$Asset = 'work-ledger-windows-amd64.exe'
$Bin = Join-Path $HomeDir 'bin\work-ledger.exe'
function ReleaseUrl($file) {
    if ($Version -eq 'latest') { "$BaseUrl/releases/latest/download/$file" } else { "$BaseUrl/releases/download/$Version/$file" }
}

$env:WL_HOME = $HomeDir

if (Test-Path $Bin) {
    Info "检测到已安装（$Bin），执行升级 …"
    if ($Version -eq 'latest') { & $Bin ctl upgrade -y } else { & $Bin ctl upgrade $Version -y }
    exit $LASTEXITCODE
}

$tmp = Join-Path $env:TEMP ("wl-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Info "下载 $Asset（$Version）…"
    Invoke-WebRequest -UseBasicParsing -Uri (ReleaseUrl 'sha256sums.txt') -OutFile "$tmp\sha256sums.txt"
    Invoke-WebRequest -UseBasicParsing -Uri (ReleaseUrl $Asset) -OutFile "$tmp\$Asset"
    $line = Get-Content "$tmp\sha256sums.txt" | Where-Object { ($_ -split '\s+')[1] -replace '^\*', '' -eq $Asset } | Select-Object -First 1
    if (-not $line) { Die "sha256sums.txt 中没有 $Asset" }
    $expect = ($line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash "$tmp\$Asset" -Algorithm SHA256).Hash.ToLower()
    if ($expect -ne $actual) { Die "sha256 校验失败：期望 $expect，实际 $actual" }
    Info 'sha256 校验通过。'

    foreach ($d in 'bin', 'config', 'data', 'backups', 'logs', 'tmp') { New-Item -ItemType Directory -Force -Path (Join-Path $HomeDir $d) | Out-Null }
    Copy-Item "$tmp\$Asset" $Bin -Force

    $envFile = Join-Path $HomeDir 'config\.env'
    if (-not (Test-Path $envFile)) {
        Info "生成配置文件 $envFile …"
        $bytes = New-Object byte[] 32
        [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
        $token = -join ($bytes | ForEach-Object { $_.ToString('x2') })
        $content = @"
# ===== 基础配置 =====
APP_NAME="工作台账"
APP_ENV=production
APP_DEBUG=false
APP_TIMEZONE=Asia/Shanghai
APP_URL=http://$Listen
LISTEN_ADDR=$Listen
SESSION_NAME=work_ledger_session
SESSION_LIFETIME_HOURS=12

# ===== AI 接口 =====
AI_API_TOKEN=$token
AI_BASE_URL=https://api.deepseek.com/v1
AI_MODEL=deepseek-chat
AI_API_KEY=
AI_TIMEOUT=120

# ===== 二次验证 =====
TOTP_ISSUER="工作台账"
REQUIRE_2FA=false

# ===== 升级源（GitHub 经 github.tool.do 代理）=====
UPDATE_BASE_URL=$BaseUrl
"@
        [IO.File]::WriteAllText($envFile, $content, (New-Object Text.UTF8Encoding($false)))
        # 仅允许管理员与 SYSTEM 访问配置文件
        icacls $envFile /inheritance:r /grant:r 'Administrators:F' 'SYSTEM:F' | Out-Null
    }

    Info '初始化数据库 …'
    $installArgs = @('install', "--admin-user=$AdminUser")
    if ($AdminPassword) { $installArgs += "--admin-password=$AdminPassword" }
    if ($Demo) { $installArgs += '--demo' }
    $installOutput = & $Bin @installArgs 2>&1 | Out-String
    Write-Host $installOutput
    if ($LASTEXITCODE -ne 0) { Die '数据库初始化失败。' }

    Info '注册 Windows 服务 …'
    & $Bin service install
    if ($LASTEXITCODE -ne 0) { Die '注册服务失败。' }

    $binDir = Join-Path $HomeDir 'bin'
    $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    if (($machinePath -split ';') -notcontains $binDir) {
        [Environment]::SetEnvironmentVariable('Path', "$machinePath;$binDir", 'Machine')
        $env:Path = "$env:Path;$binDir"
    }

    Start-Service WorkLedger
    $hostPart, $port = $Listen -split ':(?=[^:]+$)'
    if ($hostPart -in @('', '0.0.0.0', '::', '[::]')) { $hostPart = '127.0.0.1' }
    $ok = $false
    for ($i = 0; $i -lt 30; $i++) {
        try { Invoke-WebRequest -UseBasicParsing "http://${hostPart}:$port/api/v1/health" -TimeoutSec 3 | Out-Null; $ok = $true; break } catch { Start-Sleep 1 }
    }
    if (-not $ok) { Die '服务未通过健康检查，请查看 C:\ProgramData\work-ledger\logs\app.log' }

    Write-Host ''
    Info '部署完成！'
    Write-Host "  访问地址：http://$Listen"
    Write-Host "  运行目录：$HomeDir"
    Write-Host "  配置文件：$envFile（配置 AI_API_KEY 后执行 wlctl restart）"
    Write-Host '  管理命令（新开管理员 PowerShell）：wlctl status | logs | check | upgrade | backup'
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
