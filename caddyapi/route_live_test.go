package caddyapi

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Opt in with CADDY_TEST_BINARY=/absolute/path/to/stock/caddy (tested: 2.11.4).
// Every trial starts an isolated process; fixture seeding never races mutations.
func liveCaddy(t *testing.T, n int) *Client {
	t.Helper()
	binary := os.Getenv("CADDY_TEST_BINARY")
	if binary == "" {
		t.Skip("set CADDY_TEST_BINARY for stock Caddy integration tests")
	}
	addr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		a := l.Addr().String()
		require.NoError(t, l.Close())
		return a
	}
	admin, edge := addr(), addr()
	routes := []map[string]interface{}{}
	for i := 0; i < n; i++ {
		for _, prefix := range []string{"own", "foreign"} {
			id := fmt.Sprintf("%s-%d", prefix, i)
			routes = append(routes, map[string]interface{}{"@id": id, "handle": []map[string]interface{}{{"handler": "static_response", "body": id}}})
		}
	}
	cfg := map[string]interface{}{"admin": map[string]interface{}{"listen": admin}, "apps": map[string]interface{}{"http": map[string]interface{}{"servers": map[string]interface{}{"edge": map[string]interface{}{"listen": []string{edge}, "routes": routes}}}}}
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "caddy-route-test-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	body, err := json.Marshal(cfg)
	require.NoError(t, err)
	config := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(config, body, 0600))
	log, err := os.Create(filepath.Join(dir, "caddy.log"))
	require.NoError(t, err)
	cmd := exec.Command(binary, "run", "--config", config)
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir)
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
			t.Log(string(b))
		}
	})
	c := NewClient("http://"+admin, nil)
	c.client.SetTimeout(10 * time.Second)
	for i := 0; i < 200; i++ {
		r, e := c.client.R().Get(routeServersPath)
		if e == nil && r.StatusCode() == 200 {
			return c
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Caddy admin did not become ready")
	return nil
}

func liveRoutes(t *testing.T, c *Client) map[string]json.RawMessage {
	t.Helper()
	resp, err := c.client.R().Get(routeServersPath + "/edge/routes")
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode())
	var routes []json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Body(), &routes))
	result := map[string]json.RawMessage{}
	for _, route := range routes {
		var identity struct {
			ID string `json:"@id"`
		}
		require.NoError(t, json.Unmarshal(route, &identity))
		require.NotContains(t, result, identity.ID)
		result[identity.ID] = route
	}
	return result
}

func TestRouteMutationLiveETag(t *testing.T) {
	c := liveCaddy(t, 2)
	before := liveRoutes(t, c)
	snapshot, err := c.client.R().Get(routeServersPath)
	require.NoError(t, err)
	require.Equal(t, 200, snapshot.StatusCode())
	tag := snapshot.Header().Get("ETag")
	require.Regexp(t, routeServersETag, tag)
	t.Logf("stock Caddy servers ETag: %s", tag)
	// A tag for the whole servers object is accepted on a child mutation.
	r, err := c.client.R().SetHeader("If-Match", tag).Delete(routeServersPath + "/edge/routes/0")
	require.NoError(t, err)
	require.Equal(t, 200, r.StatusCode())
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		req := c.client.R().SetHeader("If-Match", tag)
		if method == http.MethodPatch {
			req.SetBody(Route{ID: "own-1"})
		}
		r, err = req.Execute(method, routeServersPath+"/edge/routes/0")
		require.NoError(t, err)
		require.Equal(t, 412, r.StatusCode())
	}
	after := liveRoutes(t, c)
	require.JSONEq(t, string(before["foreign-0"]), string(after["foreign-0"]))
	require.JSONEq(t, string(before["foreign-1"]), string(after["foreign-1"]))
	require.NoError(t, c.UpdateRouteByID("own-1", Route{ID: "own-1", Group: "updated"}))
	require.NoError(t, c.DeleteRouteByID("own-1"))
	require.NoError(t, c.DeleteRouteByID("own-1"))
	require.Error(t, c.UpdateRouteByID("own-1", Route{ID: "own-1"}))
	t.Log("stale parent-container tag rejected both DELETE and PATCH with 412; fresh provider mutations and idempotent deletion passed")
}

func TestRouteMutationLiveOverlap(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for trial := 0; trial < 10; trial++ {
			t.Run(fmt.Sprintf("mixed=%v/trial=%d", mixed, trial), func(t *testing.T) {
				const n = 32
				c := liveCaddy(t, n)
				before := liveRoutes(t, c)
				errors := make([]error, n)
				start := make(chan struct{})
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-start
						// Separate clients (as with separate provider processes); no shared mutex.
						client := NewClient(c.client.HostURL, nil)
						client.client.SetTimeout(10 * time.Second)
						id := fmt.Sprintf("own-%d", i)
						if mixed && i%2 == 0 {
							errors[i] = client.UpdateRouteByID(id, Route{ID: id, Group: "updated", Handlers: []HandleMarshal{{Handle: StaticResponse{Body: "updated-" + id}}}})
						} else {
							errors[i] = client.DeleteRouteByID(id)
						}
					}(i)
				}
				close(start)
				wg.Wait()
				after := liveRoutes(t, c)
				successes, conflicts, ambiguous := 0, 0, 0
				for i, err := range errors {
					foreign := fmt.Sprintf("foreign-%d", i)
					require.Contains(t, after, foreign)
					require.JSONEq(t, string(before[foreign]), string(after[foreign]))
					own := fmt.Sprintf("own-%d", i)
					if err == nil {
						successes++
						if mixed && i%2 == 0 {
							require.Contains(t, after, own)
							var r Route
							require.NoError(t, json.Unmarshal(after[own], &r))
							require.Equal(t, "updated", r.Group)
						} else {
							require.NotContains(t, after, own, "delete reported success but target remains")
						}
					} else if strings.Contains(err.Error(), "retry budget exhausted") {
						conflicts++
						require.Contains(t, after, own)
					} else {
						// Caddy can close admin connections during reload. Count these
						// as returned errors, never as success or convergence. Safety
						// assertions use actual route bodies, not these errors.
						ambiguous++
						var transportError *url.Error
						require.True(t, stderrors.As(err, &transportError), "unexpected non-transport error: %v", err)
						if mixed && i%2 == 0 {
							require.Contains(t, after, own)
						}
						t.Logf("admin transport failure: target=%s error=%v present=%v", own, err, after[own] != nil)
					}
				}
				require.Equal(t, n, successes+conflicts+ambiguous)
				require.Greater(t, successes, 0, "a trial with no successful mutations proves nothing")
				for id := range after {
					require.Contains(t, before, id, "unexpected route identity")
				}
				t.Logf("own outcomes: success=%d exhausted-412=%d ambiguous=%d; all %d foreign route bodies preserved", successes, conflicts, ambiguous, n)
			})
		}
	}
}

// Force a real 412 between the fixed client's GET and mutation, without a
// probabilistic scheduling dependency or changing Caddy. This transport only
// injects a second independent client's successful removal of own-0.
type conflictTransport struct {
	base      http.RoundTripper
	inject    func()
	once      sync.Once
	conflicts int
}

func (tr *conflictTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodDelete || r.Method == http.MethodPatch {
		tr.once.Do(tr.inject)
	}
	resp, err := tr.base.RoundTrip(r)
	if resp != nil && resp.StatusCode == 412 {
		tr.conflicts++
	}
	return resp, err
}
func TestRouteMutationLiveInjectedConflict(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			c := liveCaddy(t, 2)
			before := liveRoutes(t, c)
			other := NewClient(c.client.HostURL, nil)
			tr := &conflictTransport{base: c.client.GetClient().Transport, inject: func() { require.NoError(t, other.DeleteRouteByID("own-0")) }}
			c.client.SetTransport(tr)
			var err error
			if method == http.MethodDelete {
				err = c.DeleteRouteByID("own-1")
			} else {
				err = c.UpdateRouteByID("own-1", Route{ID: "own-1", Group: "updated"})
			}
			require.NoError(t, err)
			require.Equal(t, 1, tr.conflicts)
			after := liveRoutes(t, c)
			for _, id := range []string{"foreign-0", "foreign-1"} {
				require.JSONEq(t, string(before[id]), string(after[id]))
			}
			if method == http.MethodDelete {
				require.NotContains(t, after, "own-1")
			} else {
				require.Contains(t, after, "own-1")
				require.Contains(t, string(after["own-1"]), `"group":"updated"`)
			}
			t.Log("real 412 observed once; fixed client refetched and addressed moved target safely")
		})
	}
}
