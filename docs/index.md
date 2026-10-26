---
page_title: "caddy Provider"
subcategory: ""
description: |-
  
---

# caddy Provider

# terraform-provider-caddy

This is a terraform provider to manage the [caddy api](https://caddyserver.com/).

## Setup

First you will need caddy running with the admin api enabled. This provider supports two methods to accesing the API endpoint

### HTTP Endpoint

The simplest is to just use the default endpoint `http://localhost:2019`.
This is the default caddy uses and the default that this provider will use too.

However, this is not recommended as it is not secure.

### Bearer-token authentication

For an admin endpoint that accepts arbitrary supplied bearer tokens, set
`CADDY_BEARER_TOKEN` in the provider process environment:

```hcl
provider "caddy" {
  host = "https://localhost:2020"
}
```

Alternatively, set the optional sensitive `bearer_token` provider argument.
Explicit input takes precedence over the environment, including an explicit
empty string, which disables authentication. Omitted input and an unset/empty
environment variable preserve unauthenticated behavior. Nonempty tokens must
contain visible ASCII characters without whitespace; no JWT or issuer-specific
format is required. The header is sent on every API request, including snapshot
reads and conditional retries. Token issuance, renewal, and server-side token
acceptance are outside the provider's scope.

Use HTTPS for remote endpoints; normal certificate and hostname verification
remain enabled. Existing Unix/SSH transports are unchanged. Authenticated
redirects to a different scheme, host, or port are rejected before sending the
redirected request; same-origin redirects are allowed.

Prefer environment input rather than putting credentials in HCL. `Sensitive`
masks normal CLI presentation; it is not encryption or magic secret storage.
Variables, saved plans, outputs, state and debug/trace logs may still persist
secrets; protect them and avoid debug/trace logging with real tokens. The
provider does not copy its token into managed resource state. Authenticated HTTP
error diagnostics include only the method, numeric status, and standard status
text. Request failures report known redirect, timeout, cancellation, or TLS
classifications and otherwise omit raw error details. Response bodies, server
reason phrases, and redirect URLs are not included because they may echo
credentials. Unauthenticated diagnostics are unchanged.

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

## Schema

### Optional

- **bearer_token** (String, Sensitive) Optional bearer token. Defaults to `CADDY_BEARER_TOKEN` when omitted; explicit input wins, and an explicit empty string disables bearer authentication.
- **host** (String) http or unix socket to act as the caddy admin API endpoint
- **ssh** (Block Set) (see [below for nested schema](#nestedblock--ssh))

<a id="nestedblock--ssh"></a>
### Nested Schema for `ssh`

SSH Configuration to proxy caddy API access through. Recommended if accessing caddy API remotely as caddy provides no authentication

Required:

- **host** (String)
- **host_key** (String)
- **key_file** (String)
