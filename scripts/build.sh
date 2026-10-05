#!/usr/bin/env bash
# Build the plugin and its speech engine for one target, into the repository
# root, where plugin.json names them (`run` and each stage's `binary`).
# CI and the release workflow both call this, so what CI checks on every push
# is what a release ships.
#
#   scripts/build.sh <goos> <goarch> <rust-target>
#   scripts/build.sh darwin arm64 aarch64-apple-darwin
set -euo pipefail
goos=$1 goarch=$2 target=$3
cd "$(dirname "$0")/.."

exe=""
[ "$goos" = windows ] && exe=.exe

GOOS=$goos GOARCH=$goarch go build -C src -trimpath -ldflags='-s -w' -o "../announcements-plugin$exe" .

# The engine is distributed under the GPL-3.0 (it statically links espeak-ng),
# so it is built only against the unmodified sherpa-onnx release libraries,
# which the sherpa-onnx-sys crate downloads when SHERPA_ONNX_LIB_DIR is unset.
unset SHERPA_ONNX_LIB_DIR
cargo build --locked --release --target "$target" --manifest-path stages/sherpa-tts/Cargo.toml

cp "stages/sherpa-tts/target/$target/release/sherpa_tts$exe" .
echo "built announcements-plugin$exe and sherpa_tts$exe for $goos/$goarch"
