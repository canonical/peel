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

package pull

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/canonical/peel/internal/certs"
	"github.com/canonical/peel/internal/config"
)

// UserAgent is sent with every registry request peel makes.
const UserAgent = "peel/0 (+https://github.com/canonical/peel)"

// Image fetches the image described by cfg from its registry, selecting the
// manifest matching cfg.Platform (or the host's platform by default) if the
// reference points at a multi-arch index.
func Image(ctx context.Context, cfg *config.Config) (v1.Image, error) {
	var opts []name.Option
	opts = append(opts, name.WeakValidation)
	if cfg.Insecure {
		opts = append(opts, name.Insecure)
	}

	ref, err := name.ParseReference(cfg.Image, opts...)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference %q: %w", cfg.Image, err)
	}

	platform, err := resolvePlatform(cfg.Platform)
	if err != nil {
		return nil, err
	}

	transport, err := httpTransport()
	if err != nil {
		return nil, err
	}

	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithAuth(authenticator(cfg)),
		remote.WithPlatform(platform),
		remote.WithUserAgent(UserAgent),
		remote.WithTransport(transport),
	)
	if err != nil {
		return nil, fmt.Errorf("pulling %s: %w", cfg.Image, err)
	}
	return img, nil
}

// httpTransport builds an http.Transport that trusts peel's embedded CA
// bundle, since the rootfs has no system trust store of its own to fall
// back on (especially not before anything has been unpacked onto it yet).
func httpTransport() (*http.Transport, error) {
	pool, err := certs.Pool()
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool}
	return t, nil
}

// authenticator builds an authn.Authenticator from the credentials in cfg.
// It never contacts a credential helper or keychain: peel only trusts
// credentials that were explicitly provided via its configuration.
func authenticator(cfg *config.Config) authn.Authenticator {
	switch {
	case cfg.Username != "" || cfg.Password != "":
		return &authn.Basic{Username: cfg.Username, Password: cfg.Password}
	case cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "":
		return authn.FromConfig(authn.AuthConfig{
			Auth:          cfg.Auth,
			IdentityToken: cfg.IdentityToken,
			RegistryToken: cfg.RegistryToken,
		})
	default:
		return authn.Anonymous
	}
}

// resolvePlatform parses an "os/arch[/variant]" string, defaulting to the
// host's OS and architecture when s is empty.
func resolvePlatform(s string) (v1.Platform, error) {
	if s == "" {
		return v1.Platform{OS: "linux", Architecture: runtime.GOARCH}, nil
	}
	parts := strings.Split(s, "/")
	p := v1.Platform{OS: parts[0]}
	switch len(parts) {
	case 2:
		p.Architecture = parts[1]
	case 3:
		p.Architecture = parts[1]
		p.Variant = parts[2]
	default:
		return v1.Platform{}, fmt.Errorf("invalid platform %q, expected \"os/arch\" or \"os/arch/variant\"", s)
	}
	return p, nil
}
