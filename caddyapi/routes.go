package caddyapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Route represents the Caddy Route object
// https://caddyserver.com/docs/json/apps/http/servers/routes/
type Route struct {
	ID       string          `json:"@id,omitempty"`
	Group    string          `json:"group,omitempty"`
	Matchers []Match         `json:"match,omitempty"`
	Handlers []HandleMarshal `json:"handle,omitempty"`
	Terminal bool            `json:"terminal,omitempty"`
}

// CreateRoute appends the route to the routes array of the named HTTP
// server via POST /config/apps/http/servers/{server}/routes. The route's
// @id makes it independently addressable afterwards; a duplicate @id is
// rejected by Caddy with 400 so an existing route can never be clobbered
// on create. The whole routes array is never read-modified and re-written.
func (c *Client) CreateRoute(serverName string, route Route) error {
	resp, err := c.client.R().SetBody(route).Post(URLFromID("@config/apps/http/servers/" + serverName + "/routes"))
	if err != nil {
		return fmt.Errorf("CreateRoute: %w", c.safeError(err))
	}
	if resp.IsError() {
		return fmt.Errorf("CreateRoute: %w", StatusError{resp})
	}
	return nil
}

// GetRouteByID fetches the route registered under the given @id via
// GET /id/{id}. A nil route with a nil error means the route no longer
// exists remotely (404).
func (c *Client) GetRouteByID(id string) (*Route, error) {
	resp, err := c.client.R().SetResult(&Route{}).Get(URLFromID(id))
	if err != nil {
		return nil, fmt.Errorf("GetRouteByID: %w", c.safeError(err))
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil, nil
	}
	if resp.IsError() {
		return nil, fmt.Errorf("GetRouteByID: %w", StatusError{resp})
	}
	return resp.Result().(*Route), nil
}

// UpdateRouteByID replaces one top-level route in place, conditionally on a
// snapshot containing its identity and position. See mutateRouteByID.
func (c *Client) UpdateRouteByID(id string, route Route) error {
	if route.ID != id || id == "" {
		return fmt.Errorf("UpdateRouteByID: route identity must remain %q", id)
	}
	return c.mutateRouteByID(http.MethodPatch, id, &route)
}

// DeleteRouteByID conditionally removes one top-level route. Absence in the
// snapshot is success; mutation errors (including ambiguous transport errors)
// are returned, not retried blindly.
func (c *Client) DeleteRouteByID(id string) error {
	if id == "" {
		return fmt.Errorf("DeleteRouteByID: empty route identity")
	}
	return c.mutateRouteByID(http.MethodDelete, id, nil)
}

const routeMutationAttempts = 8
const routeServersPath = "/config/apps/http/servers"

// Caddy's strong ETag contains the configuration path and a 64-bit xxhash.
// Never send an empty, weak, wildcard, wrong-path, or malformed precondition.
var routeServersETag = regexp.MustCompile(`^"/config/apps/http/servers [0-9a-f]{16}"$`)

// mutateRouteByID deliberately avoids /id/: Caddy resolves IDs before taking
// its mutation lock, so another client's deletion can shift the resolved index.
// The servers snapshot includes every parent routes array and its ancestry.
// Caddy checks its ETag under the same lock as the single-index mutation.
// Only an explicit 412 is safe to retry, always with a new snapshot and index.
func (c *Client) mutateRouteByID(method, id string, route *Route) error {
	for attempt := 0; attempt < routeMutationAttempts; attempt++ {
		resp, err := c.client.R().Get(routeServersPath)
		if err != nil {
			return fmt.Errorf("%s route %q snapshot: %w", method, id, c.safeError(err))
		}
		if resp.IsError() {
			return fmt.Errorf("%s route %q snapshot: %w", method, id, StatusError{resp})
		}
		tag := resp.Header().Get("ETag")
		if !routeServersETag.MatchString(tag) {
			return fmt.Errorf("%s route %q: missing or invalid servers ETag", method, id)
		}
		// Decode identity only: foreign/plugin handlers need not fit our model.
		var servers map[string]struct {
			Routes []struct {
				ID string `json:"@id"`
			} `json:"routes"`
		}
		if err := json.Unmarshal(resp.Body(), &servers); err != nil {
			return fmt.Errorf("%s route %q snapshot: %w", method, id, c.safeError(err))
		}
		target := ""
		for name, server := range servers {
			for i, candidate := range server.Routes {
				if candidate.ID == id {
					if target != "" || name == "." || name == ".." || strings.Contains(name, "/") {
						return fmt.Errorf("%s route %q: ambiguous or unaddressable target", method, id)
					}
					target = fmt.Sprintf("%s/%s/routes/%d", routeServersPath, url.PathEscape(name), i)
				}
			}
		}
		if target == "" {
			if method == http.MethodDelete {
				return nil
			}
			return fmt.Errorf("%s route %q: not found in top-level server routes", method, id)
		}
		req := c.client.R().SetHeader("If-Match", tag)
		if route != nil {
			req.SetBody(*route)
		}
		resp, err = req.Execute(method, target)
		if err != nil {
			return fmt.Errorf("%s route %q: %w", method, id, c.safeError(err))
		}
		if resp.StatusCode() == http.StatusPreconditionFailed {
			continue
		}
		if resp.IsError() {
			return fmt.Errorf("%s route %q: %w", method, id, StatusError{resp})
		}
		return nil
	}
	return fmt.Errorf("%s route %q: conflict retry budget exhausted after %d attempts", method, id, routeMutationAttempts)
}

// GetServers fetches the map of HTTP servers with their inline routes.
// It is used to discover which server owns a route when importing.
func (c *Client) GetServers() (map[string]Server, error) {
	resp, err := c.client.R().SetResult(&map[string]Server{}).Get(URLFromID("@config/apps/http/servers"))
	if err != nil {
		return nil, fmt.Errorf("GetServers: %w", c.safeError(err))
	}
	if resp.IsError() {
		return nil, fmt.Errorf("GetServers: %w", StatusError{resp})
	}
	return *(resp.Result().(*map[string]Server)), nil
}
