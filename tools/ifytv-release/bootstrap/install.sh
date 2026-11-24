#!/bin/sh
# Keep the entire installer in a function so a partial download cannot run it.
install_ifytv() (
    set -eu
    umask 077
    version='@@VERSION@@'
    system=$(uname -s)
    machine=$(uname -m)
    if [ "$system" = Darwin ] && [ "$machine" = x86_64 ] &&
        [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || :)" = 1 ]; then
        machine=arm64
    fi
    case "$system/$machine" in
        Darwin/x86_64) target=darwin-amd64; expected='@@SHA256_DARWIN_AMD64@@' ;;
        Darwin/arm64) target=darwin-arm64; expected='@@SHA256_DARWIN_ARM64@@' ;;
        Linux/x86_64|Linux/amd64) target=linux-amd64; expected='@@SHA256_LINUX_AMD64@@' ;;
        Linux/aarch64|Linux/arm64) target=linux-arm64; expected='@@SHA256_LINUX_ARM64@@' ;;
        *) printf 'ifytv: unsupported platform %s/%s\n' "$system" "$machine" >&2; exit 1 ;;
    esac

    stage=$(mktemp -d "${TMPDIR:-/tmp}/ifytv-install.XXXXXXXXXX")
    archive=$stage/ifytv.zip
    program=$stage/ifytv
    trap 'rm -f "$archive" "$program"; rmdir "$stage"' 0
    trap 'exit 1' HUP INT TERM

    url="https://github.com/Ruicod/ifytv-cli/releases/download/v$version/ifytv-$version-$target.zip"
    if command -v curl >/dev/null 2>&1; then
        curl -q -fsSL --proto '=https' --proto-redir '=https' -o "$archive" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$archive" "$url"
    else
        echo 'ifytv: install curl or wget to download the release' >&2
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        digest=$(sha256sum < "$archive")
    elif command -v shasum >/dev/null 2>&1; then
        digest=$(shasum -a 256 < "$archive")
    else
        echo 'ifytv: install sha256sum or shasum to verify the release' >&2
        exit 1
    fi
    if [ "${digest%% *}" != "$expected" ]; then
        echo 'ifytv: SHA-256 mismatch for the downloaded release' >&2
        exit 1
    fi

    if command -v unzip >/dev/null 2>&1; then
        unzip -p "$archive" ifytv > "$program"
    elif command -v bsdtar >/dev/null 2>&1; then
        bsdtar -xOf "$archive" ifytv > "$program"
    elif tar --version 2>/dev/null | grep -q bsdtar; then
        tar -xOf "$archive" ifytv > "$program"
    elif command -v python3 >/dev/null 2>&1; then
        python3 -I - "$archive" "$program" <<'PY'
import shutil
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    with archive.open("ifytv") as source, open(sys.argv[2], "xb") as program:
        shutil.copyfileobj(source, program)
PY
    else
        echo 'ifytv: install unzip, bsdtar or python3 to extract the release' >&2
        exit 1
    fi
    chmod 700 "$program"
    "$program" install "$@"
)
install_ifytv "$@"
