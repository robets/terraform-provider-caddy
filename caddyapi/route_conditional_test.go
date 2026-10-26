package caddyapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const serversPath = "/config/apps/http/servers"
const snapshot = `{"edge":{"routes":[{"@id":"foreign","handle":[{"handler":"unknown_plugin"}]},{"@id":"tf-route"}]}}`
const snapshotTag = `"/config/apps/http/servers 0123456789abcdef"`

func TestRouteMutationConditional(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			reads, writes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					reads++
					assert.Equal(t, serversPath, r.URL.Path)
					w.Header().Set("ETag", snapshotTag)
					io.WriteString(w, snapshot)
					return
				}
				writes++
				assert.Equal(t, method, r.Method)
				assert.Equal(t, serversPath+"/edge/routes/1", r.URL.Path)
				assert.Equal(t, snapshotTag, r.Header.Get("If-Match"))
				if method == http.MethodPatch {
					body, _ := io.ReadAll(r.Body)
					assert.Contains(t, string(body), `"@id":"tf-route"`)
				}
			}))
			defer srv.Close()
			c := NewClient(srv.URL, nil)
			var err error
			if method == http.MethodDelete {
				err = c.DeleteRouteByID("tf-route")
			} else {
				err = c.UpdateRouteByID("tf-route", Route{ID: "tf-route"})
			}
			require.NoError(t, err)
			require.Equal(t, 1, reads)
			require.Equal(t, 1, writes)
		})
	}
}
