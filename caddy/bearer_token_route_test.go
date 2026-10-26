package caddy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/stretchr/testify/require"
)

func TestProviderBearerRouteRetry(t *testing.T) {
	t.Setenv("CADDY_BEARER_TOKEN", "synthetic-route-credential")
	fa, _ := newFakeAdmin(t)
	reads, writes, rejected := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-route-credential" {
			rejected++
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/config/apps/http/servers" {
			reads++
		}
		if r.Method == "PATCH" {
			writes++
			if writes == 1 {
				w.WriteHeader(412)
				return
			}
		}
		fa.handle(w, r)
	}))
	defer srv.Close()
	c := configuredBearerClient(t, map[string]interface{}{"host": srv.URL})
	res := ServerRouteResource()
	d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge", "route_id": "example-route", "handle": handleConfig("first"),
	})
	require.NoError(t, res.Create(d, c))
	require.NoError(t, res.Read(d, c))
	require.NoError(t, d.Set("handle", handleConfig("second")))
	require.NoError(t, res.Update(d, c))
	require.NoError(t, res.Delete(d, c))
	require.Zero(t, rejected)
	require.GreaterOrEqual(t, reads, 3)
	require.Equal(t, 2, writes)
}
