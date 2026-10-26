package caddyapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerTokenRedirectPolicyLifecycle(t *testing.T) {
	for _, policy := range []string{"default", "custom allow", "custom refuse"} {
		t.Run(policy, func(t *testing.T) {
			targetHeaders := make(chan http.Header, 4)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetHeaders <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer target.Close()
			sourceHeaders := make(chan http.Header, 12)
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceHeaders <- r.Header.Clone()
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			c := NewClient(source.URL, nil)
			policyCalls := 0
			if policy != "default" {
				c.client.GetClient().CheckRedirect = func(req *http.Request, via []*http.Request) error {
					policyCalls++
					if policy == "custom refuse" {
						return http.ErrUseLastResponse
					}
					return nil
				}
			}
			// Clearing an already unauthenticated client must not discard its policy.
			require.NoError(t, c.SetBearerToken(""))
			for cycle := 0; cycle < 2; cycle++ {
				for _, token := range []string{"synthetic-first", "synthetic-replacement"} {
					require.NoError(t, c.SetBearerToken(token))
					require.Error(t, c.SetBearerToken("synthetic-invalid\r\ninjected"))
					_, err := c.GetHTTP()
					require.Error(t, err)
					require.Contains(t, err.Error(), "different origin")
					require.NotContains(t, err.Error(), token)
					require.Equal(t, "Bearer "+token, (<-sourceHeaders).Get("Authorization"))
					require.Empty(t, targetHeaders, "authenticated redirect reached destination")
				}
				require.NoError(t, c.SetBearerToken(""))
				require.NoError(t, c.SetBearerToken(""))
				_, err := c.GetHTTP()
				require.NoError(t, err, "original redirect behavior must be restored")
				require.NotContains(t, <-sourceHeaders, "Authorization")
				if policy == "custom refuse" {
					require.Empty(t, targetHeaders, "custom policy must still refuse redirects")
				} else {
					require.Len(t, targetHeaders, 1)
					require.NotContains(t, <-targetHeaders, "Authorization")
				}
				if policy != "default" {
					require.Equal(t, cycle+1, policyCalls, "restore the actual original policy, not an auth wrapper")
				}
			}
		})
	}
}

func TestBearerTokenInvalidPreservesEstablishedState(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"whitespace", "synthetic invalid"},
		{"header injection", "synthetic\r\nInjected: value"},
		{"control character", "synthetic\x00"},
		{"delete character", "synthetic\x7f"},
		{"non ASCII", "synthetic\xc2\xa0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(chan http.Header, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Clone()
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("rejected " + r.Header.Get("Authorization")))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, nil)
			require.NoError(t, c.SetBearerToken("synthetic-established"))
			err := c.SetBearerToken(tc.token)
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.token)
			_, err = c.GetHTTP()
			require.Error(t, err)
			require.Contains(t, err.Error(), "401 Unauthorized")
			require.NotContains(t, err.Error(), "synthetic-established")
			require.Equal(t, "Bearer synthetic-established", (<-headers).Get("Authorization"))
		})
	}
}

func TestBearerTokenLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens []string
	}{
		{"clear after request", []string{"synthetic-first", ""}},
		{"replace token", []string{"synthetic-first", "synthetic-second"}},
		{"repeated set and clear", []string{"", "synthetic-first", "synthetic-second", "", "", "synthetic-third", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(chan http.Header, len(tc.tokens))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, nil)
			for _, token := range tc.tokens {
				require.NoError(t, c.SetBearerToken(token))
				_, err := c.GetHTTP()
				require.NoError(t, err)
				header := <-headers
				if token == "" {
					require.NotContains(t, header, "Authorization", "clearing must remove the header entirely")
				} else {
					require.Equal(t, []string{"Bearer " + token}, header.Values("Authorization"))
				}
			}
		})
	}
}
