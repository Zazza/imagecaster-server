#!/usr/bin/env bash
# Linux-сборка stable-diffusion.cpp для установщика: готовые сборки upstream требуют glibc 2.43 (Ubuntu 26.04)
# и не запускаются на Debian 13 / Ubuntu 24.04. Собираем в Ubuntu 22.04 (glibc 2.35).
# usage: tools/sdcpp-build.sh cuda|vulkan|cpu   → dist/sdcpp-linux-x86_64-<variant>.tar.gz (+ .sha256)
set -euo pipefail
cd "$(dirname "$0")/.."

VARIANT="${1:?cuda|vulkan|cpu}"
VULKAN_SDK_VERSION="1.4.363.1"
VULKAN_SDK_SHA="b7867d8ca658563813af666f34b93db87f1b1c7d9d69d97e924c210c7ba33eb8"
COMMIT="465adf3" # = config.SDCppVersion master-1086-465adf3
REPO="https://github.com/leejet/stable-diffusion.cpp.git"
ARCHS="${SDCPP_ARCHS:-61;70;75;80;86;89;90;120}"
SRC="dist/sdcpp-src"
NAME="sdcpp-linux-x86_64-$VARIANT"
OUT="dist/$NAME"

case "$VARIANT" in
  cuda) IMAGE="nvidia/cuda:12.8.1-devel-ubuntu22.04"; FLAGS="-DSD_CUDA=ON -DGGML_CUDA_NCCL=OFF -DCMAKE_CUDA_ARCHITECTURES=$ARCHS" ;;
  vulkan) IMAGE="ubuntu:22.04"; FLAGS="-DSD_VULKAN=ON" ;;
  cpu) IMAGE="ubuntu:22.04"; FLAGS="" ;;
  *) echo "unknown variant: $VARIANT" >&2; exit 2 ;;
esac

mkdir -p dist
if [[ ! -d "$SRC/.git" ]]; then
  git clone --quiet "$REPO" "$SRC"
fi
git -C "$SRC" fetch --quiet origin
git -C "$SRC" checkout --quiet "$COMMIT"
git -C "$SRC" submodule update --quiet --init --recursive

docker run --rm -e FLAGS="$FLAGS" -e VARIANT="$VARIANT" -e VKV="$VULKAN_SDK_VERSION" -e VKSHA="$VULKAN_SDK_SHA" -e OWNER="$(id -u):$(id -g)" -v "$PWD/$SRC:/src" "$IMAGE" bash -euc '
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  apt-get install -y -qq build-essential cmake git curl xz-utils ca-certificates >/dev/null
  if [[ "$VARIANT" == vulkan ]]; then
    curl -fsSL -o /tmp/vk.tar.xz "https://sdk.lunarg.com/sdk/download/$VKV/linux/vulkansdk-linux-x86_64-$VKV.tar.xz"
    echo "$VKSHA  /tmp/vk.tar.xz" | sha256sum -c - >/dev/null
    mkdir -p /opt/vulkan && tar -xJf /tmp/vk.tar.xz -C /opt/vulkan
    export VULKAN_SDK="/opt/vulkan/$VKV/x86_64"
    export PATH="$VULKAN_SDK/bin:$PATH" CMAKE_PREFIX_PATH="$VULKAN_SDK"
    FLAGS="$FLAGS -DVulkan_INCLUDE_DIR=$VULKAN_SDK/include -DVulkan_LIBRARY=$VULKAN_SDK/lib/VulkanLoader/lib/libvulkan.so -DVulkan_GLSLC_EXECUTABLE=$VULKAN_SDK/bin/glslc"
  fi
  STUBS=/tmp/cuda-stubs
  LINK=""
  if [[ "$VARIANT" == cuda ]]; then
    mkdir -p "$STUBS"
    ln -sf /usr/local/cuda/lib64/stubs/libcuda.so "$STUBS/libcuda.so"
    ln -sf /usr/local/cuda/lib64/stubs/libcuda.so "$STUBS/libcuda.so.1"
    LINK="-L$STUBS -Wl,-rpath-link,$STUBS"
  fi
  B=/src/build-$VARIANT
  cmake -S /src -B "$B" $FLAGS -DGGML_NATIVE=OFF -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_EXE_LINKER_FLAGS="$LINK" -DCMAKE_SHARED_LINKER_FLAGS="$LINK" \
    -DCMAKE_BUILD_RPATH_USE_ORIGIN=ON -DCMAKE_BUILD_RPATH="\$ORIGIN" >/dev/null
  cmake --build "$B" --config Release -j "$(nproc)" --target sd-server >/dev/null
  mkdir -p "$B/pkg"
  cp "$B/bin/sd-server" "$B/pkg/"
  if [[ "$VARIANT" == cuda ]]; then
    for lib in libcudart.so.12 libcublas.so.12 libcublasLt.so.12; do
      cp -L "/usr/local/cuda/lib64/$lib" "$B/pkg/"
    done
  fi
  chown -R "$OWNER" /src
'

rm -rf "$OUT"
mkdir -p "$OUT"
cp "$SRC/build-$VARIANT/pkg/"* "$OUT/"
echo "$COMMIT" > "$OUT/COMMIT"
tar -C "$OUT" -czf "dist/$NAME.tar.gz" .
(cd dist && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256")
echo "dist/$NAME.tar.gz"
