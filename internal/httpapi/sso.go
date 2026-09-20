package httpapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type SSOConfig struct {
	Issuer, ServiceID, ServiceKey, ClientID, ClientSecret, RedirectURL, EncryptionKey string
}
type SSOClient struct {
	config   SSOConfig
	http     *http.Client
	cipher   cipher.AEAD
	mu       sync.Mutex
	provider *oidc.Provider
}
type ssoError struct{ Status int }

func (e *ssoError) Error() string { return fmt.Sprintf("SSO responded with HTTP %d", e.Status) }
func NewSSO(cfg SSOConfig) (*SSOClient, error) {
	for _, raw := range []string{cfg.Issuer, cfg.RedirectURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return nil, errors.New("SSO URLs must use HTTPS (HTTP is allowed on loopback only)")
		}
	}
	key, e := hex.DecodeString(cfg.EncryptionKey)
	if e != nil || len(key) != 32 {
		return nil, errors.New("SESSION_ENCRYPTION_KEY must be 64 hex characters")
	}
	serviceKey, e := hex.DecodeString(cfg.ServiceKey)
	if e != nil || len(serviceKey) != 32 || cfg.ServiceID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("SSO service and OAuth client credentials are required")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &SSOClient{config: cfg, cipher: aead, http: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *SSOClient) seal(token, aad string) []byte {
	nonce := make([]byte, c.cipher.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		panic(e)
	}
	return c.cipher.Seal(nonce, nonce, []byte(token), []byte(aad))
}
func (c *SSOClient) unseal(data []byte, aad string) (string, error) {
	n := c.cipher.NonceSize()
	if len(data) < n {
		return "", errors.New("invalid encrypted session")
	}
	plain, e := c.cipher.Open(nil, data[:n], data[n:], []byte(aad))
	return string(plain), e
}
func (c *SSOClient) call(ctx context.Context, path string, in, out any) error {
	if c == nil {
		return errors.New("SSO is not configured")
	}
	data, e := json.Marshal(in)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.config.Issuer, "/")+"/internal/v1/"+path, bytes.NewReader(data))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-ID", c.config.ServiceID)
	req.Header.Set("X-Service-Key", c.config.ServiceKey)
	res, e := c.http.Do(req)
	if e != nil {
		return errors.New("SSO connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &ssoError{res.StatusCode}
	}
	if out == nil {
		io.Copy(io.Discard, io.LimitReader(res.Body, 8192))
		return nil
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out); e != nil {
		return errors.New("invalid SSO response")
	}
	return nil
}
func (c *SSOClient) identity(ctx context.Context, kind, token string) (User, error) {
	var result struct {
		User User `json:"user"`
	}
	e := c.call(ctx, kind+"/introspect", map[string]string{"accessToken": token}, &result)
	var remote *ssoError
	if errors.As(e, &remote) && remote.Status == 401 {
		return User{}, pgx.ErrNoRows
	}
	if e == nil && !validSubject(result.User.ID) {
		return User{}, errors.New("invalid SSO subject")
	}
	return result.User, e
}
func validSubject(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && strings.ToLower(id) == id
}
func (c *SSOClient) Profiles(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	for len(ids) > 0 {
		n := min(len(ids), 100)
		var res struct {
			Users []struct {
				ID       string `json:"id"`
				Nickname string `json:"nickname"`
			} `json:"users"`
		}
		if e := c.call(ctx, "users/lookup", map[string]any{"userIds": ids[:n]}, &res); e != nil {
			return nil, e
		}
		for _, u := range res.Users {
			names[u.ID] = u.Nickname
		}
		ids = ids[n:]
	}
	return names, nil
}
func (c *SSOClient) search(ctx context.Context, q string) ([]string, error) {
	ids := []string{}
	after := ""
	for {
		var res struct {
			IDs  []string `json:"userIds"`
			Next string   `json:"next"`
		}
		if e := c.call(ctx, "users/search", map[string]string{"query": q, "after": after}, &res); e != nil {
			return nil, e
		}
		ids = append(ids, res.IDs...)
		if res.Next == "" {
			return ids, nil
		}
		if res.Next <= after || len(ids) > 100000 {
			return nil, errors.New("SSO search exceeded limit")
		}
		after = res.Next
	}
}
func (c *SSOClient) oidc(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx = oidc.ClientContext(ctx, c.http)
	if c.provider == nil {
		p, e := oidc.NewProvider(ctx, c.config.Issuer)
		if e != nil {
			return nil, nil, errors.New("SSO discovery failed")
		}
		c.provider = p
	}
	cfg := &oauth2.Config{ClientID: c.config.ClientID, ClientSecret: c.config.ClientSecret, RedirectURL: c.config.RedirectURL, Endpoint: c.provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile"}}
	return c.provider, cfg, nil
}
