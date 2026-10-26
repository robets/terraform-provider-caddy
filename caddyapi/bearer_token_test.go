package caddyapi

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
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
