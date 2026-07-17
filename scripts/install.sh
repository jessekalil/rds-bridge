#!/usr/bin/env bash
set -euo pipefail

repo="jessekalil/rds-bridge"
binary="rds-bridge"
install_dir="${HOME}/.local/bin"
tag=""

usage() {
  cat <<'EOF'
Usage: install.sh [--version <tag>] [--dir <directory>]

Install rds-bridge from a GitHub Release. Without --version, installs the
latest release.
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--tag)
      [ "$#" -ge 2 ] || die "$1 needs a value"
      tag="$2"
      shift 2
      ;;
    --dir)
      [ "$#" -ge 2 ] || die "--dir needs a value"
      install_dir="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v install >/dev/null 2>&1 || die "install is required"

os="$(uname -s)"
case "$os" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) die "unsupported operating system: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "unsupported architecture: $arch" ;;
esac

if [ -z "$tag" ]; then
  release="$(curl --fail --silent --show-error --location "https://api.github.com/repos/${repo}/releases/latest")" || die "could not resolve the latest release"
  tag="$(printf '%s\n' "$release" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$tag" ] || die "latest release did not include a tag"
fi

version="${tag#v}"
archive="${binary}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${repo}/releases/download/${tag}"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

curl --fail --silent --show-error --location --output "$temp_dir/$archive" "$base_url/$archive" || die "could not download $archive"
curl --fail --silent --show-error --location --output "$temp_dir/checksums.txt" "$base_url/checksums.txt" || die "could not download checksums.txt"

expected="$(awk -v name="$archive" '$2 == name { print $1 }' "$temp_dir/checksums.txt")"
[ -n "$expected" ] || die "checksum for $archive was not found"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$temp_dir/$archive" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$temp_dir/$archive" | awk '{ print $1 }')"
else
  die "sha256sum or shasum is required"
fi

[ "$actual" = "$expected" ] || die "checksum verification failed for $archive"

tar -xzf "$temp_dir/$archive" -C "$temp_dir"
source_binary="$temp_dir/$binary"
[ -f "$source_binary" ] || die "release archive did not contain $binary"

mkdir -p "$install_dir"
install -m 0755 "$source_binary" "$install_dir/$binary"
printf 'installed %s to %s\n' "$binary" "$install_dir/$binary"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'warning: %s is not on PATH\n' "$install_dir" >&2 ;;
esac
