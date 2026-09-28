#!/usr/bin/env bash
# Contract battery for the caddy_server_route resource, run against a
# disposable Caddy (the same pinned image the foundation build uses) plus
# the local OpenTofu client via dev_overrides. It proves the properties the
# @id-scoped design exists for: create/read/empty plan/update/delete/import,
# remote disappearance re-creation, duplicate @id refusal, preservation of
# foreign and control-plane routes across provider updates (no clobber),
# and two independent roots sharing one server. See README.md.
set -euo pipefail

: "${CADDY_IMAGE:=caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d}"
: "${SUFFIX:=routedev}"
: "${TOFU_BIN:=tofu}"

work=$(mktemp -d "/tmp/caddy-route-$SUFFIX.XXXXXX")
ctr=route-provider-$SUFFIX
admin=127.0.0.1:21100
edge=127.0.0.1:21101
failures=0

cleanup() {
  local status=$?
  trap - EXIT
  docker rm -f "$ctr" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

fail() {
  printf 'route-contract FAIL: %s\n' "$*" >&2
  failures=$((failures + 1))
}

expect() {
  local want=$1 got=$2 name=$3
  [[ "$want" == "$got" ]] || fail "$name: want [$want] got [$got]"
}

admin_code() { # method path [json]
  local m=$1 p=$2 b=${3:-}
  if [[ -n $b ]]; then
    curl -s -o /dev/null -w '%{http_code}' -X "$m" \
      -H 'Content-Type: application/json' --data "$b" "http://$admin$p"
  else
    curl -s -o /dev/null -w '%{http_code}' -X "$m" "http://$admin$p"
  fi
}

body() { curl -s "http://$admin$1"; }
serve() { curl -s "http://$edge$1"; }
order() { body /config/apps/http/servers/edge/routes | jq -c '[.[]."@id"]'; }
tofu_in() {
  local dir=$1
  shift
  (cd "$work/$dir" && TF_CLI_CONFIG_FILE=$work/rc.tf TF_PLUGIN_CACHE_DIR=$work/tfcache "$TOFU_BIN" "$@")
}

# --- build provider + dev override config ---------------------------------
mkdir -p "$work/bin" "$work/tfcache" "$work/api" "$work/rootA" "$work/rootB" "$work/rootImport"
go build -o "$work/bin/terraform-provider-caddy" .

cat > "$work/rc.tf" <<EOF
provider_installation {
  dev_overrides {
    "conradludgate/caddy" = "$work/bin"
  }
  direct {}
}
EOF

# --- disposable Caddy with a control-plane-owned base route ----------------
cat > "$work/api/edge.json" <<'EOF'
{"admin":{"listen":"0.0.0.0:2019"},
 "apps":{"http":{"servers":{"edge":{"listen":[":9091"],"routes":[
   {"@id":"cp-base","match":[{"path":["/cp-route"]}],"handle":[{"handler":"static_response","body":"cp-base"}]}]}}}}}
EOF
docker rm -f "$ctr" >/dev/null 2>&1 || true
docker run -d --name "$ctr" \
  -p "$admin:2019" -p "$edge:9091" \
  -v "$work/api/edge.json:/etc/caddy/edge.json:ro" \
  "$CADDY_IMAGE" caddy run --config /etc/caddy/edge.json --resume >/dev/null
for _ in $(seq 1 30); do
  [[ $(admin_code GET /config/) == 200 ]] && break
  sleep 0.5
done
expect 200 "$(admin_code GET /config/)" "disposable Caddy admin up"

# --- roots ------------------------------------------------------------------
for d in rootA rootB rootImport; do
  cat > "$work/$d/main.tf" <<'EOF'
terraform {
  required_providers {
    caddy = { source = "conradludgate/caddy" }
  }
}
provider "caddy" {
  host = "http://127.0.0.1:21100"
}
EOF
done
cat >> "$work/rootA/main.tf" <<'EOF'
variable "body" {
  type    = string
  default = "RA1"
}

resource "caddy_server_route" "a" {
  server_name = "edge"
  route_id    = "tf-route-a"
  match {
    path = ["/tf-a"]
  }
  handle {
    static_response {
      body = var.body
    }
  }
}

resource "caddy_server_route" "b" {
  server_name = "edge"
  route_id    = "tf-route-b"
  match {
    path = ["/tf-b"]
  }
  handle {
    static_response {
      body = "RB1"
    }
  }
}
EOF
cat >> "$work/rootB/main.tf" <<'EOF'
variable "body" {
  type    = string
  default = "RC1"
}

resource "caddy_server_route" "c" {
  server_name = "edge"
  route_id    = "tf-route-c"
  match {
    path = ["/tf-c"]
  }
  handle {
    static_response {
      body = var.body
    }
  }
}
EOF
cat >> "$work/rootImport/main.tf" <<'EOF'
resource "caddy_server_route" "c" {
  server_name = "edge"
  route_id    = "tf-route-c"
  match {
    path = ["/tf-c"]
  }
  handle {
    static_response {
      body = "RC2"
    }
  }
}
EOF

# --- 1. create + serve + control-plane route intact --------------------------
expect 0 "$(tofu_in rootA apply -input=false -auto-approve >/dev/null 2>&1; echo $?)" "rootA create"
expect 200 "$(admin_code GET /id/tf-route-a)" "created route-a readable by @id"
expect RA1 "$(serve /tf-a)" "route-a serves"
expect RB1 "$(serve /tf-b)" "route-b serves"
expect cp-base "$(serve /cp-route)" "control-plane base route untouched"

# --- 2. empty plan -----------------------------------------------------------
expect 0 "$(tofu_in rootA plan -detailed-exitcode -input=false >/dev/null 2>&1; echo $?)" "empty plan after create"

# --- 3. independent second root ---------------------------------------------
expect 0 "$(tofu_in rootB apply -input=false -auto-approve >/dev/null 2>&1; echo $?)" "rootB create"
expect RC1 "$(serve /tf-c)" "rootB route serves"
expect 0 "$(tofu_in rootA plan -detailed-exitcode -input=false >/dev/null 2>&1; echo $?)" "rootA plan still empty after rootB"
expect 0 "$(tofu_in rootB plan -detailed-exitcode -input=false >/dev/null 2>&1; echo $?)" "rootB empty plan"
expect '["cp-base","tf-route-a","tf-route-b","tf-route-c"]' "$(order | jq -c '.[0:1] + (.[1:] | sort)')" "append-only placement keeps base route first"
created_order=$(order)

# --- 4. foreign route must survive provider updates (no clobber) ------------
expect 200 "$(admin_code POST /config/apps/http/servers/edge/routes \
  '{"@id":"foreign-x","match":[{"path":["/foreign"]}],"handle":[{"handler":"static_response","body":"FX"}]}')" \
  "foreign route added out of band"
expect 0 "$(tofu_in rootA apply -input=false -auto-approve -var body=RA2 >/dev/null 2>&1; echo $?)" "rootA update"
expect RA2 "$(serve /tf-a)" "route-a updated"
expect RB1 "$(serve /tf-b)" "sibling route untouched by update"
expect RC1 "$(serve /tf-c)" "other root untouched by rootA update"
expect FX "$(serve /foreign)" "foreign route survives provider update (no clobber)"
expect "$created_order" "$(order | jq -c ".[0:-1]")" "update preserved route order"
expect "foreign-x" "$(order | jq -r ".[-1]")" "foreign route appended"

# --- 5. cross-root independence on update ------------------------------------
expect 0 "$(tofu_in rootB apply -input=false -auto-approve -var body=RC2 >/dev/null 2>&1; echo $?)" "rootB update"
expect RC2 "$(serve /tf-c)" "rootB route updated"
expect RA2 "$(serve /tf-a)" "rootA route untouched by rootB update"

# --- 6. disappearance: remote delete is detected and recreated ---------------
expect 200 "$(admin_code DELETE /id/tf-route-b)" "simulate external deletion"
expect 404 "$(admin_code GET /id/tf-route-b)" "route gone remotely"
expect 2 "$(tofu_in rootA plan -detailed-exitcode -input=false -var body=RA2 >/dev/null 2>&1; echo $?)" "plan detects disappearance"
expect 0 "$(tofu_in rootA apply -input=false -auto-approve -var body=RA2 >/dev/null 2>&1; echo $?)" "apply recreates disappeared route"
expect RB1 "$(serve /tf-b)" "recreated route serves"
expect 0 "$(tofu_in rootA plan -detailed-exitcode -input=false -var body=RA2 >/dev/null 2>&1; echo $?)" "empty plan after recreation"

# --- 7. import by @id yields an empty plan -----------------------------------
expect 0 "$(tofu_in rootImport import -input=false caddy_server_route.c tf-route-c >/dev/null 2>&1; echo $?)" "import by @id"
expect 0 "$(tofu_in rootImport plan -detailed-exitcode -input=false >/dev/null 2>&1; echo $?)" "empty plan after import (read fidelity)"
expect 1 "$(tofu_in rootImport import -input=false caddy_server_route.c no-such-route >/dev/null 2>&1; echo $?)" "import of missing route fails"

# --- 8. delete is idempotent: destroy after the route already vanished --------
expect 0 "$(tofu_in rootImport destroy -input=false -auto-approve >/dev/null 2>&1; echo $?)" "rootImport destroy removes route-c"
expect 404 "$(admin_code GET /id/tf-route-c)" "route-c gone"
expect 0 "$(tofu_in rootB destroy -input=false -auto-approve >/dev/null 2>&1; echo $?)" "destroy is idempotent after remote disappearance"

# --- 9. final: provider routes gone, foreign + control-plane intact ----------
expect 0 "$(tofu_in rootA destroy -input=false -auto-approve >/dev/null 2>&1; echo $?)" "rootA destroy"
expect 404 "$(admin_code GET /id/tf-route-a)" "route-a deleted"
expect 404 "$(admin_code GET /id/tf-route-b)" "route-b deleted"
expect cp-base "$(serve /cp-route)" "control-plane route survives destroy"
expect FX "$(serve /foreign)" "foreign route survives destroy"
expect '["edge"]' "$(body /config/apps/http/servers/ | jq -c 'keys')" "server itself untouched"
expect '["cp-base","foreign-x"]' "$(order)" "only non-provider routes remain"

if ((failures != 0)); then
  printf 'route-contract: %d assertion(s) failed\n' "$failures" >&2
  exit 1
fi
printf 'route-contract: all assertions passed for %s\n' "$SUFFIX"
