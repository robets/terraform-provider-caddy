package caddy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestSubrouteReadback(t *testing.T) {
	for depth := 0; depth <= 5; depth++ {
		for _, errors := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth_%d/errors_%t", depth, errors), func(t *testing.T) {
				handler := caddyapi.HandleMarshal{Handle: caddyapi.StaticResponse{Body: "nested response"}}
				for i := 0; i < depth; i++ {
					routes := []caddyapi.Route{{Handlers: []caddyapi.HandleMarshal{handler}}}
					subroute := caddyapi.Subroute{Routes: routes}
					if errors {
						subroute.Errors = &caddyapi.ServerErrors{Routes: routes}
					}
					handler = caddyapi.HandleMarshal{Handle: subroute}
				}

				for _, tc := range []struct {
					name   string
					schema map[string]*schema.Schema
				}{
					{"resource_schema", ServerRouteResource().Schema},
					{"exact_depth_schema", ServerRoute{depth}.Schema().BuildResource().Schema},
				} {
					t.Run(tc.name, func(t *testing.T) {
						d := schema.TestResourceDataRaw(t, tc.schema, nil)
						d.SetId("nested-route")
						if err := d.Set("handle", ServerRouteHandlersInto([]caddyapi.HandleMarshal{handler})); err != nil {
							t.Fatal(err)
						}
						got := ServerRouteHandlersFrom(GetObjectList(d, "handle"))
						wantJSON, err := json.Marshal([]caddyapi.HandleMarshal{handler})
						if err != nil {
							t.Fatal(err)
						}
						gotJSON, err := json.Marshal(got)
						if err != nil {
							t.Fatal(err)
						}
						if string(gotJSON) != string(wantJSON) {
							t.Fatalf("readback changed handler: got %s, want %s", gotJSON, wantJSON)
						}
						// A second read must retain the SDK's schema-derived set hashes.
						before := d.State().Attributes
						if err := d.Set("handle", ServerRouteHandlersInto(got)); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(before, d.State().Attributes) {
							t.Fatal("handler state changed on repeated read")
						}
					})
				}
			})
		}
	}
}

func TestSubrouteEmptyReadback(t *testing.T) {
	for _, subroute := range []caddyapi.Subroute{{}, {Errors: &caddyapi.ServerErrors{}}} {
		d := schema.TestResourceDataRaw(t, ServerRouteResource().Schema, nil)
		handlers := []caddyapi.HandleMarshal{{Handle: subroute}}
		if err := d.Set("handle", ServerRouteHandlersInto(handlers)); err != nil {
			t.Fatal(err)
		}
		got := ServerRouteHandlersFrom(GetObjectList(d, "handle"))
		s := got[0].Handle.(caddyapi.Subroute)
		if len(s.Routes) != 0 || (s.Errors != nil && len(s.Errors.Routes) != 0) {
			t.Fatalf("empty subroute gained routes: %#v", s)
		}
	}
}
