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

// Package config loads peel's runtime configuration from disk.
//
// The configuration is a single JSON document rendered by one LXD image
// template (see image/templates/config.tpl) from the container's instance
// configuration keys. Every scalar value in the template is passed through
// pongo2's "escapejs" filter, which escapes quotes, backslashes and control
// characters into \uXXXX sequences; those are also valid JSON escapes, so
// the rendered value is always well-formed JSON regardless of what an
// instance configuration key contains.
//
// The only settings that hold structured data (Entrypoint, Cmd and Env
// overrides) are embedded as raw JSON arrays rather than escaped strings;
// it is the responsibility of whoever sets the corresponding LXD instance
// config key to provide valid JSON for those.
package config
