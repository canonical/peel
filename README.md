# peel

peel is a minimal init (PID 1) for [LXD](https://canonical.com/lxd) containers
that unpeels an OCI image into a running LXD container: it pulls the
image from a registry, unpacks its layers directly onto the container's root
filesystem, and execs the image's entrypoint.

It is meant to be installed as `/sbin/init` inside an otherwise-empty LXD
image (see [`image/`](image/)). The LXD image itself carries no application
code; peel downloads that at container start time, based on a handful of LXD
instance configuration keys.

> [!WARNING]
> `peel` is a prototype. It should not be used in a production environment.

## How it works

When the container first starts, peel reads its configuration from
`/peel/config.json` (rendered by an LXD image template from the instance
configuration keys listed below), brings up networking. Peel only ever
unpeels once: if the OCI is already unpeeled, it goes straight to starting the
entrypoint.

Otherwise, it pulls the image's manifest, config and layers from the
registry and extracts every layer directly onto `/`, honouring whiteout
files as defined by the [OCI image spec](https://github.com/opencontainers/image-spec/blob/main/layer.md).
peel never deletes anything on its own initiative: only whatever the image's own
layers overwrite or explicitly whiteout changes on disk. Layers are never
permitted to touch `/sbin/init` or anything under `/peel/`, so an image can't
replace peel or tamper with peel's own state.

`/peel/config.json` is deleted once peel has unpacked the OCI image, so registry
credentials don't linger in the rootfs.

Finally, peel resolves the command to run (the image's entrypoint/cmd/env/
working directory/user, together with any of peel's own overrides),
execs it, and supervises the container from then on: reaping every exited
process (including re-parented orphans), and turning `SIGINT`/`SIGPWR` (or
the entrypoint exiting on its own) into a `reboot(2)` syscall, which LXD
intercepts to actually restart or stop the container.

### Networking

LXD hands the container a bare network interface with no address and no
`/etc/resolv.conf`. Since peel's own rootfs starts out essentially empty,
peel is also its own minimal IPv4/IPv6 configurator: on every boot it brings
up every non-loopback interface via DHCPv4 and IPv6 SLAAC, and writes the
results to `/etc/resolv.conf`. This runs before peel ever talks to a
registry, and is best-effort: a failure on any interface or protocol is
logged but never blocks boot.

Peel embeds a copy of [Mozilla's CA bundle as distributed by curl](https://curl.se/docs/caextract.html)
for its own OCI registry connections.

## Instance configuration keys

Set these with `lxc config set <instance> <key> <value>` before starting the
container (or as part of `lxc launch/init ... -c key=value`). Only
`user.oci.image` is required.

| Key                        | Effect                                                                                 |
| --------------------------- | --------------------------------------------------------------------------------------- |
| `user.oci.image`            | **Required.** The OCI image reference, e.g. `docker.io/library/nginx:1.27`.             |
| `user.oci.platform`         | `os/arch[/variant]` to pull, e.g. `linux/arm64`. Defaults to the host's platform.       |
| `user.oci.insecure`         | `true` to allow talking to the registry over plain HTTP.                               |
| `user.oci.username`         | Registry username (basic auth).                                                        |
| `user.oci.password`         | Registry password (basic auth).                                                        |
| `user.oci.auth`             | `base64(username:password)`, as an alternative to username/password.                   |
| `user.oci.identity_token`   | Registry identity token (as in a Docker `config.json` auth entry).                      |
| `user.oci.registry_token`   | Registry bearer token (as in a Docker `config.json` auth entry).                        |
| `user.oci.entrypoint`       | JSON array overriding the image's `Entrypoint`, e.g. `["/bin/myapp"]`.                  |
| `user.oci.cmd`               | JSON array overriding the image's `Cmd`, e.g. `["--flag","value"]`.                     |
| `user.oci.env`               | JSON array of `"KEY=VALUE"` strings, merged on top of the image's `Env`.                |
| `user.oci.working_dir`      | Overrides the image's working directory.                                               |
| `user.oci.user`              | Overrides the image's user, as `uid[:gid]` or `name[:group]`.                          |

It is recommended to set `security.devlxd=false` to prevent a container process
reading any configuration.

## Building the LXD image

```sh
image/build.sh                    # host architecture -> image/dist/peel-<arch>.tar.xz
image/build.sh -a arm64            # cross-compiled for arm64
lxc image import image/dist/peel-amd64.tar.xz --alias peel
lxc init peel my-container -c user.oci.image=docker.io/library/nginx:1.27
lxc start my-container
```

See `image/build.sh -h` for all options. The script cross-compiles peel with
`CGO_ENABLED=0`, so building for `arm64`/`ppc64le`/etc. from an `amd64` host
works without a cross toolchain.

## Installing a pre-built image

Every tagged release (see [`.github/workflows/release.yml`](.github/workflows/release.yml))
publishes a peel image for each supported architecture as a release asset,
and republishes a small [simplestreams](https://images.lxd.canonical.com/streams/v1/index.json)
index/images document as assets on a separate `streams/v1` release. That
means this repository doubles as an LXD image server:

```sh
lxc remote add peel https://github.com/canonical/peel/releases/download --protocol simplestreams
lxc launch peel:peel my-container -c user.oci.image=docker.io/library/nginx:1.27
```

`lxc image list peel:` will show the latest published image for your local
architecture; older versions remain available and can be selected by
fingerprint.
