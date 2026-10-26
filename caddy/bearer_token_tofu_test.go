package caddy

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/conradludgate/terraform-provider-caddy/caddyapi"
	"github.com/stretchr/testify/require"
)

// Opt-in, local-only end-to-end test: CADDY_BEARER_TOFU_TEST=1 go test ./caddy -run TestBearerTokenOpenTofu -v
// Requires Go and OpenTofu; no provider registry, containers, or external API.
func TestBearerTokenOpenTofu(t *testing.T) {
	if os.Getenv("CADDY_BEARER_TOFU_TEST") != "1" {
		t.Skip("set CADDY_BEARER_TOFU_TEST=1 to run local HTTPS/OpenTofu test")
	}
	tofu, err := exec.LookPath("tofu")
	require.NoError(t, err)
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "terraform-provider-caddy"), ".")
	build.Dir = ".."
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	const token = "synthetic-tofu-credential"
	fa := &fakeAdmin{routes: map[string]caddyapi.Route{}, under: map[string]bool{}}
	var mu sync.Mutex
	rejected, snapshots, conflicts, authed := 0, 0, 0, 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			rejected++
			// Deliberately echo to ensure diagnostics do not disclose credentials.
			http.Error(w, "rejected "+r.Header.Get("Authorization"), http.StatusUnauthorized)
			return
		}
		authed++
		if r.Method == "GET" && r.URL.Path == "/config/apps/http/servers" {
			snapshots++
		}
		if r.Method == "PATCH" && conflicts == 0 {
			conflicts++
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		fa.handle(w, r)
	}))
	defer srv.Close()
	ca := filepath.Join(work, "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600))
	rc := filepath.Join(work, "tofurc")
	require.NoError(t, os.WriteFile(rc, []byte(fmt.Sprintf(`provider_installation {
 dev_overrides { "conradludgate/caddy" = %q }
 direct {}
}
`, bin)), 0600))
	config := fmt.Sprintf(`terraform {
 required_providers { caddy = { source = "conradludgate/caddy" } }
}
provider "caddy" { host = %q }
variable "body" { default = "first" }
resource "caddy_server_route" "example" {
 server_name = "edge"
 route_id = "example-route"
 handle {
  static_response { body = var.body }
 }
}
output "route_id" { value = caddy_server_route.example.id }
`, srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(work, "main.tf"), []byte(config), 0600))
	run := func(credential, trust string, args ...string) (string, error) {
		cmd := exec.Command(tofu, args...)
		cmd.Dir = work
		for _, entry := range os.Environ() {
			key := strings.SplitN(entry, "=", 2)[0]
			if strings.HasPrefix(key, "TF_") || strings.HasPrefix(key, "TOFU_") || key == "CADDY_BEARER_TOKEN" || key == "SSL_CERT_FILE" {
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "TF_CLI_CONFIG_FILE="+rc, "TF_IN_AUTOMATION=1", "CADDY_BEARER_TOKEN="+credential, "SSL_CERT_FILE="+trust)
		b, err := cmd.CombinedOutput()
		require.NotContains(t, string(b), token, "credential in CLI output")
		require.NotContains(t, string(b), "synthetic-wrong", "credential in CLI output")
		return string(b), err
	}
	outText, err := run(token, ca, "apply", "-auto-approve", "-input=false", "-no-color")
	require.NoError(t, err, outText)
	outText, err = run(token, ca, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	require.NoError(t, err, outText)
	outText, err = run(token, ca, "apply", "-auto-approve", "-input=false", "-no-color", "-var=body=second")
	require.NoError(t, err, outText)
	outText, err = run("synthetic-wrong", ca, "plan", "-input=false", "-no-color")
	require.Error(t, err)
	require.Contains(t, outText, "401")
	outText, err = run("", ca, "plan", "-input=false", "-no-color")
	require.Error(t, err)
	require.Contains(t, outText, "401")
	outText, err = run(token, filepath.Join(work, "untrusted.pem"), "plan", "-input=false", "-no-color")
	require.Error(t, err)
	require.Contains(t, outText, "certificate signed by unknown authority")
	state, err := os.ReadFile(filepath.Join(work, "terraform.tfstate"))
	require.NoError(t, err)
	require.NotContains(t, string(state), token)
	require.NotContains(t, string(state), "bearer_token")
	outText, err = run(token, ca, "output", "-json")
	require.NoError(t, err, outText)
	require.Contains(t, outText, "example-route")
	outText, err = run(token, ca, "destroy", "-auto-approve", "-input=false", "-no-color")
	require.NoError(t, err, outText)
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, rejected, 2)
	require.GreaterOrEqual(t, snapshots, 3, "update conflict must re-read snapshot; delete also reads")
	require.Equal(t, 1, conflicts)
	require.Greater(t, authed, 0)
	t.Log("verified HTTPS trust, authenticated CRUD and conflict retry, wrong/missing token rejection, untrusted CA rejection, and no provider credential in resource state or outputs")
}
