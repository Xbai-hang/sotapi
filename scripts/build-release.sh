#!/usr/bin/env bash

set -euo pipefail

version=${1:-}
case "$version" in
  ""|*[!A-Za-z0-9._-]*)
    printf '%s\n' 'usage: scripts/build-release.sh <version>' >&2
    exit 2
    ;;
esac

project_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
dist_dir="$project_dir/dist"
mkdir -p "$dist_dir"

# dist is a generated release directory. Remove only artifacts owned by this
# script so a reused runner cannot upload files left by an earlier version.
for old_asset in \
  "$dist_dir"/sotapi_*.zip \
  "$dist_dir"/sotapi_*.tar.gz \
  "$dist_dir"/checksums.txt; do
  if [[ -f "$old_asset" ]]; then
    rm -f -- "$old_asset"
  fi
done

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/sotapi-release.XXXXXX")
cleanup() {
  rm -rf -- "$build_dir"
}
trap cleanup EXIT HUP INT TERM

asset_names=()

build_target() {
  local goos=$1
  local goarch=$2
  local archive=$3
  local target="${goos}_${goarch}"
  local stage="$build_dir/$target"
  local binary=sotapi
  local asset="sotapi_${version}_${target}.${archive}"

  if [[ "$goos" == windows ]]; then
    binary=sotapi.exe
  fi

  mkdir -p "$stage/configs"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags='-s -w' -o "$stage/$binary" ./cmd/sotapi
  cp "$project_dir/README.md" "$project_dir/LICENSE" "$stage/"
  cp "$project_dir/configs/config.example.yaml" "$stage/configs/"

  if [[ "$archive" == zip ]]; then
    (cd "$stage" && zip -q -r "$dist_dir/$asset" .)
  else
    tar -C "$stage" -czf "$dist_dir/$asset" .
  fi
  asset_names+=("$asset")
}

cd "$project_dir"
build_target windows 386 zip
build_target windows amd64 zip
build_target darwin arm64 tar.gz
build_target darwin amd64 tar.gz
build_target linux amd64 tar.gz
build_target linux arm64 tar.gz

(
  cd "$dist_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${asset_names[@]}" >checksums.txt
  else
    shasum -a 256 "${asset_names[@]}" >checksums.txt
  fi
)

printf 'built %d release archives in %s\n' "${#asset_names[@]}" "$dist_dir"
