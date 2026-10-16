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
rule_filename="rule.dat"
dist_name=${1:?usage: build.sh <dist_name>}

mkdir -p ./dist

build_rules() {
    # Build rule.dat from the v2fly/domain-list-community submodule.
    # This is OPTIONAL: a missing file only disables named geosite/geoip
    # lists (see router.NewRouter), inline rules still work.
    if [ -f "${rule_filename}" ]; then
        return 0
    fi
    if [ ! -d "third_party/domain-list-community/data" ]; then
        echo "WARNING: submodule third_party/domain-list-community not found; skipping rule.dat build" >&2
        return 0
    fi
    echo "Building ${rule_filename} from submodule..."
    go run ./tools/build-rules/main.go --out="${rule_filename}" || {
        echo "WARNING: failed to build ${rule_filename}; named routing rules will be disabled at runtime" >&2
        rm -f "${rule_filename}"
    }
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
        # only when present, exactly like rule.dat.
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
    build_rules
    for arch in "${!build_targets[@]}"; do
        for os in ${build_targets[$arch]}; do
            build_one "$os" "$arch"
        done
    done
    mv ./*.tar.gz ./*.zip ./dist/
    ls -l ./dist
}

main
