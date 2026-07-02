#!/bin/sh
# build.sh assembles a unified LXD image tarball whose rootfs is just
# enough to run peel as PID 1: peel's own binary at /sbin/init, the
# directories LXD expects to find, and the one template that renders
# peel's config.json.
#
# The resulting image does *not* contain an application: peel downloads and
# unpacks that at container start time, based on the LXD instance
# configuration keys listed in image/README.md (or ../README.md).
#
# Usage:
#   image/build.sh [-a GOARCH] [-o OUTPUT.tar.xz]
#
# Examples:
#   image/build.sh                          # host architecture, image/dist/peel-<arch>.tar.xz
#   image/build.sh -a arm64                 # cross-compiled for arm64
#   image/build.sh -o /tmp/peel.tar.xz       # explicit output path
#
# Import the result with:
#   lxc image import image/dist/peel-<arch>.tar.xz --alias peel

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

GOARCH=$(go env GOARCH)
OUTPUT=""

while getopts "a:o:h" opt; do
	case "$opt" in
	a) GOARCH=$OPTARG ;;
	o) OUTPUT=$OPTARG ;;
	h)
		sed -n '2,20p' "$0"
		exit 0
		;;
	*)
		exit 1
		;;
	esac
done

# LXD/LXC image metadata uses its own architecture names rather than Go's.
case "$GOARCH" in
amd64) LXD_ARCH=x86_64 ;;
arm64) LXD_ARCH=aarch64 ;;
arm) LXD_ARCH=armv7l ;;
386) LXD_ARCH=i686 ;;
ppc64le) LXD_ARCH=ppc64le ;;
s390x) LXD_ARCH=s390x ;;
riscv64) LXD_ARCH=riscv64 ;;
*)
	echo "build.sh: unknown GOARCH '$GOARCH', don't know its LXD architecture name" >&2
	exit 1
	;;
esac

: "${OUTPUT:="$ROOT_DIR/image/dist/peel-$GOARCH.tar.xz"}"
mkdir -p "$(dirname -- "$OUTPUT")"

WORK_DIR=$(mktemp -d)
trap 'rm -rf -- "$WORK_DIR"' EXIT

echo "building peel for linux/$GOARCH" >&2
mkdir -p "$WORK_DIR/rootfs/sbin" "$WORK_DIR/rootfs/dev" "$WORK_DIR/rootfs/proc" \
	"$WORK_DIR/rootfs/sys" "$WORK_DIR/rootfs/peel"
(
	cd "$ROOT_DIR"
	CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
		go build -trimpath -ldflags="-s -w" -o "$WORK_DIR/rootfs/sbin/init" ./cmd/peel
)
chmod 0755 "$WORK_DIR/rootfs/sbin/init"

cp -R "$ROOT_DIR/image/templates" "$WORK_DIR/templates"

cat >"$WORK_DIR/metadata.yaml" <<EOF
architecture: $LXD_ARCH
creation_date: $(date +%s)
properties:
  os: peel
  description: peel (linux/$GOARCH) - OCI image runner
  architecture: $GOARCH
templates:
  /peel/config.json:
    when: [start]
    template: config.tpl
EOF

echo "packaging $OUTPUT" >&2
tar --numeric-owner --owner=0 --group=0 -C "$WORK_DIR" \
	-cJf "$OUTPUT" metadata.yaml templates rootfs

echo "done: $OUTPUT" >&2
