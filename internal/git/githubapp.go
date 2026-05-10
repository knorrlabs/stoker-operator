package git

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultGitHubAPIURL = "https://api.github.com"
	pemTypePrivateKey   = "PRIVATE KEY"
)

// GitHubAppTokenResult holds the result of a GitHub App installation token exchange.
type GitHubAppTokenResult struct {
	Token     string
	ExpiresAt time.Time
}

// ExchangeGitHubAppToken exchanges a GitHub App PEM private key for a short-lived
// installation access token. It signs a JWT (RS256, 10-minute expiry), then POSTs
// to the GitHub installations API to obtain a 1-hour bearer token.
func ExchangeGitHubAppToken(ctx context.Context, pemBytes []byte, appID, installationID int64, apiURL string) (*GitHubAppTokenResult, error) {
	if apiURL == "" {
		apiURL = defaultGitHubAPIURL
	}

	key, err := ParseRSAPrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PEM key: %w", err)
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)), // clock skew buffer
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", appID),
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	jwtStr, err := jwtToken.SignedString(key)
	if err != nil {
		return nil, fmt.Errorf("signing JWT: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &GitHubAppTokenResult{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

// ParseRSAPrivateKey parses a PEM-encoded RSA private key, supporting both
// PKCS#1 (RSA PRIVATE KEY) and PKCS#8 (PRIVATE KEY) formats.
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case pemTypePrivateKey:
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}
