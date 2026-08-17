package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, string(encoded)
}

func verifyAssertion(t *testing.T, authorization string, key *rsa.PrivateKey) {
	t.Helper()
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		t.Errorf("authorization = %q", authorization)
		return
	}
	parts := strings.Split(strings.TrimPrefix(authorization, prefix), ".")
	if len(parts) != 3 {
		t.Errorf("JWT has %d parts", len(parts))
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Errorf("decode JWT signature: %v", err)
		return
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("verify JWT signature: %v", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Errorf("decode JWT claims: %v", err)
		return
	}
	var claims struct {
		Issuer string `json:"iss"`
		Issued int64  `json:"iat"`
		Expiry int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Errorf("decode JWT claims: %v", err)
		return
	}
	if claims.Issuer != "123456" || claims.Expiry-claims.Issued > int64(10*time.Minute/time.Second) {
		t.Errorf("JWT claims = %+v", claims)
	}
}

func TestMintTokenScopesRequestToRepository(t *testing.T) {
	key, pemKey := testKey(t)
	expires := time.Date(2026, time.August, 17, 22, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/7654321/access_tokens" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong request", http.StatusNotFound)
			return
		}
		verifyAssertion(t, r.Header.Get("Authorization"), key)
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2026-03-10" {
			t.Errorf("API version = %q", got)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var repositories []string
		if err := json.Unmarshal(body["repositories"], &repositories); err != nil {
			t.Errorf("repositories: %v", err)
		}
		if len(repositories) != 1 || repositories[0] != "api" {
			t.Errorf("repositories = %v", repositories)
		}
		if _, configured := body["permissions"]; configured {
			t.Error("token request must inherit permissions from the GitHub App")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_scoped", "expires_at": expires})
	}))
	t.Cleanup(server.Close)

	provider, err := newApp(123456, 7654321, pemKey, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	token, err := provider.MintToken(context.Background(), []string{"acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if token.Value != "ghs_scoped" || !token.ExpiresAt.Equal(expires) || len(token.Repos) != 1 || token.Repos[0] != "acme/api" {
		t.Fatalf("token = %+v", token)
	}
}

func TestListRepositoriesUsesInstallationTokenAndPaginates(t *testing.T) {
	key, pemKey := testKey(t)
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7654321/access_tokens":
			verifyAssertion(t, r.Header.Get("Authorization"), key)
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode token request: %v", err)
			}
			if _, scoped := body["repositories"]; scoped {
				t.Error("discovery token must cover the installation")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation", "expires_at": time.Now().Add(time.Hour).UTC(),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			getCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer ghs_installation" {
				t.Errorf("repository authorization = %q", got)
			}
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Errorf("per_page = %q", got)
			}
			page := r.URL.Query().Get("page")
			if page == "1" {
				repositories := make([]map[string]string, 100)
				for i := range repositories {
					repositories[i] = map[string]string{"full_name": fmt.Sprintf("acme/repo-%03d", i)}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "repositories": repositories})
				return
			}
			if page != "2" {
				t.Errorf("page = %q", page)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 101, "repositories": []map[string]string{{"full_name": "acme/aaa"}},
			})
		default:
			http.Error(w, "wrong request", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	provider, err := newApp(123456, 7654321, pemKey, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := provider.ListRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 101 || repositories[0] != "acme/aaa" || repositories[100] != "acme/repo-099" {
		t.Fatalf("repositories = %v", repositories)
	}
	if getCalls != 2 {
		t.Fatalf("repository calls = %d", getCalls)
	}
}

func TestConfigurationAndRequestErrors(t *testing.T) {
	_, pemKey := testKey(t)
	for name, configured := range map[string]struct {
		appID, installationID int64
		key                   string
	}{
		"app ID":          {appID: 0, installationID: 1, key: pemKey},
		"installation ID": {appID: 1, installationID: 0, key: pemKey},
		"private key":     {appID: 1, installationID: 1, key: "not PEM"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(configured.appID, configured.installationID, configured.key); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied by GitHub", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	provider, err := newApp(1, 2, pemKey, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.MintToken(context.Background(), []string{"missing-slash"}); err == nil {
		t.Fatal("expected invalid repository error")
	}
	if _, err := provider.MintToken(context.Background(), []string{"acme/api"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("API error = %v", err)
	}
}
