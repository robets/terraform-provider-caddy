package caddy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/stretchr/testify/require"
)

func TestProviderBearerTokenErrorDiagnostics(t *testing.T) {
	const token = "synthetic-diagnostic-probe"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("cleared token leaked to redirect destination")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthenticated diagnostic " + token))
	}))
	defer target.Close()
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
		{"redirect URL", "different origin", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL+"/"+token)
			w.WriteHeader(http.StatusTemporaryRedirect)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.serve)
			defer srv.Close()
			c := configuredBearerClient(t, map[string]interface{}{"host": srv.URL, "bearer_token": token})
			r := Provider().ResourcesMap["caddy_http"]
			d := schema.TestResourceDataRaw(t, r.Schema, nil)
			d.SetId("@config/apps/http")
			_, diags := r.RefreshWithoutUpgrade(context.Background(), d.State(), c)
			require.True(t, diags.HasError())
			for _, diagnostic := range diags {
				text := diagnostic.Summary + diagnostic.Detail
				require.False(t, strings.Contains(text, token), "credential appeared in provider diagnostic")
				require.Contains(t, text, tc.want)
			}
			// Exercise disabling authentication on this same configured client,
			// not a fresh provider with an initially empty token.
			require.NoError(t, c.(interface{ SetBearerToken(string) error }).SetBearerToken(""))
			_, diags = r.RefreshWithoutUpgrade(context.Background(), d.State(), c)
			require.True(t, diags.HasError())
			for _, diagnostic := range diags {
				text := diagnostic.Summary + diagnostic.Detail
				require.Contains(t, text, token, "cleared client should restore unauthenticated diagnostics")
			}
		})
	}
}
