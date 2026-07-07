#!/bin/sh
# test/lxd-lo-proxy-test.sh is an end-to-end test of peel's socket proxy
# (internal/lo): sharing loopback/wildcard listeners across LXD containers
# via a shared /peel/lo directory.
#
# It builds the peel LXD image, imports it, and launches two containers
# that share a host directory, bind-mounted as /peel/lo in both:
#
#   - "a" runs the real service under test: an OCI image (nginx by
#     default) that listens on 0.0.0.0:80.
#   - "b" runs an idle entrypoint of its own (nothing listens on port 80
#     in "b" otherwise), so port 80 only exists there because peel's
#     socket proxy put it there.
#
# It then:
#
#   1. Waits for both containers to get an IPv4 address.
#   2. Waits for nginx to answer directly in "a" (proving peel there
#      pulled/unpacked/started it, same as test/lxd-nginx-test.sh).
#   3. Waits for "b" to be able to reach nginx's content at its own
#      127.0.0.1:80 -- proving peel discovered "a"'s listener over
#      /peel/lo and forwarded the request through to "a".
#   4. Confirms "b"'s own external address on port 80 does NOT also serve
#      that content: a wildcard-bound proxy listener has to bind 0.0.0.0
#      itself (so local processes behave exactly as they would against
#      the original listener), but must still reject connections that
#      aren't themselves local.
#
# Requires a working LXD install (a storage pool and a network/NIC on the
# "default" profile, e.g. via `lxd init`) and `curl` on the host running
# this script. The containers, shared directory and image it creates are
# removed on exit, whether the test passes or fails.
#
# Usage:
#   test/lxd-lo-proxy-test.sh
#
# Environment overrides:
#   IMAGE_REF      OCI image "a" runs (default: public.ecr.aws/nginx/nginx:latest)
#   IDLE_IMAGE_REF OCI image "b" runs, entrypoint overridden to idle
#                  (default: public.ecr.aws/docker/library/busybox:latest)
#   BOOT_WAIT      seconds to wait for boot + proxy setup (default: 180)
#   KEEP           if set to 1, don't delete containers/image/dir on exit

set -eu

IMAGE_REF=${IMAGE_REF:-public.ecr.aws/nginx/nginx:latest}
IDLE_IMAGE_REF=${IDLE_IMAGE_REF:-public.ecr.aws/docker/library/busybox:latest}
BOOT_WAIT=${BOOT_WAIT:-180}
KEEP=${KEEP:-0}

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RUN_ID=$(date +%s)-$$
IMAGE_ALIAS="peel-test-img-$RUN_ID"
CONTAINER_A="peel-test-lo-a-$RUN_ID"
CONTAINER_B="peel-test-lo-b-$RUN_ID"
WORK_DIR=$(mktemp -d)
SHARE_DIR=$(mktemp -d)

log() { printf '\n>>> %s\n' "$*" >&2; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
	if [ "$KEEP" = "1" ]; then
		log "KEEP=1 set, leaving $CONTAINER_A, $CONTAINER_B, $IMAGE_ALIAS and $SHARE_DIR in place"
	else
		log "cleaning up"
		lxc delete --force "$CONTAINER_A" >/dev/null 2>&1 || true
		lxc delete --force "$CONTAINER_B" >/dev/null 2>&1 || true
		lxc image delete "$IMAGE_ALIAS" >/dev/null 2>&1 || true
		rm -rf -- "$SHARE_DIR"
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
	lxc list "$1" -c 4 --format csv 2>/dev/null | head -n1 | sed 's/ .*//'
}

have_ip() {
	ip=$(container_ip "$1")
	[ -n "$ip" ]
}

responds_at() {
	curl -fsS -o /dev/null --max-time 2 "http://$1/"
}

b_loopback_responds() {
	lxc exec "$CONTAINER_B" -- wget -q -T 2 -O /dev/null http://127.0.0.1:80/
}

command -v lxc >/dev/null 2>&1 || fail "lxc not found; install and configure LXD first"
command -v curl >/dev/null 2>&1 || fail "curl not found; it's needed to talk to the containers"
lxc list >/dev/null 2>&1 || fail "could not talk to the LXD daemon"
[ -n "$(lxc storage list --format csv 2>/dev/null)" ] || fail "no LXD storage pool configured (try: lxd init)"

log "building the peel LXD image"
"$ROOT_DIR/image/build.sh" -o "$WORK_DIR/peel.tar.xz"

log "importing image as $IMAGE_ALIAS"
lxc image import "$WORK_DIR/peel.tar.xz" --alias "$IMAGE_ALIAS"

# Both containers see the same host directory as /peel/lo. Containers may
# have non-overlapping uid maps, so the socket files peel creates in there
# won't necessarily be owned by a uid the other container's root is
# mapped to: relax the directory's permissions rather than fight idmaps,
# since containers sharing loopback listeners are mutually trusting by
# construction anyway (peel itself makes the socket files it creates
# world-read/writable for the same reason).
chmod 0777 "$SHARE_DIR"

log "launching $CONTAINER_A with user.oci.image=$IMAGE_REF"
lxc launch "local:$IMAGE_ALIAS" "$CONTAINER_A" -c "user.oci.image=$IMAGE_REF" -c "security.devlxd=false"
lxc config device add "$CONTAINER_A" lo disk source="$SHARE_DIR" path=/peel/lo >/dev/null

log "launching $CONTAINER_B with an idle entrypoint (user.oci.image=$IDLE_IMAGE_REF)"
lxc launch "local:$IMAGE_ALIAS" "$CONTAINER_B" \
	-c "user.oci.image=$IDLE_IMAGE_REF" \
	-c 'user.oci.entrypoint=["sh","-c","while true; do sleep 3600; done"]' \
	-c "security.devlxd=false"
lxc config device add "$CONTAINER_B" lo disk source="$SHARE_DIR" path=/peel/lo >/dev/null

log "waiting for both containers to get an IPv4 address"
wait_for 60 have_ip "$CONTAINER_A" || fail "$CONTAINER_A never got an IPv4 address"
wait_for 60 have_ip "$CONTAINER_B" || fail "$CONTAINER_B never got an IPv4 address"
ip_a=$(container_ip "$CONTAINER_A")
ip_b=$(container_ip "$CONTAINER_B")
log "container addresses: a=$ip_a b=$ip_b"

log "waiting up to ${BOOT_WAIT}s for peel to pull, unpack and start nginx in $CONTAINER_A"
wait_for "$BOOT_WAIT" responds_at "$ip_a" || fail "nginx never responded on http://$ip_a/"
log "PASS: nginx responded directly on http://$ip_a/"

log "waiting up to ${BOOT_WAIT}s for $CONTAINER_B to proxy nginx onto its own 127.0.0.1:80"
wait_for "$BOOT_WAIT" b_loopback_responds ||
	fail "$CONTAINER_B never got a response from its own 127.0.0.1:80"
log "PASS: $CONTAINER_B's 127.0.0.1:80 is being proxied to $CONTAINER_A's nginx"

log "checking the proxied response actually looks like nginx's content"
response=$(lxc exec "$CONTAINER_B" -- wget -q -T 2 -O - http://127.0.0.1:80/ || true)
case "$response" in
*nginx*) log "PASS: response via $CONTAINER_B's loopback looks like nginx's default page" ;;
*) fail "response via $CONTAINER_B's loopback doesn't look like nginx's default page: $response" ;;
esac

log "checking $CONTAINER_B's external address does NOT also serve it"
if responds_at "$ip_b"; then
	fail "$CONTAINER_B answered on its own external address $ip_b:80; a wildcard proxy listen must reject non-local traffic"
fi
log "PASS: $CONTAINER_B's external address $ip_b:80 rejected the connection, as expected"

log "ALL TESTS PASSED"
