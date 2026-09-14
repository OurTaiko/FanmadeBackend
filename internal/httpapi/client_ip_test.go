package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrust(t *testing.T) {
	for _, tc := range []struct{ name, proxies, peer, header, expected string }{
		{"default ignores headers", "", "127.0.0.1:1234", "198.51.100.1", "127.0.0.1"},
		{"trusted loopback", "127.0.0.1/32", "127.0.0.1:1234", "198.51.100.1", "198.51.100.1"},
		{"untrusted peer", "127.0.0.1/32", "192.0.2.1:1234", "198.51.100.1", "192.0.2.1"},
		{"IPv6", "::1/128", "[::1]:1234", "2001:db8::1", "2001:db8::1"},
		{"mapped IPv4", "127.0.0.1/32", "[::ffff:127.0.0.1]:1234", "::ffff:198.51.100.1", "198.51.100.1"},
		{"invalid value", "127.0.0.1/32", "127.0.0.1:1234", "unknown", "127.0.0.1"},
		{"no header", "127.0.0.1/32", "127.0.0.1:1234", "", "127.0.0.1"},
		{"chain rejected", "127.0.0.1/32", "127.0.0.1:1234", "198.51.100.1, 192.0.2.1", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxies, err := ParseTrustedProxies(tc.proxies)
			if err != nil {
				t.Fatal(err)
			}
			s := New(nil, Config{TrustedProxies: proxies})
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Real-IP", tc.header)
			if got := s.clientIP(r); got != tc.expected {
				t.Fatalf("got %s, want %s", got, tc.expected)
			}
			r.Header.Add("X-Real-IP", "203.0.113.1")
			if got := s.clientIP(r); got == "203.0.113.1" {
				t.Fatal("accepted duplicate IP header")
			}
		})
	}
	for _, value := range []string{"localhost", "127.0.0.1", "127.0.0.1/32,"} {
		if _, err := ParseTrustedProxies(value); err == nil {
			t.Fatalf("accepted invalid CIDR %q", value)
		}
	}
}

func TestProxyRateLimitsSeparateClients(t *testing.T) {
	proxies, _ := ParseTrustedProxies("127.0.0.1/32")
	h := New(nil, Config{Origin: "https://example.test", TrustedProxies: proxies}).Handler()
	call := func(ip string) int {
		r := httptest.NewRequest("POST", "/unknown", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "https://example.test")
		r.Header.Set("X-Real-IP", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for n := 0; n < 40; n++ {
		if code := call("192.0.2.1"); code != 404 {
			t.Fatal(code)
		}
	}
	if code := call("192.0.2.1"); code != 429 {
		t.Fatal(code)
	}
	if code := call("192.0.2.2"); code != 404 {
		t.Fatal("different client shared rate limit", code)
	}
}
