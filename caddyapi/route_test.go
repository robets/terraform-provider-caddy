package caddyapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedCall struct {
	Method string
	Path   string
	Body   string
}

// stubServer returns a client pointed at a single-response stub plus a
// pointer to the recorded call.
func stubServer(t *testing.T, status int, body string) (*Client, *recordedCall) {
	rec := &recordedCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*rec = recordedCall{Method: r.Method, Path: r.URL.Path, Body: string(b)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, nil), rec
}

const routeBody = `{"@id":"tf-route","group":"g","handle":[{"handler":"static_response","body":"R1"}],"match":[{"path":["/x"]}]}`

func TestCreateRoute(t *testing.T) {
	c, rec := stubServer(t, 200, "")
	require.NoError(t, c.CreateRoute("edge", Route{ID: "tf-route", Handlers: []HandleMarshal{{Handle: StaticResponse{Body: "R1"}}}}))
	assert.Equal(t, "POST", rec.Method)
	assert.Equal(t, "/config/apps/http/servers/edge/routes", rec.Path)
	assert.Contains(t, rec.Body, `"@id":"tf-route"`)
	assert.Contains(t, rec.Body, `"handler":"static_response"`)
}

func TestCreateRouteDuplicateRejected(t *testing.T) {
	c, _ := stubServer(t, 400, `{"error":"duplicate ID"}`)
	err := c.CreateRoute("edge", Route{ID: "tf-route"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestGetRouteByID(t *testing.T) {
	c, rec := stubServer(t, 200, routeBody)
	route, err := c.GetRouteByID("tf-route")
	require.NoError(t, err)
	require.NotNil(t, route)
	assert.Equal(t, "GET", rec.Method)
	assert.Equal(t, "/id/tf-route", rec.Path)
	assert.Equal(t, "tf-route", route.ID)
	assert.Equal(t, "g", route.Group)
	require.Len(t, route.Matchers, 1)
	assert.Equal(t, []string{"/x"}, route.Matchers[0].Path)
	require.Len(t, route.Handlers, 1)
	assert.Equal(t, StaticResponse{Body: "R1"}, route.Handlers[0].Handle)
}

func TestGetRouteByIDMissingIsNil(t *testing.T) {
	c, _ := stubServer(t, 404, `{"error":"not found"}`)
	route, err := c.GetRouteByID("gone")
	require.NoError(t, err)
	assert.Nil(t, route)
}

func TestGetRouteByIDError(t *testing.T) {
	c, _ := stubServer(t, 500, "")
	_, err := c.GetRouteByID("x")
	assert.Error(t, err)
}

func TestUpdateRouteByID(t *testing.T) {
	c, rec := stubServer(t, 200, "")
	require.NoError(t, c.UpdateRouteByID("tf-route", Route{ID: "tf-route", Handlers: []HandleMarshal{{Handle: StaticResponse{Body: "R2"}}}}))
	assert.Equal(t, "PATCH", rec.Method)
	assert.Equal(t, "/id/tf-route", rec.Path)
	assert.Contains(t, rec.Body, `"@id":"tf-route"`)
	assert.Contains(t, rec.Body, `"R2"`)
}

func TestUpdateRouteByIDMissingIsError(t *testing.T) {
	c, _ := stubServer(t, 404, `{"error":"no such id"}`)
	assert.Error(t, c.UpdateRouteByID("gone", Route{ID: "gone"}))
}

func TestDeleteRouteByID(t *testing.T) {
	c, rec := stubServer(t, 200, "")
	require.NoError(t, c.DeleteRouteByID("tf-route"))
	assert.Equal(t, "DELETE", rec.Method)
	assert.Equal(t, "/id/tf-route", rec.Path)
}

func TestDeleteRouteIDMissingIsIdempotent(t *testing.T) {
	c, _ := stubServer(t, 404, `{"error":"not found"}`)
	assert.NoError(t, c.DeleteRouteByID("gone"))
}

func TestDeleteRouteByIDError(t *testing.T) {
	c, _ := stubServer(t, 500, "")
	assert.Error(t, c.DeleteRouteByID("x"))
}

func TestGetServers(t *testing.T) {
	c, rec := stubServer(t, 200, `{"edge":{"listen":[":9091"],"routes":[{"@id":"tf-route","handle":[{"handler":"static_response","body":"R1"}]}]}}`)
	servers, err := c.GetServers()
	require.NoError(t, err)
	assert.Equal(t, "GET", rec.Method)
	assert.Equal(t, "/config/apps/http/servers", rec.Path)
	require.Contains(t, servers, "edge")
	assert.Equal(t, []string{":9091"}, servers["edge"].Listen)
	require.Len(t, servers["edge"].Routes, 1)
	assert.Equal(t, "tf-route", servers["edge"].Routes[0].ID)
}
