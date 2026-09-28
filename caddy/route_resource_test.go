package caddy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdmin emulates the Caddy admin API surface this resource uses:
// POST /config/apps/http/servers/{name}/routes (append, duplicate @id
// refused), GET/PATCH/DELETE /id/{id}, and GET /config/apps/http/servers.
type fakeAdmin struct {
	mu     sync.Mutex
	routes map[string]caddyapi.Route // by @id
	under  map[string]bool           // route @id -> attached to a server
	calls  []string                  // "METHOD path" in order
	srv    *httptest.Server
}

func newFakeAdmin(t *testing.T) (*fakeAdmin, *caddyapi.Client) {
	fa := &fakeAdmin{routes: map[string]caddyapi.Route{}, under: map[string]bool{}}
	fa.srv = httptest.NewServer(http.HandlerFunc(fa.handle))
	t.Cleanup(fa.srv.Close)
	return fa, caddyapi.NewClient(fa.srv.URL, nil)
}

func (fa *fakeAdmin) handle(w http.ResponseWriter, r *http.Request) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.calls = append(fa.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/config/apps/http/servers/") && strings.HasSuffix(r.URL.Path, "/routes"):
		var route caddyapi.Route
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &route); err != nil || route.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, dup := fa.routes[route.ID]; dup {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"duplicate ID '"`)
			return
		}
		fa.routes[route.ID] = route
		fa.under[route.ID] = true
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		route, ok := fa.routes[id]
		if r.Method == http.MethodGet {
			// Like real Caddy, an existing @id is addressable regardless of
			// which app owns it; the under flag only hides it from the
			// server scan.
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(route)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			b, _ := io.ReadAll(r.Body)
			var updated caddyapi.Route
			if err := json.Unmarshal(b, &updated); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fa.routes[id] = updated
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			delete(fa.routes, id)
			delete(fa.under, id)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/config/apps/http/servers":
		ids := make([]string, 0, len(fa.routes))
		for id := range fa.routes {
			if fa.under[id] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		var routes []caddyapi.Route
		for _, id := range ids {
			routes = append(routes, fa.routes[id])
		}
		_ = json.NewEncoder(w).Encode(map[string]caddyapi.Server{
			"edge": {Listen: []string{":9091"}, Routes: routes},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (fa *fakeAdmin) stored(id string) (caddyapi.Route, bool) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	r, ok := fa.routes[id]
	return r, ok
}

func handleConfig(body string) []interface{} {
	return []interface{}{map[string]interface{}{
		"static_response": []interface{}{map[string]interface{}{"body": body}},
	}}
}

func TestRouteResourceCreateReadUpdateDelete(t *testing.T) {
	fa, client := newFakeAdmin(t)
	res := ServerRouteResource()

	d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "tf-route",
		"handle":      handleConfig("R1"),
	})

	require.NoError(t, res.Create(d, client))
	assert.Equal(t, "tf-route", d.Id())
	route, ok := fa.stored("tf-route")
	require.True(t, ok, "create must insert the route under the server")
	assert.Equal(t, caddyapi.StaticResponse{Body: "R1"}, route.Handlers[0].Handle)
	assert.Contains(t, fa.calls, "POST /config/apps/http/servers/edge/routes")
	assert.NotContains(t, fa.calls, "PATCH /config/apps/http/servers/edge/routes",
		"must not read-modify-write the shared routes array")

	// Read reproduces the stored content.
	require.NoError(t, res.Read(d, client))
	assert.Equal(t, "tf-route", d.Id())
	assert.Equal(t, "edge", d.Get("server_name"))
	assert.Len(t, d.Get("handle").([]interface{}), 1)

	// Update replaces the route in place by @id.
	require.NoError(t, d.Set("handle", handleConfig("R2")))
	require.NoError(t, res.Update(d, client))
	assert.Contains(t, fa.calls, "PATCH /id/tf-route")
	route, _ = fa.stored("tf-route")
	assert.Equal(t, caddyapi.StaticResponse{Body: "R2"}, route.Handlers[0].Handle)

	// Delete removes it and is idempotent afterwards (remote 404).
	require.NoError(t, res.Delete(d, client))
	_, ok = fa.stored("tf-route")
	assert.False(t, ok)
	assert.Equal(t, "", d.Id())
	require.NoError(t, res.Delete(d, client))
}

func TestRouteResourceReadRemovesStateWhenGone(t *testing.T) {
	fa, client := newFakeAdmin(t)
	res := ServerRouteResource()

	d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "tf-route",
		"handle":      handleConfig("R1"),
	})
	require.NoError(t, res.Create(d, client))

	// Route disappears remotely (simulated external DELETE).
	fa.mu.Lock()
	delete(fa.routes, "tf-route")
	fa.mu.Unlock()

	require.NoError(t, res.Read(d, client))
	assert.Equal(t, "", d.Id(), "remote 404 must clear state")
}

func TestRouteResourceCreateDuplicateRefusedWithoutClobber(t *testing.T) {
	fa, client := newFakeAdmin(t)
	res := ServerRouteResource()

	first := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "shared-id",
		"handle":      handleConfig("owner-1"),
	})
	require.NoError(t, res.Create(first, client))

	second := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "shared-id",
		"handle":      handleConfig("owner-2"),
	})
	err := res.Create(second, client)
	require.Error(t, err, "duplicate @id must fail, not clobber")
	assert.Contains(t, err.Error(), "400")

	route, _ := fa.stored("shared-id")
	assert.Equal(t, caddyapi.StaticResponse{Body: "owner-1"}, route.Handlers[0].Handle,
		"the existing route must be untouched")
}

func TestRouteResourceHandleValidation(t *testing.T) {
	res := ServerRouteResource()

	empty := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "tf-route",
	})
	_, err := serverRouteFromData(empty, "tf-route")
	require.Error(t, err, "at least one handler block is required")

	blankBlock := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "tf-route",
		"handle":      []interface{}{map[string]interface{}{}},
	})
	_, err = serverRouteFromData(blankBlock, "tf-route")
	require.Error(t, err, "empty handle block must error, not panic")
}

func TestRouteResourceImportDiscoversServer(t *testing.T) {
	fa, client := newFakeAdmin(t)
	res := ServerRouteResource()

	seed := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
		"server_name": "edge",
		"route_id":    "tf-route",
		"handle":      handleConfig("R1"),
	})
	require.NoError(t, res.Create(seed, client))

	// Import passes only the @id through; Read must discover the server.
	d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{})
	d.SetId("tf-route")
	require.NoError(t, res.Read(d, client))
	assert.Equal(t, "edge", d.Get("server_name"))
	assert.Equal(t, "tf-route", d.Get("route_id"))

	// An @id that exists but is not under any http server is refused
	// rather than adopted (it could not be safely deleted later).
	fa.mu.Lock()
	fa.under["tf-route"] = false
	fa.mu.Unlock()
	orphan := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{})
	orphan.SetId("tf-route")
	err := res.Read(orphan, client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to adopt")
}

func TestRouteResourceImportRejectsBadID(t *testing.T) {
	res := ServerRouteResource()
	require.NotNil(t, res.Importer)

	d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{})
	d.SetId("bad id/with slash")
	_, err := res.Importer.StateContext(nil, d, nil)
	require.Error(t, err)

	ok := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{})
	ok.SetId("good_id.1-2")
	_, err = res.Importer.StateContext(nil, ok, nil)
	require.NoError(t, err)
}

func TestRouteResourceNameValidation(t *testing.T) {
	res := ServerRouteResource()

	for _, bad := range []string{"", "-lead", "has space", "a/b", "@at"} {
		_, errs := res.Schema["route_id"].ValidateFunc(bad, "route_id")
		assert.NotEmpty(t, errs, "route_id %q must be rejected", bad)
	}
	for _, good := range []string{"a", "example-route", "root-a.app_1"} {
		_, errs := res.Schema["route_id"].ValidateFunc(good, "route_id")
		assert.Empty(t, errs, "route_id %q must be accepted", good)
	}

	_, errs := res.Schema["server_name"].ValidateFunc("srv/0", "server_name")
	assert.NotEmpty(t, errs)

	assert.True(t, res.Schema["route_id"].ForceNew)
	assert.True(t, res.Schema["server_name"].ForceNew)
	assert.Equal(t, 1, res.Schema["handle"].MinItems)
}
