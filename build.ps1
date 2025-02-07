#!/usr/bin/env pwsh

$ErrorActionPreference = "Stop"

function Write-ColorOutput($ForegroundColor) {
    $fc = $host.UI.RawUI.ForegroundColor
    $host.UI.RawUI.ForegroundColor = $ForegroundColor
    if ($args) {
        Write-Output $args
    }
    $host.UI.RawUI.ForegroundColor = $fc
}

$projectRoot = $PSScriptRoot
$outputDir = Join-Path $projectRoot "output"

Write-ColorOutput Green "Creating output directories..."
$platforms = @(
    "windows_amd64",
    "linux_amd64",
    "linux_arm64",
    "darwin_amd64",
    "darwin_arm64"
)

foreach ($platform in $platforms) {
    $null = New-Item -ItemType Directory -Force -Path (Join-Path $outputDir $platform)
}

function Build-Binary {
    param (
        [string]$os,
        [string]$arch,
        [string]$component
    )
    
    $outDir = Join-Path $outputDir "${os}_${arch}"
    $extension = if ($os -eq "windows") { ".exe" } else { "" }
    $outPath = Join-Path $outDir "$component$extension"
    
    Write-ColorOutput Cyan "Building ${component} for ${os}/${arch}..."
    $env:GOOS = $os
    $env:GOARCH = $arch
    
    try {
        go build -o $outPath "$projectRoot/$component/main.go"
        return $outPath
    }
    catch {
        Write-ColorOutput Red "Failed to build ${component} for ${os}/${arch}: $_"
        exit 1
    }
}

Write-ColorOutput Green "Starting build process..."
$buildTime = Get-Date
$builtFiles = @()

foreach ($platform in $platforms) {
    $os, $arch = $platform.Split("_")
    $builtFiles += Build-Binary -os $os -arch $arch -component "kankans"
    $builtFiles += Build-Binary -os $os -arch $arch -component "kankanc"
}

Write-ColorOutput Green "`nBuild completed successfully at $buildTime"
Write-ColorOutput Yellow "`nBuilt files:"
foreach ($file in $builtFiles) {
    Write-ColorOutput White "- $file"
}
