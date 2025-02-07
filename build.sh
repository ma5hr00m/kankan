#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${PROJECT_ROOT}/output"

print_color() {
    local color=$1
    shift
    echo -e "${color}$*${NC}"
}

print_color "$GREEN" "Creating output directories..."
PLATFORMS=(
    "windows_amd64"
    "linux_amd64"
    "linux_arm64"
    "darwin_amd64"
    "darwin_arm64"
)

for platform in "${PLATFORMS[@]}"; do
    mkdir -p "${OUTPUT_DIR}/${platform}"
done

build_binary() {
    local os=$1
    local arch=$2
    local component=$3
    
    local out_dir="${OUTPUT_DIR}/${os}_${arch}"
    local extension=""
    [[ $os == "windows" ]] && extension=".exe"
    local out_path="${out_dir}/${component}${extension}"
    
    print_color "$CYAN" "Building ${component} for ${os}/${arch}..."
    
    GOOS=$os GOARCH=$arch go build -o "$out_path" "${PROJECT_ROOT}/${component}/main.go"
    echo "$out_path"
}

print_color "$GREEN" "Starting build process..."
BUILD_TIME=$(date '+%Y-%m-%d %H:%M:%S')
built_files=()

for platform in "${PLATFORMS[@]}"; do
    IFS='_' read -r os arch <<< "$platform"
    built_files+=("$(build_binary "$os" "$arch" "kankans")")
    built_files+=("$(build_binary "$os" "$arch" "kankanc")")
done

print_color "$GREEN" $'\nBuild completed successfully at '"$BUILD_TIME"
print_color "$YELLOW" $'\nBuilt files:'
for file in "${built_files[@]}"; do
    print_color "$NC" "- $file"
done
