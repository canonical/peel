#!/usr/bin/env python3
"""Updates streams/v1/images.json and streams/v1/index.json for the peel
LXD simplestreams remote.

For each --asset arch=path given, this upserts a per-architecture "product"
in images.json (peel:current:<arch>:default) and adds (or replaces, if one
already exists for the same release tag) a "version" entry pointing at the
release asset that will be uploaded alongside this script's output.

The item path stored in images.json is relative to the simplestreams
remote's base URL (e.g. "v1.0.0/peel-amd64-lxd.tar.xz"), which is exactly
the shape LXD's simplestreams client joins onto the remote URL. Since a
GitHub release asset lives at:

    https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>

adding the remote as:

    lxc remote add peel https://github.com/<owner>/<repo>/releases/download

makes that relative path resolve to the correct download URL.

The item uses ftype "lxd_combined.tar.gz", which is how LXD's simplestreams
client (shared/simplestreams/products.go) recognises a *unified* LXD image
tarball (metadata.yaml + templates/ + rootfs/ all in one file, as produced
by image/build.sh) without requiring a separate rootfs/squashfs item.
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
import sys
from typing import Any

FORMAT = "products:1.0"


def sha256sum(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)

    return h.hexdigest()


def load_images(path: str | None) -> dict[str, Any]:
    if path and os.path.exists(path):
        with open(path) as f:
            data = json.load(f)

        data.setdefault("products", {})
        return data

    return {
        "content_id": "images",
        "format": FORMAT,
        "datatype": "image-downloads",
        "products": {},
    }


def upsert_version(
    product: dict[str, Any],
    tag: str,
    item_key: str,
    item: dict[str, Any],
    timestamp: str,
) -> None:
    for version in product["versions"].values():
        if version.get("label") == tag:
            version["items"][item_key] = item
            return

    key = timestamp
    while key in product["versions"]:
        key += "0"

    product["versions"][key] = {"label": tag, "items": {item_key: item}}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Release tag, e.g. v1.0.0")
    parser.add_argument(
        "--existing-images", help="Path to a previously published images.json, if any"
    )
    parser.add_argument("--output-images", required=True)
    parser.add_argument("--output-index", required=True)
    parser.add_argument(
        "--asset",
        action="append",
        default=[],
        metavar="ARCH=PATH",
        help="Architecture (GOARCH name, e.g. amd64) and local path to its built image tarball",
    )
    args = parser.parse_args()

    if not args.asset:
        print("error: at least one --asset is required", file=sys.stderr)
        return 1

    images = load_images(args.existing_images)

    now = datetime.datetime.now(datetime.timezone.utc)
    timestamp = now.strftime("%Y%m%d_%H%M")

    for asset in args.asset:
        if "=" not in asset:
            print(
                f"error: invalid --asset {asset!r}, expected ARCH=PATH", file=sys.stderr
            )
            return 1

        arch, path = asset.split("=", 1)

        product_key = f"peel:current:{arch}:default"
        product = images["products"].setdefault(
            product_key,
            {
                "aliases": "peel",
                "arch": arch,
                "distro": "peel",
                "os": "peel",
                "release": "current",
                "release_title": "current",
                "variant": "default",
                "requirements": {},
                "versions": {},
            },
        )
        product.setdefault("versions", {})

        item = {
            "ftype": "lxd_combined.tar.gz",
            "path": f"{args.version}/{os.path.basename(path)}",
            "size": os.path.getsize(path),
            "sha256": sha256sum(path),
        }

        upsert_version(product, args.version, "lxd.tar.xz", item, timestamp)

    images["updated"] = now.strftime("%Y-%m-%dT%H:%M:%SZ")

    os.makedirs(os.path.dirname(args.output_images) or ".", exist_ok=True)
    with open(args.output_images, "w") as f:
        json.dump(images, f, indent=2)
        f.write("\n")

    index = {
        "format": "index:1.0",
        "index": {
            "images": {
                "datatype": "image-downloads",
                "path": "streams/v1/images.json",
                "format": FORMAT,
                "updated": images["updated"],
                "products": sorted(images["products"].keys()),
            }
        },
    }

    os.makedirs(os.path.dirname(args.output_index) or ".", exist_ok=True)
    with open(args.output_index, "w") as f:
        json.dump(index, f, indent=2)
        f.write("\n")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
