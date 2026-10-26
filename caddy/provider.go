package caddy

import (
	"github.com/conradludgate/tfutils"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Provider for caddy
func Provider() *schema.Provider {
	p := tfutils.Provider{
		Schema: tfutils.SchemaMap{
			"host": tfutils.String().Default("http://localhost:2019"),
			"ssh": tfutils.SchemaMap{
				"host":     tfutils.String().Required(true),
				"key_file": tfutils.String().Required(true),
				"host_key": tfutils.String().Required(true),
			}.IntoSet().Optional(true).MaxItems(1),
		},
		Resources: tfutils.ResourceMap{
			"caddy_http":   HTTP{},
			"caddy_server": Server{},
		},
		DataSources: tfutils.DataSourceMap{
			"caddy_server_route": ServerRoute{5},
		},
		ConfigureFunc: providerConfigurer,
	}.Build()

	// caddy_server_route (resource) needs ForceNew, validation, MinItems
	// and an importer, which tfutils cannot express, so it is registered
	// as a hand-assembled *schema.Resource built from the shared tfutils
	// match/handle structures.
	p.ResourcesMap["caddy_server_route"] = ServerRouteResource()

	return p
}
