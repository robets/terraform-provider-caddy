package caddy

import (
	"testing"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/stretchr/testify/require"
)

func TestReverseProxyReadback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dials []string
	}{
		{name: "single", dials: []string{"127.0.0.1:8080"}},
		{name: "multiple", dials: []string{"127.0.0.1:8080", "127.0.0.1:8081"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreams := make([]caddyapi.Upstream, len(tc.dials))
			values := make([]interface{}, len(tc.dials))
			for i, dial := range tc.dials {
				upstreams[i] = caddyapi.Upstream{Dial: dial}
				values[i] = map[string]interface{}{"dial": dial}
			}
			proxy := caddyapi.ReverseProxy{Upstreams: upstreams}

			// Constructing the handler set exercises the SDK's nested-list hash.
			handler := ServerRouteHandlerInto(caddyapi.HandleMarshal{Handle: proxy})
			set := handler["reverse_proxy"].(*schema.Set)
			require.Equal(t, []interface{}{map[string]interface{}{"upstream": values}}, set.List())

			// A real resource read must replace stale state and retain list order.
			fa, client := newFakeAdmin(t)
			fa.routes["proxy-route"] = caddyapi.Route{
				ID:       "proxy-route",
				Handlers: []caddyapi.HandleMarshal{{Handle: proxy}},
			}
			fa.under["proxy-route"] = true
			res := ServerRouteResource()
			d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
				"server_name": "edge", "route_id": "proxy-route", "handle": handleConfig("stale"),
			})
			d.SetId("proxy-route")
			require.NoError(t, res.Read(d, client))
			require.Equal(t, "proxy-route", d.Id())
			require.Equal(t, "edge", d.Get("server_name"))
			handlers := d.Get("handle").([]interface{})
			require.Len(t, handlers, 1)
			state := handlers[0].(map[string]interface{})
			require.Zero(t, state["static_response"].(*schema.Set).Len())
			proxies := state["reverse_proxy"].(*schema.Set).List()
			require.Equal(t, []interface{}{map[string]interface{}{"upstream": values}}, proxies)
			roundTrip, err := serverRouteFromData(d, d.Id())
			require.NoError(t, err)
			require.Equal(t, proxy, roundTrip.Handlers[0].Handle)
		})
	}
}
