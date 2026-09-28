package caddyapi

import (
	"fmt"
	"net/http"
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
		return fmt.Errorf("CreateRoute: %w", err)
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
		return nil, fmt.Errorf("GetRouteByID: %w", err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil, nil
	}
	if resp.IsError() {
		return nil, fmt.Errorf("GetRouteByID: %w", StatusError{resp})
	}
	return resp.Result().(*Route), nil
}

// UpdateRouteByID replaces the route registered under the given @id in
// place via PATCH /id/{id}, preserving its position in the routes array
// and leaving every other route untouched. (POST /id/{id} is not used:
// Caddy treats posting an already-registered @id as a duplicate ID and
// answers 400.)
func (c *Client) UpdateRouteByID(id string, route Route) error {
	resp, err := c.client.R().SetBody(route).Patch(URLFromID(id))
	if err != nil {
		return fmt.Errorf("UpdateRouteByID: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("UpdateRouteByID: %w", StatusError{resp})
	}
	return nil
}

// DeleteRouteByID removes the route registered under the given @id via
// DELETE /id/{id}. A missing route (404) is not an error, so Delete is
// idempotent.
func (c *Client) DeleteRouteByID(id string) error {
	resp, err := c.client.R().Delete(URLFromID(id))
	if err != nil {
		return fmt.Errorf("DeleteRouteByID: %w", err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	if resp.IsError() {
		return fmt.Errorf("DeleteRouteByID: %w", StatusError{resp})
	}
	return nil
}

// GetServers fetches the map of HTTP servers with their inline routes.
// It is used to discover which server owns a route when importing.
func (c *Client) GetServers() (map[string]Server, error) {
	resp, err := c.client.R().SetResult(&map[string]Server{}).Get(URLFromID("@config/apps/http/servers"))
	if err != nil {
		return nil, fmt.Errorf("GetServers: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("GetServers: %w", StatusError{resp})
	}
	return *(resp.Result().(*map[string]Server)), nil
}
