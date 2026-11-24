#!/bin/sh
set -eu
PATH=/usr/bin:/bin:$PATH

# Expose only the selected downloader, checksum utility and ZIP reader.
tools="$(cd "$IFYTV_INSTALL_HOME" && pwd)/tools"
mkdir "$tools"
for utility in chmod mktemp rm rmdir cp grep; do
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$(command -v "$utility")" > "$tools/$utility"
done
cat > "$tools/uname" <<'SH'
#!/bin/sh
case "$1" in
    -s) echo "$IFYTV_TEST_OS" ;;
    -m) echo "$IFYTV_TEST_ARCH" ;;
esac
SH
cat > "$tools/sysctl" <<'SH'
#!/bin/sh
[ "$IFYTV_TEST_TRANSLATED" = true ] && echo 1 || echo 0
SH
if [ "$IFYTV_TEST_FALLBACK" = true ]; then
    downloader=wget
    hash=shasum
    extract=bsdtar
else
    downloader=curl
    hash=sha256sum
    extract=unzip
fi
cat > "$tools/$downloader" <<'SH'
#!/bin/sh
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o|-O|--output) shift; destination=$1 ;;
        https://*) address=$1 ;;
    esac
    shift
done
printf '%s' "$address" > "$IFYTV_INSTALL_HOME/url"
cp "$IFYTV_TEST_ZIP" "$destination"
SH
if command -v "$hash" >/dev/null; then
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$(command -v "$hash")" > "$tools/$hash"
elif [ "$hash" = shasum ]; then
    printf '#!/bin/sh\n[ "$1" = -a ] && [ "$2" = 256 ] || exit 96\nshift 2\nexec "%s" "$@"\n' "$(command -v sha256sum)" > "$tools/$hash"
else
    printf '#!/bin/sh\nexec "%s" -a 256 "$@"\n' "$(command -v shasum)" > "$tools/$hash"
fi
if [ -n "$IFYTV_TEST_PYTHON" ]; then
    printf '#!/bin/sh\necho "tar (GNU tar) 1.35"\n' > "$tools/tar"
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$IFYTV_TEST_PYTHON" > "$tools/python3"
elif command -v "$extract" >/dev/null; then
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$(command -v "$extract")" > "$tools/$extract"
elif [ "$extract" = bsdtar ]; then
    printf '#!/bin/sh\n[ "$1" = -xOf ] || exit 97\nshift\nexec "%s" -p "$@"\n' "$(command -v unzip)" > "$tools/$extract"
else
    printf '#!/bin/sh\n[ "$1" = -p ] || exit 97\nshift\nexec "%s" -xOf "$@"\n' "$(command -v bsdtar)" > "$tools/$extract"
fi
chmod +x "$tools"/*
shell=$(command -v sh)
PATH=$tools
export PATH
exec "$shell" -s -- "$@" < "$IFYTV_INSTALL_HOME/install.sh"
