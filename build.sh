declare -A build_targets
build_targets=(["amd64"]="linux windows darwin" ["arm64"]="linux android darwin")
rule_filename="rules.dat"
dist_name=${1}

function prepare() {
    cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1 && pwd)" || exit 1  # change to project directory
    if [ ! -d "./dist"  ]
    then
      mkdir ./dist
    fi
    export CGO_ENABLED=0
    wget https://github.com/HerrKKK/domain-list-community/releases/latest/download/${rule_filename}
}

function main() {
    prepare
    for arch in ${!build_targets[*]}
      do
        go_os_array=${build_targets[$arch]}
        for os in ${go_os_array[@]}
        do
          export GOARCH=${arch}
          export GOOS=${os}
          target="${dist_name}-${GOOS}-${GOARCH}"
          if [ "${GOOS}" == "windows" ]
          then
            go build -o "./${target}.exe"
            zip "${target}.zip" "./${target}.exe" ${rule_filename} config.json
            go build -o "./${target}_cli.exe" -ldflags="-H windowsgui"
            zip "${target}.zip" "./${target}_cli.exe" ${rule_filename} config.json
          else
            go build -o "./${target}"
            tar czvf "${dist_name}-${GOOS}-${GOARCH}.tar.gz" "./${target}" ${rule_filename} config.json
          fi
        done
      done

    mv ./*.tar.gz ./dist
    mv ./*.zip ./dist
}

main
