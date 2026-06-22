param(
	[ValidateSet("auto", "v1", "v2", "v3", "v4")]
	[string]$GOAMD64 = "auto",

	[string]$Output = "better-cloudflare-ip.exe",

	[string]$PGO = "auto"
)

$ErrorActionPreference = "Stop"

function Resolve-GoExe {
	$goCommand = Get-Command go -ErrorAction SilentlyContinue
	if ($goCommand) {
		return $goCommand.Source
	}

	$defaultGoExe = "C:\Program Files\Go\bin\go.exe"
	if (Test-Path -LiteralPath $defaultGoExe) {
		return $defaultGoExe
	}

	throw "go.exe was not found in PATH or $defaultGoExe"
}

function Find-NativeGOAMD64 {
	param([string]$GoExe)

	$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("better-cloudflare-ip-goamd64-" + [System.Guid]::NewGuid().ToString("N"))
	New-Item -ItemType Directory -Path $tempDir | Out-Null

	try {
		$probeFile = Join-Path $tempDir "probe.go"
		Set-Content -Path $probeFile -Encoding ASCII -Value "package main`nfunc main() {}`n"

		foreach ($level in @("v4", "v3", "v2", "v1")) {
			$probeExe = Join-Path $tempDir ("probe-" + $level + ".exe")
			$env:GOAMD64 = $level
			& $GoExe build -o $probeExe $probeFile *> $null
			if ($LASTEXITCODE -ne 0) {
				continue
			}

			& $probeExe *> $null
			if ($LASTEXITCODE -eq 0) {
				return $level
			}
		}

		return "v1"
	}
	finally {
		Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
	}
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $scriptDir

$goExe = Resolve-GoExe

$hostOS = (& $goExe env GOHOSTOS).Trim()
$hostArch = (& $goExe env GOHOSTARCH).Trim()

if ($hostOS -ne "windows") {
	throw "This script only builds a local Windows .exe. Current GOHOSTOS=$hostOS."
}

$env:GOOS = $hostOS
$env:GOARCH = $hostArch
$env:CGO_ENABLED = "0"

if ($hostArch -eq "amd64") {
	if ($GOAMD64 -eq "auto") {
		$GOAMD64 = Find-NativeGOAMD64 -GoExe $goExe
	}
	$env:GOAMD64 = $GOAMD64
	Write-Host "Using GOAMD64=$GOAMD64"
}
else {
	Remove-Item Env:\GOAMD64 -ErrorAction SilentlyContinue
	Write-Host "GOAMD64 is only used on amd64; current GOARCH=$hostArch"
}

$outputPath = Join-Path $scriptDir $Output
$hasModule = Test-Path -LiteralPath (Join-Path $scriptDir "go.mod")
$pgoValue = $PGO.Trim()
if ($pgoValue -eq "") {
	$pgoValue = "auto"
}
if ($pgoValue -ne "auto" -and $pgoValue -ne "off") {
	$pgoValue = (Resolve-Path -LiteralPath $pgoValue).Path
}
$pgoFlag = "-pgo=$pgoValue"

Write-Host "Building $outputPath for $($env:GOOS)/$($env:GOARCH)"
Write-Host "Using PGO=$pgoValue"
if ($hasModule) {
	Remove-Item Env:\GO111MODULE -ErrorAction SilentlyContinue
	& $goExe build -trimpath -buildvcs=false $pgoFlag -ldflags="-s -w" -o $outputPath .
}
else {
	$env:GO111MODULE = "off"
	$mainFile = Join-Path $scriptDir "main.go"
	Write-Host "No go.mod found; building main.go in GOPATH mode"
	& $goExe build -trimpath $pgoFlag -ldflags="-s -w" -o $outputPath $mainFile
}
if ($LASTEXITCODE -ne 0) {
	throw "go build failed with exit code $LASTEXITCODE"
}

$file = Get-Item -LiteralPath $outputPath
Write-Host ("Built {0} ({1:N2} MB)" -f $file.FullName, ($file.Length / 1MB))
