// Package ghapp mints short-lived, repository-scoped GitHub App installation
// tokens.
//
// zotui hands one to each job's sandbox so the remote system can access exactly
// the repository the job needs, with a credential that expires within the hour.
// The App private key never leaves the host - only the minted, narrowly-scoped
// token is injected into the sandbox.
package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/openzot/openzot/internal/zotui/source"
)

// App is a configured GitHub App installation. It implements source.Source.
type App struct {
	AppID          int64
	InstallationID int64
	key            *rsa.PrivateKey
}

var _ source.Source = (*App)(nil)

// New parses the App's PEM private key (PKCS#1 or PKCS#8) and returns an App.
func New(appID, installationID int64, pemKey string) (*App, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("ghapp: no PEM block found in the private key")
	}

	key, err := parseRSA(block.Bytes)
	if err != nil {
		return nil, err
	}

	return &App{AppID: appID, InstallationID: installationID, key: key}, nil
}

func parseRSA(der []byte) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}

	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("ghapp: parse private key: %w", err)
	}

	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("ghapp: private key is not RSA")
	}

	return rk, nil
}

// MintToken mints an installation access token scoped to repos, expiring within
// the hour.
//
// Flow: build the signed JWT assertion (assertion, below) that authenticates as
// the App, then POST it to /app/installations/{InstallationID}/access_tokens with
// {"repositories": repos, "permissions": {contents: write, pull_requests: write}}
// and return the {token, expires_at} the API issues.
func (a *App) MintToken(ctx context.Context, repos []string) (*source.Token, error) {
	if _, err := a.assertion(time.Now()); err != nil {
		return nil, err
	}

	// TODO: exchange the assertion for a scoped installation token via the GitHub
	// API and return it. Until then the flow is wired end to end but stops here.
	_ = repos
	return nil, errors.New("ghapp: installation-token exchange not wired yet")
}

// ListRepositories discovers the repositories this installation can access, as
// "owner/name" strings. This is the set offered when no explicit lockdown list is
// configured - the installation is scoped to a single organization and whatever
// repositories the App has been granted.
//
// Flow (TODO wire): mint an installation token (unscoped), then GET
// /installation/repositories with it, paginating, and collect each repository's
// full_name.
func (a *App) ListRepositories(ctx context.Context) ([]string, error) {
	if _, err := a.assertion(time.Now()); err != nil {
		return nil, err
	}
	return nil, errors.New("ghapp: repository discovery not wired yet")
}

// assertion builds the RS256-signed JWT that authenticates zotui *as the App*.
// GitHub rejects clock skew, so the issued-at is backdated slightly and the
// lifetime kept under the 10-minute maximum.
func (a *App) assertion(now time.Time) (string, error) {
	header := b64url(`{"alg":"RS256","typ":"JWT"}`)
	claims := b64url(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-30*time.Second).Unix(), now.Add(9*time.Minute).Unix(), a.AppID))

	signingInput := header + "." + claims
	digest := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("ghapp: sign assertion: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
