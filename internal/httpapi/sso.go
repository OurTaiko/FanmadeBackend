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
	"net"
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
	// Optional TCP destination for the issuer, e.g. sso:8090 inside Docker.
	// The public issuer, HTTP Host and TLS certificate verification stay unchanged.
	ConnectAddress string
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

// Application client secrets need not be hex. Accept header-safe opaque credentials;
// SSO remains authoritative for verification and application capability checks.
func validServiceCredential(value string, minLen, maxLen int) bool {
	if len(value) < minLen || len(value) > maxLen {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

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
	if !validServiceCredential(cfg.ServiceID, 1, 100) || !validServiceCredential(cfg.ServiceKey, 40, 1024) || cfg.ClientID == "" || cfg.ClientSecret == "" {
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
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if cfg.ConnectAddress != "" {
		host, port, err := net.SplitHostPort(cfg.ConnectAddress)
		if err != nil || host == "" || port == "" {
			return nil, errors.New("SSO_CONNECT_ADDRESS must be host:port")
		}
		issuer, _ := url.Parse(cfg.Issuer)
		issuerPort := issuer.Port()
		if issuerPort == "" {
			issuerPort = "443"
			if issuer.Scheme == "http" {
				issuerPort = "80"
			}
		}
		issuerAddress := net.JoinHostPort(issuer.Hostname(), issuerPort)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// This explicit route must not send private SSO traffic through an env proxy.
		proxy := transport.Proxy
		transport.Proxy = func(r *http.Request) (*url.URL, error) {
			if r.URL.Scheme == issuer.Scheme && r.URL.Host == issuer.Host {
				return nil, nil
			}
			return proxy(r)
		}
		dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == issuerAddress {
				address = cfg.ConnectAddress
			}
			return dialer.DialContext(ctx, network, address)
		}
		client.Transport = transport
	}
	return &SSOClient{config: cfg, cipher: aead, http: client}, nil
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
	result.User.AvatarURL = c.avatarURL(result.User)
	return result.User, e
}

// avatarURL passes through only the SSO's own content-addressed avatar path for
// this user, so a compromised or misconfigured upstream cannot inject other URLs.
func (c *SSOClient) avatarURL(u User) string {
	prefix := strings.TrimRight(c.config.Issuer, "/") + "/avatars/" + u.ID + "/"
	rest, ok := strings.CutPrefix(u.AvatarURL, prefix)
	digest, ext := strings.CutSuffix(rest, ".webp")
	if !ok || !ext || len(digest) != 32 || strings.Trim(digest, "0123456789abcdef") != "" {
		return ""
	}
	return u.AvatarURL
}
func validSubject(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && strings.ToLower(id) == id
}

// PublicProfile is what SSO publishes about any user: nickname and avatar only.
type PublicProfile struct {
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
}

func (c *SSOClient) Profiles(ctx context.Context, ids []string) (map[string]PublicProfile, error) {
	names := map[string]PublicProfile{}
	for len(ids) > 0 {
		n := min(len(ids), 100)
		var res struct {
			Users []User `json:"users"`
		}
		if e := c.call(ctx, "users/lookup", map[string]any{"userIds": ids[:n]}, &res); e != nil {
			return nil, e
		}
		for _, u := range res.Users {
			names[u.ID] = PublicProfile{Nickname: u.Nickname, AvatarURL: c.avatarURL(u)}
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
