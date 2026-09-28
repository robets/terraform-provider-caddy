package caddy

import (
	"context"
	"fmt"
	"regexp"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/conradludgate/tfutils"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// caddyNameRegexp constrains server names and route @ids to values that are
// safe inside admin API URL paths. Caddy itself accepts ids containing
// spaces or slashes (which then break /id/ lookups), so the provider
// validates up front instead.
var caddyNameRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]*$`)

// ServerRouteResource manages a single route of an existing HTTP server
// through the Caddy admin API's @id-scoped endpoints.
//
// Creation appends the route (carrying its stable @id) to the configured
// server's routes array via a single POST; Caddy rejects a duplicate @id
// with 400, so an existing route can never be clobbered on create.
// Read/update/delete address the route by @id (GET/PATCH/DELETE /id/{id}),
// replacing it in place. The shared routes array is never read, modified
// and re-PUT as a whole, so roots can independently manage different
// routes on the same server without clobbering each other or routes owned
// by the control-plane base config.
//
// Placement semantics: creation always appends (lowest match priority, so
// control-plane base routes keep precedence); updates preserve position.
// Caddy has no non-clobbering indexed insert (POST routes/{index} also
// appends in 2.11), so no index knob is offered.
//
// The Terraform id is the route @id, which Caddy enforces unique across
// the whole config. Import by @id; the owning server is discovered on the
// first read.
func ServerRouteResource() *schema.Resource {
	sm := tfutils.SchemaMap{
		"server_name": tfutils.String().Required(true),
		"route_id":    tfutils.String().Required(true),
		"group":       tfutils.String().Optional(true),
		"terminal":    tfutils.Bool().Optional(true),
		"match":       tfutils.ListOf(ServerRouteMatcher{}).Optional(true),
		"handle":      tfutils.ListOf(ServerRouteHandler{5}).Optional(true),
	}.BuildSchemaMap()

	// tfutils cannot express ForceNew, MinItems or validation; post-process
	// the built schemas.
	sm["server_name"].ForceNew = true
	sm["server_name"].ValidateFunc = validation.StringMatch(caddyNameRegexp,
		"server names must start with a letter or digit and contain only letters, digits, '_', '-', '.'")
	sm["route_id"].ForceNew = true
	sm["route_id"].ValidateFunc = validation.StringMatch(caddyNameRegexp,
		"route ids must start with a letter or digit and contain only letters, digits, '_', '-', '.'")
	sm["handle"].MinItems = 1

	return &schema.Resource{
		Schema:   sm,
		Create:   serverRouteCreate,
		Read:     serverRouteRead,
		Update:   serverRouteUpdate,
		Delete:   serverRouteDelete,
		Importer: &schema.ResourceImporter{StateContext: serverRouteImport},
	}
}

func serverRouteCreate(d *schema.ResourceData, m interface{}) error {
	c := m.(Client)

	routeID := GetString(d, "route_id")
	route, err := serverRouteFromData(d, routeID)
	if err != nil {
		return err
	}

	if err := c.CreateRoute(GetString(d, "server_name"), route); err != nil {
		return err
	}

	d.SetId(routeID)
	return serverRouteRead(d, m)
}

func serverRouteRead(d *schema.ResourceData, m interface{}) error {
	c := m.(Client)

	routeID := d.Id()
	route, err := c.GetRouteByID(routeID)
	if err != nil {
		return err
	}
	if route == nil {
		// Remote 404: the route disappeared; drop state so the next plan
		// recreates it.
		d.SetId("")
		return nil
	}

	// After import only the @id is known; find which server holds it. A
	// route that exists but is not under any HTTP server is not something
	// this resource can own or safely delete, so refuse it loudly.
	if GetString(d, "server_name") == "" {
		server, found, err := findRouteServer(c, routeID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("route %s exists but is not registered under any HTTP server; refusing to adopt it", routeID)
		}
		if err := d.Set("server_name", server); err != nil {
			return err
		}
	}

	if err := d.Set("route_id", routeID); err != nil {
		return err
	}
	if err := d.Set("group", route.Group); err != nil {
		return err
	}
	if err := d.Set("terminal", route.Terminal); err != nil {
		return err
	}
	if err := d.Set("match", ServerRouteMatchersInto(route.Matchers)); err != nil {
		return err
	}
	return d.Set("handle", ServerRouteHandlersInto(route.Handlers))
}

func serverRouteUpdate(d *schema.ResourceData, m interface{}) error {
	c := m.(Client)

	routeID := d.Id()
	route, err := serverRouteFromData(d, routeID)
	if err != nil {
		return err
	}

	// Replace this route in place by @id; sibling routes are untouched.
	if err := c.UpdateRouteByID(routeID, route); err != nil {
		return err
	}

	return serverRouteRead(d, m)
}

func serverRouteDelete(d *schema.ResourceData, m interface{}) error {
	c := m.(Client)

	// DeleteRouteByID treats a remote 404 as success, so Delete is
	// idempotent when the route already disappeared.
	if err := c.DeleteRouteByID(d.Id()); err != nil {
		return err
	}

	d.SetId("")
	return nil
}

func serverRouteImport(_ context.Context, d *schema.ResourceData, _ interface{}) ([]*schema.ResourceData, error) {
	if !caddyNameRegexp.MatchString(d.Id()) {
		return nil, fmt.Errorf("import id must be the route @id (letters, digits, '_', '-', '.'); got %q", d.Id())
	}
	return []*schema.ResourceData{d}, nil
}

// serverRouteFromData encodes the configured route shape, guarding against
// the shared encoder's panic on malformed empty handler blocks.
func serverRouteFromData(d *schema.ResourceData, routeID string) (route caddyapi.Route, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid route configuration: %v", r)
		}
	}()

	handlers := ServerRouteHandlersFrom(GetObjectList(d, "handle"))
	if len(handlers) == 0 {
		return route, fmt.Errorf("handle: at least one handler block (e.g. static_response) is required")
	}

	return caddyapi.Route{
		ID:       routeID,
		Group:    GetString(d, "group"),
		Terminal: GetBool(d, "terminal"),
		Matchers: ServerRouteMatchersFrom(GetObjectList(d, "match")),
		Handlers: handlers,
	}, nil
}

// findRouteServer reports which HTTP server's routes array contains the
// route registered under the given @id.
func findRouteServer(c Client, routeID string) (string, bool, error) {
	servers, err := c.GetServers()
	if err != nil {
		return "", false, err
	}
	for name, server := range servers {
		for _, route := range server.Routes {
			if route.ID == routeID {
				return name, true, nil
			}
		}
	}
	return "", false, nil
}
