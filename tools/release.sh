#!/bin/sh
# Builds what a release offers for download into dist/release:
#
#   xibalba_<version>_linux_<arch>.tar.gz   program, example configuration, service file
#   xibalba_<version>_<arch>.deb            package for Debian and Ubuntu
#   SHA256SUMS                              checksums of all of them
#
# Usage: tools/release.sh VERSION     (for example 0.1.0; no leading "v")
# Needs go, tar, gzip, dpkg-deb, sha256sum.
set -eu

VERSION="${1:?usage: tools/release.sh VERSION}"
case "$VERSION" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "the version must look like 1.2.3 or 1.2.3-rc1, not \"$VERSION\"" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/dist/release"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
rm -rf "$OUT"
mkdir -p "$OUT"
cd "$ROOT"

# The time of the commit, so that building the same commit twice gives the
# same files.
STAMP="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
export SOURCE_DATE_EPOCH="$STAMP"
LDFLAGS="-s -w -X github.com/MaMoja/xibalba/internal/buildinfo.Version=$VERSION"

# go architecture : GOARM : Debian architecture
for target in amd64::amd64 arm64::arm64 arm:7:armhf; do
    goarch="${target%%:*}"; rest="${target#*:}"; goarm="${rest%%:*}"; debarch="${rest#*:}"
    name="$goarch"; [ -n "$goarm" ] && name="armv$goarm"
    bin="$WORK/bin-$name/xibalba"
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
        go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$bin" ./cmd/xibalba

    # The archive: unpack and copy.
    dir="$WORK/xibalba_${VERSION}_linux_$name"
    mkdir -p "$dir"
    cp "$bin" LICENSE README.md xibalba.example.yaml examples/systemd/xibalba.service "$dir/"
    tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$STAMP" \
        -C "$WORK" -cf - "xibalba_${VERSION}_linux_$name" | gzip -n -9 > "$OUT/xibalba_${VERSION}_linux_$name.tar.gz"

    # The package.
    pkg="$WORK/deb-$name"
    install -D -m 0755 "$bin" "$pkg/usr/bin/xibalba"
    install -D -m 0644 packaging/xibalba.yaml "$pkg/etc/xibalba/xibalba.yaml"
    install -d -m 0755 "$pkg/etc/xibalba/rules"
    install -d -m 0755 "$pkg/lib/systemd/system"
    sed -e 's|/usr/local/bin/xibalba|/usr/bin/xibalba|g' -e '1,/^$/d' examples/systemd/xibalba.service > "$pkg/lib/systemd/system/xibalba.service"
    chmod 0644 "$pkg/lib/systemd/system/xibalba.service"
    install -D -m 0644 xibalba.example.yaml "$pkg/usr/share/doc/xibalba/xibalba.example.yaml"
    install -D -m 0644 README.md "$pkg/usr/share/doc/xibalba/README.md"
    install -D -m 0644 LICENSE "$pkg/usr/share/doc/xibalba/copyright"
    install -d -m 0755 "$pkg/DEBIAN"
    install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm "$pkg/DEBIAN/"
    echo "/etc/xibalba/xibalba.yaml" > "$pkg/DEBIAN/conffiles"
    size="$(du -sk "$pkg" | cut -f1)"
    cat > "$pkg/DEBIAN/control" <<CONTROL
Package: xibalba
Version: $VERSION
Architecture: $debarch
Maintainer: The Xibalba Authors <noreply@github.com>
Installed-Size: $size
Depends: adduser
Section: web
Priority: optional
Homepage: https://github.com/MaMoja/xibalba
Description: protects a website from AI crawlers and other bots
 Xibalba stands in front of a website and decides for every request whether
 it is let through, has to pass a security check, or is blocked. One program,
 no other service needed, nothing about visitors sent to third parties.
CONTROL
    find "$pkg" -exec touch -h -d "@$STAMP" {} +
    dpkg-deb --root-owner-group -Zxz --build "$pkg" "$OUT/xibalba_${VERSION}_$debarch.deb" >/dev/null
done

cd "$OUT"
sha256sum xibalba_* > SHA256SUMS
ls -l
