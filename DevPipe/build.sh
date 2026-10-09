#!/usr/bin/env sh
# Gera os executáveis do DevPipe em dist/ (usa vendor/: não precisa de internet).
set -e
cd "$(dirname "$0")"
mkdir -p dist
export CGO_ENABLED=0
for target in windows/amd64 windows/arm64 linux/amd64 darwin/arm64 darwin/amd64; do
  os=${target%/*}; arch=${target#*/}
  out="dist/devpipe-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  echo "→ $out"
  GOOS=$os GOARCH=$arch go build -mod=vendor -trimpath -ldflags "-s -w" -o "$out" ./cmd/devpipe
done
echo "Pronto."
