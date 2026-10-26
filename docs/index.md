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

For an admin endpoint configured to accept bearer tokens, set
`CADDY_BEARER_TOKEN` in the provider process environment and configure its URL:

```hcl
provider "caddy" {
  host = "https://localhost:2020"
}
```

The optional, sensitive `bearer_token` argument overrides `CADDY_BEARER_TOKEN`.
An explicit `bearer_token = ""` disables bearer authentication, even when the
environment variable is set. If the argument is omitted and the environment
variable is unset or empty, no Authorization header is sent. Nonempty tokens
must contain only visible ASCII characters without whitespace; no JWT format
is required.

The token is sent on every API request. The provider does not issue or renew
tokens or configure the endpoint to accept them. Use HTTPS for remote endpoints;
normal certificate and hostname verification remain enabled. Bearer tokens also
work over Unix sockets and SSH, without replacing SSH authentication or host-key
verification. Authenticated redirects must keep the same scheme, host, and port.

Prefer environment input over embedding tokens in HCL. Sensitive values are
masked in normal CLI output, not encrypted. Protect environment access, saved
plans, state, and logs; avoid debug/trace logging with real tokens. The provider
does not copy its token into managed resource state, but configuration variables
and outputs can still persist it.

Authenticated errors omit response bodies, server reason phrases, redirect URLs,
and raw request error details to avoid exposing credentials. HTTP status codes
and common redirect, timeout, cancellation, or TLS failures are still reported.

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

- **bearer_token** (String, Sensitive) Token for [bearer authentication](#bearer-token-authentication).
- **host** (String) http or unix socket to act as the caddy admin API endpoint
- **ssh** (Block Set) (see [below for nested schema](#nestedblock--ssh))

<a id="nestedblock--ssh"></a>
### Nested Schema for `ssh`

SSH Configuration to proxy caddy API access through. Recommended if accessing caddy API remotely as caddy provides no authentication

Required:

- **host** (String)
- **host_key** (String)
- **key_file** (String)
