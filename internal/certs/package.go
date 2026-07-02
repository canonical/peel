// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package certs embeds a bundle of CA root certificates for peel's own use,
// and can install it into the rootfs for the entrypoint's use too.
//
// peel's rootfs starts out with nothing in it, in particular no
// /etc/ssl/certs: without a trust store, peel could not make the very
// first, TLS-verified request to a registry. cacert.pem is Mozilla's CA
// bundle as distributed by the curl project
// (https://curl.se/docs/caextract.html), embedded at build time so that
// peel never depends on anything else being present in the image.
package certs
