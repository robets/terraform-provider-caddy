package caddyapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteMutationTransportErrorIsNotRetried(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		for _, failRead := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/read=%v", method, failRead), func(t *testing.T) {
				var mu sync.Mutex
				reads, writes := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Method == http.MethodGet {
						reads++
					} else {
						writes++
					}
					if (r.Method == http.MethodGet) == failRead {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							conn.Close()
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("ETag", snapshotTag)
					io.WriteString(w, snapshot)
				}))
				defer srv.Close()
				c := NewClient(srv.URL, nil)
				var err error
				if method == http.MethodDelete {
					err = c.DeleteRouteByID("tf-route")
				} else {
					err = c.UpdateRouteByID("tf-route", Route{ID: "tf-route"})
				}
				mu.Lock()
				defer mu.Unlock()
				require.Error(t, err)
				assert.Equal(t, 1, reads)
				expected := 1
				if failRead {
					expected = 0
				}
				assert.Equal(t, expected, writes)
			})
		}
	}
}

func TestRouteMutationDisappearedAfterConflict(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			reads, writes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					reads++
					w.Header().Set("ETag", snapshotTag)
					if reads == 1 {
						io.WriteString(w, snapshot)
					} else {
						io.WriteString(w, `{}`)
					}
					return
				}
				writes++
				w.WriteHeader(412)
			}))
			defer srv.Close()
			c := NewClient(srv.URL, nil)
			if method == http.MethodDelete {
				require.NoError(t, c.DeleteRouteByID("tf-route"))
			} else {
				require.Error(t, c.UpdateRouteByID("tf-route", Route{ID: "tf-route"}))
			}
			assert.Equal(t, 2, reads)
			assert.Equal(t, 1, writes)
		})
	}
}

func TestRouteMutationConflictRefetch(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPatch} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exhausted=%v", method, exhausted), func(t *testing.T) {
				reads, writes := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet {
						reads++
						w.Header().Set("ETag", fmt.Sprintf(`"/config/apps/http/servers %016x"`, reads))
						if reads == 1 {
							io.WriteString(w, snapshot)
						} else {
							io.WriteString(w, `{"edge":{"routes":[{"@id":"tf-route"}]}}`)
						}
						return
					}
					writes++
					assert.Equal(t, method, r.Method)
					assert.Equal(t, fmt.Sprintf(`"/config/apps/http/servers %016x"`, reads), r.Header.Get("If-Match"))
					index := 0
					if reads == 1 {
						index = 1
					}
					assert.Equal(t, fmt.Sprintf("%s/edge/routes/%d", serversPath, index), r.URL.Path)
					if exhausted || writes == 1 {
						w.WriteHeader(412)
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
				if exhausted {
					require.Error(t, err)
					assert.Contains(t, err.Error(), "retry budget exhausted")
					assert.Equal(t, 8, writes)
				} else {
					require.NoError(t, err)
					assert.Equal(t, 2, writes)
				}
				assert.Equal(t, writes, reads)
			})
		}
	}
}

func TestRouteMutationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, tag, body         string
		readStatus, writeStatus int
		writes                  int
		wantError               bool
	}{
		{"missing-tag", "", snapshot, 200, 200, 0, true},
		{"weak-tag", `W/` + snapshotTag, snapshot, 200, 200, 0, true},
		{"wildcard", "*", snapshot, 200, 200, 0, true},
		{"wrong-path", `"/config/apps/http/servers/edge/routes/1 0123456789abcdef"`, snapshot, 200, 200, 0, true},
		{"bad-hash", `"/config/apps/http/servers xyz"`, snapshot, 200, 200, 0, true},
		{"bad-json", snapshotTag, `{`, 200, 200, 0, true},
		{"wrong-shape", snapshotTag, `[]`, 200, 200, 0, true},
		{"snapshot-500", snapshotTag, snapshot, 500, 200, 0, true},
		{"snapshot-404", snapshotTag, snapshot, 404, 200, 0, true},
		{"mutation-500", snapshotTag, snapshot, 200, 500, 1, true},
		{"mutation-404", snapshotTag, snapshot, 200, 404, 1, true},
		{"mutation-400", snapshotTag, snapshot, 200, 400, 1, true},
		{"absent", snapshotTag, `{"edge":{"routes":[{"@id":"foreign"}]}}`, 200, 200, 0, false},
		{"no-servers", snapshotTag, `null`, 200, 200, 0, false},
		{"duplicate-id", snapshotTag, `{"edge":{"routes":[{"@id":"tf-route"},{"@id":"tf-route"}]}}`, 200, 200, 0, true},
	} {
		for _, method := range []string{http.MethodDelete, http.MethodPatch} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				reads, writes := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet {
						reads++
						w.Header().Set("ETag", tc.tag)
						w.WriteHeader(tc.readStatus)
						io.WriteString(w, tc.body)
						return
					}
					writes++
					w.WriteHeader(tc.writeStatus)
				}))
				defer srv.Close()
				c := NewClient(srv.URL, nil)
				var err error
				if method == http.MethodDelete {
					err = c.DeleteRouteByID("tf-route")
				} else {
					err = c.UpdateRouteByID("tf-route", Route{ID: "tf-route"})
				}
				if tc.wantError || (tc.writes == 0 && method == http.MethodPatch) {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, 1, reads)
				assert.Equal(t, tc.writes, writes)
			})
		}
	}
}

func TestRouteMutationRejectsEmptyDeleteIdentity(t *testing.T) {
	c, rec := stubServer(t, 200, "")
	require.Error(t, c.DeleteRouteByID(""))
	require.Empty(t, rec.Method, "invalid identity must not issue a request")
}

func TestRouteMutationRejectsChangedIdentity(t *testing.T) {
	c, _ := stubServer(t, 200, "")
	err := c.UpdateRouteByID("tf-route", Route{ID: "other"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity")
}
