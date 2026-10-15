#!/usr/bin/env bash
# Build the full cross-compiled artifact matrix into ./dist.
#   ./build.sh <dist_name>
# Pure stdlib, so every target builds with CGO disabled; the Go toolchain does
# that automatically when GOOS != host, but we set it explicitly for
# reproducibility.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# arch -> space separated list of GOOS values
declare -A build_targets=(["amd64"]="linux windows darwin" ["arm64"]="linux android darwin")
rule_filename="rules.dat"
dist_name=${1:?usage: build.sh <dist_name>}

mkdir -p ./dist

prepare_rules() {
    # rules.dat is OPTIONAL: a missing file only disables named geosite/geoip
    # lists (see router.NewRouter), inline rules still work. Never let a flaky
    # third-party download abort the whole build.
    if [ -f "${rule_filename}" ]; then
        return 0
    fi
    local url="https://github.com/HerrKKK/domain-list-community/releases/latest/download/${rule_filename}"
    if command -v wget >/dev/null 2>&1; then
        wget -O "${rule_filename}" "$url" || rm -f "${rule_filename}"
    elif command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${rule_filename}" "$url" || rm -f "${rule_filename}"
    else
        echo "neither wget nor curl available; skipping ${rule_filename}" >&2
    fi
    if [ ! -s "${rule_filename}" ]; then
        echo "WARNING: ${rule_filename} unavailable; named routing rules will be disabled at runtime" >&2
        rm -f "${rule_filename}"
    fi
}

go_flags=(-trimpath -ldflags "-s -w")

build_one() {
    local os=$1 arch=$2 target="${dist_name}-$1-$2"
    export GOOS="$os" GOARCH="$arch" CGO_ENABLED=0
    if [ "$os" = "windows" ]; then
        # console build + a windowsgui build (no console window) share one zip
        go build "${go_flags[@]}" -o "./${target}.exe"
        go build -trimpath -ldflags="-s -w -H windowsgui" -o "./${target}_cli.exe"
        local payload=("./${target}.exe" "./${target}_cli.exe")
        # config.json is gitignored, so it is absent on a CI checkout: bundle it
        # only when present, exactly like rules.dat.
        if [ -f config.json ]; then payload+=("config.json"); fi
        if [ -f "${rule_filename}" ]; then payload+=("${rule_filename}"); fi
        zip "${target}.zip" "${payload[@]}"
        rm -f "./${target}.exe" "./${target}_cli.exe"
    else
        go build "${go_flags[@]}" -o "./${target}"
        local payload=("./${target}")
        if [ -f config.json ]; then payload+=("config.json"); fi
        if [ -f "${rule_filename}" ]; then payload+=("${rule_filename}"); fi
        tar czf "${target}.tar.gz" "${payload[@]}"
        rm -f "./${target}"
    fi
}

main() {
    prepare_rules
    for arch in "${!build_targets[@]}"; do
        for os in ${build_targets[$arch]}; do
            build_one "$os" "$arch"
        done
    done
    mv ./*.tar.gz ./*.zip ./dist/
    ls -l ./dist
}

main
