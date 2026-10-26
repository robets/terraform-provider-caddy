package caddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

func configuredBearerClient(t *testing.T, config map[string]interface{}) Client {
	t.Helper()
	p := Provider()
	require.NoError(t, p.InternalValidate())
	diags := p.Configure(context.Background(), terraform.NewResourceConfigRaw(config))
	require.False(t, diags.HasError(), "provider configure failed")
	return p.Meta().(Client)
}

func TestProviderBearerToken(t *testing.T) {
	for _, tc := range []struct {
		name, env, explicit, want string
		supplied                  bool
	}{
		{name: "explicit", explicit: "synthetic-explicit", want: "Bearer synthetic-explicit", supplied: true},
		{name: "environment", env: "synthetic-env", want: "Bearer synthetic-env"},
		{name: "precedence", env: "synthetic-env", explicit: "synthetic-explicit", want: "Bearer synthetic-explicit", supplied: true},
		{name: "opaque non-JWT", explicit: "synthetic:/+==!._~-", want: "Bearer synthetic:/+==!._~-", supplied: true},
		{name: "explicit overrides invalid environment", env: "synthetic\ninvalid", explicit: "synthetic-explicit", want: "Bearer synthetic-explicit", supplied: true},
		{name: "unset"},
		{name: "empty overrides environment", env: "synthetic-env", supplied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CADDY_BEARER_TOKEN", tc.env)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				// Never print credential values on a failure.
				if r.Header.Get("Authorization") != tc.want {
					t.Error("unexpected authorization header")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			config := map[string]interface{}{"host": srv.URL}
			if tc.supplied {
				config["bearer_token"] = tc.explicit
			}
			c := configuredBearerClient(t, config)
			_, err := c.GetHTTP()
			require.NoError(t, err)
			require.NoError(t, c.CreateHTTP(caddyapi.HTTP{HTTPPort: 8080}))
			require.NoError(t, c.UpdateServerRoutes("edge", []caddyapi.Route{}))
			require.NoError(t, c.DeleteHTTP())
			require.GreaterOrEqual(t, calls, 4)
		})
	}
	require.True(t, Provider().Schema["bearer_token"].Sensitive)
}

func TestProviderBearerTokenInvalid(t *testing.T) {
	for _, token := range []string{"synthetic\r\nInjected: value", "synthetic\x00", "synthetic token", "synthetic\t", "synthetic\x7f"} {
		t.Run("invalid", func(t *testing.T) {
			t.Setenv("CADDY_BEARER_TOKEN", "")
			p := Provider()
			d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"bearer_token": token})
			_, err := providerConfigurer(d)
			require.Error(t, err)
			require.NotContains(t, err.Error(), token)
		})
	}
	t.Run("invalid environment", func(t *testing.T) {
		const token = "synthetic\r\ninvalid"
		t.Setenv("CADDY_BEARER_TOKEN", token)
		p := Provider()
		diags := p.Configure(context.Background(), terraform.NewResourceConfigRaw(map[string]interface{}{}))
		require.True(t, diags.HasError())
		for _, diagnostic := range diags {
			require.NotContains(t, diagnostic.Summary+diagnostic.Detail, token)
		}
	})
}
