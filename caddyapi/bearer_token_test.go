package caddyapi

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerTokenRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++; _, _ = io.WriteString(w, `{}`) }))
	defer target.Close()
	for _, destination := range []string{target.URL, "http://localhost:1", "https://example.invalid"} {
		t.Run(destination, func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			c := NewClient(source.URL, nil)
			require.NoError(t, c.SetBearerToken("synthetic-redirect"))
			_, err := c.GetHTTP()
			require.Error(t, err)
			require.Contains(t, err.Error(), "different origin")
			require.NotContains(t, err.Error(), "synthetic-redirect")
		})
	}
	require.Zero(t, targetCalls, "cross-origin redirect must not be requested")
	calls := 0
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.True(t, r.Header.Get("Authorization") == "Bearer synthetic-redirect")
		if r.URL.Path != "/final" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer same.Close()
	c := NewClient(same.URL, nil)
	require.NoError(t, c.SetBearerToken("synthetic-redirect"))
	_, err := c.GetHTTP()
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestBearerTokenRejectsTLSDowngrade(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	pool := x509.NewCertPool()
	pool.AddCert(source.Certificate())
	c := NewClient(source.URL, nil)
	c.client.SetTLSClientConfig(&tls.Config{RootCAs: pool})
	require.NoError(t, c.SetBearerToken("synthetic-redirect"))
	_, err := c.GetHTTP()
	require.Error(t, err)
	require.Contains(t, err.Error(), "different origin")
	require.Zero(t, calls)
}

// Exercise the shared safe-error boundary through HTTP, server, and route APIs,
// including initialization and conditional mutation snapshot reads.
func TestBearerTokenRedirectErrorRedaction(t *testing.T) {
	const token = "synthetic-error-probe"
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/"+token)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewClient(source.URL, nil)
	require.NoError(t, c.SetBearerToken(token))
	operations := map[string]func() error{
		"initialize":  func() error { return c.EnforceExists("@config/apps/http") },
		"create HTTP": func() error { return c.CreateHTTP(HTTP{}) },
		"read HTTP":   func() error { _, err := c.GetHTTP(); return err },

		"delete HTTP":           c.DeleteHTTP,
		"create server":         func() error { _, err := c.CreateServer("edge", Server{}); return err },
		"read server":           func() error { _, err := c.GetServer("edge"); return err },
		"listen":                func() error { return c.UpdateServerListen("edge", nil) },
		"routes":                func() error { return c.UpdateServerRoutes("edge", nil) },
		"delete server":         func() error { return c.DeleteServer("edge") },
		"create route":          func() error { return c.CreateRoute("edge", Route{}) },
		"read route":            func() error { _, err := c.GetRouteByID("example"); return err },
		"servers snapshot":      func() error { _, err := c.GetServers(); return err },
		"update route snapshot": func() error { return c.UpdateRouteByID("example", Route{ID: "example"}) },
		"delete route snapshot": func() error { return c.DeleteRouteByID("example") },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			err := operation()
			require.Error(t, err)
			require.False(t, strings.Contains(err.Error(), token), "credential appeared in API error")
			require.Contains(t, err.Error(), "different origin")
		})
	}
	require.Zero(t, calls, "rejected destination must never be requested")
}

func TestBearerTokenUntrustedErrorText(t *testing.T) {
	const token = "synthetic-error-probe"
	for _, tc := range []struct {
		name, want string
		serve      http.HandlerFunc
	}{
		{"reason phrase", "401 Unauthorized", func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("cannot hijack fixture connection")
				return
			}
			defer conn.Close()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 401 rejected Bearer %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", token)
		}},
		{"invalid redirect", "request failed", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", ":"+token)
			w.WriteHeader(http.StatusTemporaryRedirect)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.serve)
			defer srv.Close()
			c := NewClient(srv.URL, nil)
			require.NoError(t, c.SetBearerToken(token))
			_, err := c.GetHTTP()
			require.Error(t, err)
			require.False(t, strings.Contains(err.Error(), token), "credential appeared in API error")
			require.Contains(t, err.Error(), tc.want)
			// Without authentication, preserve original status/transport diagnostics.
			_, err = NewClient(srv.URL, nil).GetHTTP()
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), token), "unauthenticated diagnostics changed")
		})
	}
}

func TestBearerTokenErrorRedaction(t *testing.T) {
	const token = "synthetic-echo-credential"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "rejected "+r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	require.NoError(t, c.SetBearerToken(token))
	_, err := c.GetHTTP()
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	require.Contains(t, err.Error(), "401")
}
