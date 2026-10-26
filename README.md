# terraform-provider-caddy

This is a terraform provider to manage the [caddy api](https://caddyserver.com/).

## Setup

First you will need caddy running with the admin api enabled. This provider supports two methods to accesing the API endpoint

### HTTP Endpoint

The simplest is to just use the default endpoint `http://localhost:2019`.
This is the default caddy uses and the default that this provider will use too.

However, this is not recommended as it is not secure.

### Bearer-token authentication

For an admin endpoint that accepts bearer authentication, set
`CADDY_BEARER_TOKEN` in the provider process environment and configure its HTTPS URL:

```hcl
provider "caddy" {
  host = "https://localhost:2020"
}
```

The optional, sensitive `bearer_token` provider argument takes precedence over
`CADDY_BEARER_TOKEN`, including an explicit empty string (which disables bearer
authentication). Omitted input with an unset or empty environment variable sends
no Authorization header. Tokens must contain visible ASCII characters without
whitespace; no JWT format or issuer is required. The provider sends
`Authorization: Bearer <token>` on all API requests, including reads and retries.
It does not issue, renew, or configure server-side acceptance of tokens.

Use HTTPS for remote endpoints: standard certificate and hostname verification
remains enabled. Bearer authentication also works with existing Unix/SSH
transports; it does not replace SSH authentication or host-key checking. When a
token is configured, redirects to a different scheme, host, or port are rejected
before the redirected request is sent. Same-origin redirects remain supported.

Prefer supplying the token through the environment rather than embedding it in
HCL. `Sensitive` masks ordinary CLI presentation; it is **not encryption or magic
secret storage**. Tokens placed in variables, saved plans, outputs, or other
configuration can still be persisted by Terraform/OpenTofu. Protect environment
access, plan/state files and logs, and avoid debug/trace logging with real tokens.
Provider credentials are not copied into managed resource state. Authenticated
HTTP error diagnostics omit response bodies to avoid disclosing echoed tokens.

A local-only HTTPS/OpenTofu regression test uses synthetic credentials and a
temporary trusted certificate (requires Go and `tofu`, no registry installation):

```sh
CADDY_BEARER_TOFU_TEST=1 go test ./caddy -run '^TestBearerTokenOpenTofu$' -v -count=1
```

### Unix Sockets

The recommended method is to use unix sockets. Modify your caddy config to use the admin endpoint `unix//path/to/admin.sock`.
Next, run caddy (preferrably with `-resume` and in the background. If you have systemd, check out the caddy-api service file).

Once caddy is running, it should create the unix socket at the path specified. Test it by running the following (making sure you have permission to access the socket)

```sh
curl -H "Host: " --unix-socket /path/to/admin.sock http://localhost/config/
```

If you don't get an error, all is good to go! Finally, set up the provider like so

```tf
provider "caddy" {
  host = "unix:///path/to/admin.sock
}
```

### SSH

In addition to using any of the two above methods to connect to the admin API, you can proxy the request through SSH to ensure authorized access over the internet.
Ensure you can SSH into the server where Caddy is running, and that user can access the admin endpoint, the add the following to your provider config

```tf
provider "caddy" {
  host = "unix:///path/to/admin.sock
  ssh = {
    host = "user@example.com:22" // port is required
    key_file = "~/.ssh/id_rsa" // or specify a password in the host field 'user:pass@example.com:22'
    host_key = "example.com ssh_rsa ...." // in 'known_hosts' format.
  }
}
```

## Manage an individual HTTP route

`caddy_server_route` owns one route on an existing HTTP server. It appends a
route with a globally unique Caddy `@id`, then reads, updates, and deletes only
that ID. Other routes on the server are not replaced. Routes created this way
follow existing server routes; use an explicit path or host matcher to avoid
catching unrelated traffic.

```hcl
resource "caddy_server_route" "app" {
  server_name = "edge"
  route_id    = "tofu-app"

  match {
    path = ["/app/*"]
  }
  handle {
    reverse_proxy {
      upstream {
        dial = "app:8080"
      }
    }
  }
}
```

`server_name` and `route_id` are immutable. Import an existing route by its
`@id`, for example `tofu import caddy_server_route.app tofu-app`; the provider
finds its owning HTTP server during refresh. The route must already exist under
an HTTP server, and its handlers must use shapes supported by this provider.
A remote 404 clears state on refresh; deletion is idempotent.

Update and delete read `/config/apps/http/servers`, find the exact top-level
`@id`, and send a single-index PATCH or DELETE with that snapshot's strong
ETag in `If-Match`. Caddy checks this precondition under its mutation lock,
so a concurrent deletion cannot shift the index onto another owner's route.
The servers-container tag guards the parent routes array and its ancestry;
it intentionally also conflicts with changes to other HTTP servers. Foreign
handler JSON is inspected only for route identity, not decoded by the provider.
Only 412 conflicts are retried, with fresh identity/index/tag reads, for at most
eight attempts. Missing or malformed tags and exhausted conflicts fail closed.
This requires Caddy's native path-bearing ETag semantics (tested with 2.11.4).

A missing target in the snapshot makes deletion idempotent; update reports an
error. Refresh still treats `/id/{id}` 404 as disappearance. A 404 on a snapshot
or concrete mutation request is an error, not evidence that the ID is absent.
Other HTTP and transport failures are returned without retry: an EOF/reset
can leave the write outcome unknown and requires inspection before recovery.
This protects distinct route owners from index-shift mutations, not from full
configuration replacement, deliberate reuse of the same ID, or administrator
access. It does not promise all operations succeed under contention or admin
reload connection failures. No process-local mutex is required.

Run the live route contract against disposable Caddy and OpenTofu with
`bash tests/route-contract.sh` (requires Docker, OpenTofu, `curl`, and `jq`).
Run the focused concurrent-mutation regression tests against a local stock Caddy
binary with `CADDY_TEST_BINARY=/path/to/caddy go test ./caddyapi -run '^TestRouteMutationLive' -count=1 -v`.
They assert every foreign route body and each successful own operation, force a
real 412 for both PATCH and DELETE, and report exhausted conflicts and transport
errors separately. Foreign preservation is not an all-writes-converged result.
