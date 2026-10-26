#!/usr/bin/env bash
# Compile/link the real bridge, without Go's build cache or starting capture.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cc="${CC:-gcc}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Windows capture SDK compiler: $cc"
"$cc" --version
printf 'int main(void) { return 0; }\n' > "$work/main.c"
if ! "$cc" -std=c11 -Wall -Wextra -Werror \
    "$work/main.c" "$root"/internal/adapters/platform/wgc_*_windows.c \
    -o "$work/wgc-sdk-check.exe" \
    -ld3d11 -ldxgi -lruntimeobject -ldwmapi -lole32 -luuid; then
    echo 'Windows capture SDK check failed: use MSYS2 MINGW64 with current WinRT/WGC headers and import libraries.' >&2
    exit 1
fi
echo 'Windows capture SDK compile/link: PASS (no capture performed)'
