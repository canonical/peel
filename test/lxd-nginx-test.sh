#!/bin/sh
# test/lxd-nginx-test.sh is an end-to-end test of peel on a real LXD host.
#
# It builds the peel LXD image, imports it, and launches a container
# configured to run public.ecr.aws/nginx/nginx:latest. It then:
#
#   1. Waits for the container to get an IPv4 address.
#   2. Waits for nginx to actually answer HTTP requests (proving peel
#      pulled the image, unpacked its layers, and exec'd its entrypoint).
#   3. Checks /peel/state.json records the pulled image reference.
#   4. Restarts the container and confirms /peel/state.json's
#      "unpacked_at" timestamp does NOT change, proving peel skipped
#      re-pulling the already-unpacked image.
#
# Requires a working LXD install (a storage pool and a network/NIC on the
# "default" profile, e.g. via `lxd init`) and `curl` on the host running
# this script. The container it creates and the image it imports are
# removed on exit, whether the test passes or fails.
#
# Usage:
#   test/lxd-nginx-test.sh
#
# Environment overrides:
#   IMAGE_REF   OCI image to run (default: public.ecr.aws/nginx/nginx:latest)
#   BOOT_WAIT   seconds to wait for the first boot's pull+unpack (default: 180)
#   KEEP        if set to 1, don't delete the container/image on exit

set -eu

IMAGE_REF=${IMAGE_REF:-public.ecr.aws/nginx/nginx:latest}
BOOT_WAIT=${BOOT_WAIT:-180}
KEEP=${KEEP:-0}

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RUN_ID=$(date +%s)-$$
IMAGE_ALIAS="peel-test-img-$RUN_ID"
CONTAINER="peel-test-ct-$RUN_ID"
WORK_DIR=$(mktemp -d)

log() { printf '\n>>> %s\n' "$*" >&2; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
	if [ "$KEEP" = "1" ]; then
		log "KEEP=1 set, leaving $CONTAINER and $IMAGE_ALIAS in place"
	else
		log "cleaning up"
		lxc delete --force "$CONTAINER" >/dev/null 2>&1 || true
		lxc image delete "$IMAGE_ALIAS" >/dev/null 2>&1 || true
	fi
	rm -rf -- "$WORK_DIR"
}
trap cleanup EXIT

# wait_for retries a command (its arguments) once a second until it
# succeeds or the given number of seconds elapses.
wait_for() {
	tries=$1
	shift
	i=0
	while [ "$i" -lt "$tries" ]; do
		if "$@"; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

container_ip() {
	lxc list "$CONTAINER" -c 4 --format csv 2>/dev/null | head -n1 | sed 's/ .*//'
}

have_ip() {
	ip=$(container_ip)
	[ -n "$ip" ]
}

nginx_responds() {
	ip=$(container_ip)
	[ -n "$ip" ] && curl -fsS -o /dev/null --max-time 2 "http://$ip/"
}

unpacked_at() {
	lxc exec "$CONTAINER" -- sed -n 's/.*"unpacked_at": *"\([^"]*\)".*/\1/p' /peel/state.json
}

command -v lxc >/dev/null 2>&1 || fail "lxc not found; install and configure LXD first"
command -v curl >/dev/null 2>&1 || fail "curl not found; it's needed to talk to the container"
lxc list >/dev/null 2>&1 || fail "could not talk to the LXD daemon"
[ -n "$(lxc storage list --format csv 2>/dev/null)" ] || fail "no LXD storage pool configured (try: lxd init)"

log "building the peel LXD image"
"$ROOT_DIR/image/build.sh" -o "$WORK_DIR/peel.tar.xz"

log "importing image as $IMAGE_ALIAS"
lxc image import "$WORK_DIR/peel.tar.xz" --alias "$IMAGE_ALIAS"

log "launching $CONTAINER with user.oci.image=$IMAGE_REF"
lxc launch "local:$IMAGE_ALIAS" "$CONTAINER" -c "user.oci.image=$IMAGE_REF" -c "security.devlxd=false"

log "waiting for $CONTAINER to get an IPv4 address"
wait_for 60 have_ip || fail "$CONTAINER never got an IPv4 address"
log "container address: $(container_ip)"

log "waiting up to ${BOOT_WAIT}s for peel to pull, unpack and start nginx"
wait_for "$BOOT_WAIT" nginx_responds || fail "nginx never responded on http://$(container_ip)/"
log "PASS: nginx responded on http://$(container_ip)/"

log "checking /peel/state.json records the pulled image"
lxc exec "$CONTAINER" -- grep -q "\"reference\": \"$IMAGE_REF\"" /peel/state.json ||
	fail "/peel/state.json does not reference $IMAGE_REF"
unpacked_at_1=$(unpacked_at)
[ -n "$unpacked_at_1" ] || fail "could not read unpacked_at from /peel/state.json"
log "unpacked_at (before restart): $unpacked_at_1"

log "restarting $CONTAINER to verify the already-unpacked image is not re-pulled"
lxc restart "$CONTAINER"

log "waiting for $CONTAINER to come back up"
wait_for 60 nginx_responds || fail "nginx never came back up after restart"

unpacked_at_2=$(unpacked_at)
log "unpacked_at (after restart):  $unpacked_at_2"
[ "$unpacked_at_1" = "$unpacked_at_2" ] || fail "image was re-unpacked after restart (unpacked_at changed)"
log "PASS: restart skipped re-pulling the already-unpacked image"

log "ALL TESTS PASSED"
