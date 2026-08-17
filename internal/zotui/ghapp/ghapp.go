// Package ghapp mints short-lived, repository-scoped GitHub App installation
// tokens.
//
// zotui hands one to each run's sandbox so the remote system can access exactly
// the repository the worker needs, with a credential that expires within the hour.
// The App private key never leaves the host - only the minted, narrowly-scoped
// token is injected into the sandbox.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/zotui/repo"
)

// App is a configured GitHub App installation. It implements repo.Provider.
type App struct {
	AppID          int64
	InstallationID int64
	key            *rsa.PrivateKey
	baseURL        string
	client         *http.Client
}

var _ repo.Provider = (*App)(nil)

const (
	apiVersion     = "2026-03-10"
	defaultBaseURL = "https://api.github.com"
)

// New parses the App's PEM private key (PKCS#1 or PKCS#8) and returns an App.
func New(appID, installationID int64, pemKey string) (*App, error) {
	return newApp(appID, installationID, pemKey, defaultBaseURL, &http.Client{Timeout: 30 * time.Second})
}

func newApp(appID, installationID int64, pemKey, baseURL string, client *http.Client) (*App, error) {
	if appID <= 0 {
		return nil, errors.New("ghapp: app ID must be positive")
	}
	if installationID <= 0 {
		return nil, errors.New("ghapp: installation ID must be positive")
	}
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("ghapp: no PEM block found in the private key")
	}

	key, err := parseRSA(block.Bytes)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("ghapp: API base URL is required")
	}
	if client == nil {
		return nil, errors.New("ghapp: HTTP client is required")
	}

	return &App{AppID: appID, InstallationID: installationID, key: key,
		baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
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
func (a *App) MintToken(ctx context.Context, repos []string) (*repo.Token, error) {
	names, err := repositoryNames(repos)
	if err != nil {
		return nil, err
	}
	issued, err := a.exchange(ctx, names)
	if err != nil {
		return nil, err
	}
	return &repo.Token{Value: issued.Token, ExpiresAt: issued.ExpiresAt, Repos: slices.Clone(repos)}, nil
}

// ListRepositories discovers the repositories this installation can access, as
// "owner/name" strings. This is the set offered when no explicit lockdown list is
// configured - the installation is scoped to a single organization and whatever
// repositories the App has been granted.
func (a *App) ListRepositories(ctx context.Context) ([]string, error) {
	issued, err := a.exchange(ctx, nil)
	if err != nil {
		return nil, err
	}

	const pageSize = 100
	repositories := make([]string, 0)
	for page := 1; ; page++ {
		query := url.Values{"per_page": {fmt.Sprint(pageSize)}, "page": {fmt.Sprint(page)}}
		request, err := a.request(ctx, http.MethodGet, "/installation/repositories?"+query.Encode(), nil,
			"Bearer "+issued.Token)
		if err != nil {
			return nil, err
		}
		var response struct {
			TotalCount   int `json:"total_count"`
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := a.do(request, &response); err != nil {
			return nil, fmt.Errorf("ghapp: list installation repositories: %w", err)
		}
		for _, discovered := range response.Repositories {
			if discovered.FullName != "" {
				repositories = append(repositories, discovered.FullName)
			}
		}
		if len(response.Repositories) < pageSize || len(repositories) >= response.TotalCount {
			break
		}
	}
	sort.Strings(repositories)
	return repositories, nil
}

type installationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *App) exchange(ctx context.Context, repositories []string) (*installationToken, error) {
	assertion, err := a.assertion(time.Now())
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Repositories []string `json:"repositories,omitempty"`
	}{Repositories: repositories})
	if err != nil {
		return nil, fmt.Errorf("ghapp: encode token request: %w", err)
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", a.InstallationID)
	request, err := a.request(ctx, http.MethodPost, path, bytes.NewReader(body), "Bearer "+assertion)
	if err != nil {
		return nil, err
	}
	var issued installationToken
	if err := a.do(request, &issued); err != nil {
		return nil, fmt.Errorf("ghapp: mint installation token: %w", err)
	}
	if issued.Token == "" || issued.ExpiresAt.IsZero() {
		return nil, errors.New("ghapp: GitHub returned an incomplete installation token")
	}
	return &issued, nil
}

func (a *App) request(ctx context.Context, method, path string, body io.Reader, authorization string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("ghapp: create GitHub request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func (a *App) do(request *http.Request, target any) error {
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("GitHub request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("GitHub API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func repositoryNames(repositories []string) ([]string, error) {
	if len(repositories) == 0 {
		return nil, errors.New("ghapp: at least one repository is required")
	}
	names := make([]string, len(repositories))
	for index, repository := range repositories {
		owner, name, valid := strings.Cut(repository, "/")
		if !valid || owner == "" || name == "" || strings.Contains(name, "/") ||
			strings.TrimSpace(repository) != repository {
			return nil, fmt.Errorf("ghapp: repository %q must be owner/name", repository)
		}
		names[index] = name
	}
	return names, nil
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
