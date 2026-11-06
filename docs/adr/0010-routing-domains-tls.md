# ADR-0010: Routing, networks, local domains and TLS

Status: accepted 2026-11-06 (Gate B part 3). Amended 2026-11-06 by ADR-0015: the dashboard has a host of its own.

## Context

- Spec 13 sets these requirements:
  - Nginx generates routing from deployment records. Compare that (generated config plus reload) with Traefik or Caddy.
  - Generated domains are `<app>.cloudforge.localhost` and `pr-<n>-<app>.cloudforge.localhost`.
  - Local HTTPS uses a local CA (mkcert or Caddy's).
  - Let's Encrypt only once a real domain exists (M8). Compare HTTP-01 and DNS-01.
  - Custom domains are verified with a DNS TXT record and need renewal handling.
  - App containers are never exposed directly.
- Spec 5.2.8, open to challenge at Gate B: one shared Docker network, with Nginx reaching apps by container name.
- Spec 5.2.9: use `*.cloudforge.localhost` and document a dnsmasq fallback.
- ADR-0005 (accepted): the agent owns its node's proxy config.

## Evidence

- Gate A, section 4:
  - Nginx reload semantics.
  - `proxy_pass` with a variable resolves the name at request time through `resolver`.
  - Traefik's Docker provider reads the Docker API through the mounted socket.
  - Caddy's `POST /load` swaps config with zero downtime, and Caddy has its own local CA.
  - Runbot renders, diffs and sends `SIGHUP`.
- `[RAN]` A container on a user-defined network gets `nameserver 127.0.0.11` (Docker's embedded DNS). This closes a Gate A open item.
- `[RAN]` On this host, `getent hosts app.cloudforge.localhost` and `getent hosts pr-1-app.cloudforge.localhost` both return `::1`.
- `[SOURCE]` mkcert warns that "many browsers don't support second-level wildcards like *.localhost". It also reminds that "X.509 wildcards only go one level deep" (mkcert `cert.go:149-160`, BSD-3-Clause, commit 1c1dc4e).
- `[DOCS]` Let's Encrypt: HTTP-01 "cannot be used to issue wildcard certificates"; DNS-01 can (https://letsencrypt.org/docs/challenge-types/).
- `[DOCS]` Bridge network option `com.docker.network.bridge.enable_icc`: "Enable or Disable inter-container connectivity" (https://docs.docker.com/engine/network/drivers/bridge/).

## Options

Proxy:

1. Nginx with a generated config and reload.
2. Traefik, with its Docker provider or its file provider.
3. Caddy, through its JSON config API.

Network for app containers:

- A. One shared apps network (spec 5.2.8).
- B. One shared network with inter-container traffic disabled.
- C. One network per environment, with Nginx attached to all of them.

## Decision

Nginx (option 1), and one network per environment (option C).

### Routing

- The agent renders one file from its desired state:
  - a `map $host $cf_upstream` from hostname to `<container>:<port>`;
  - one `server` block with `proxy_pass http://$cf_upstream`;
  - `resolver 127.0.0.11 valid=5s`.

  Unknown hosts get a 404 page.
- To apply a change, the agent:
  1. writes the file;
  2. runs `nginx -t` inside the Nginx container;
  3. reloads Nginx;
  4. reports the route generation as applied (ADR-0005).

  If the test fails, the agent restores the old file and reports the error.
- Because `proxy_pass` uses a variable, the container's IP is looked up per request, so a container restarted with a new IP needs no reload.
- In development, Nginx publishes its ports on `127.0.0.1` and `::1` only. It needs `::1` because this host resolves `*.localhost` to `::1` first (see above).

### Networks

- **Creation:** for each environment, the agent creates a Docker network `cf-env-<id>`, with a /27 subnet taken from a configurable CloudForge /16.
- **Members:**
  - Nginx, which joins every environment network;
  - that environment's app containers (blue and green);
  - its preview database (M7).
- **Effect:** an app cannot reach another environment's containers, including a preview reaching production. It can only go through Nginx by public hostname, like any client.
- **Egress rules** for these networks are in ADR-0012.

### Hostnames

- **Dashboard:** `cloudforge-console.localhost`, which is a different site from the apps (ADR-0015). No app is ever served there.
- **Owner:** `<app>` is the project slug, and each hostname belongs to exactly one environment (`domain.hostname` is unique). The first project to claim a hostname keeps it; a second project with the same slug must pick another subdomain.
- **Preview names stay free:** slugs that start with `pr-<digits>-` are rejected, so no project can take a preview hostname.
- **Length:** slugs are at most 50 characters, so `pr-<n>-<slug>` stays inside the 63-character limit for a DNS label.
- **Why `pr-<n>-<app>` and not `pr-<n>.<app>`:** a wildcard matches exactly one label (mkcert's reminder above). So one certificate for `*.cloudforge.localhost` covers both production and preview hostnames.

### Local DNS

- Browsers, and this host's resolver, send `*.localhost` to loopback.
- Inside a container, `*.localhost` means that container's own loopback. So tests that run in containers must set the `Host` header explicitly.
- **dnsmasq fallback** (documented, not run), for tools that query DNS directly:
  - Run a dnsmasq container on a spare loopback address, for example `127.0.0.2:53`, with `address=/cloudforge.localhost/127.0.0.1` and `address=/cloudforge-console.localhost/127.0.0.1`.
  - Point the tool's resolver at it.

### TLS

- **Until M8:** plain HTTP on `*.cloudforge.localhost`.
- **M8 local HTTPS with mkcert:**
  - mkcert runs on the host, because it must install its root CA into the system and browser trust stores.
  - It issues one certificate for `cloudforge-console.localhost` (the dashboard) and `*.cloudforge.localhost` (the apps).
  - Only that certificate and its key are mounted into Nginx.
  - The CA key (`rootCA-key.pem`) never enters a container or the repository: anyone who holds it can impersonate any website to this machine's browsers.
- **M8 public HTTPS, only with a real domain:**
  - DNS-01 for the wildcard covering generated hostnames, because HTTP-01 can't issue wildcards.
  - HTTP-01 for each custom domain, with Nginx serving `/.well-known/acme-challenge/` from a shared directory.
  - The ACME client (lego or certbot, as a container) is chosen in M8.
  - The Worker renews a certificate 30 days before it expires, and the agent reloads Nginx.
- **Custom domains:**
  1. The user adds a TXT record `_cloudforge-verify.<domain>` containing the domain's `verification_token`.
  2. The Worker checks the record and sets `verified_at`.
  3. Only then does it request a certificate.

  An unverified domain is never routed.

### Two nodes (M9)

`*.localhost` always points at node 1. So node 1's Nginx stays the single entry point and forwards to node 2's Nginx. Decided in M9.

## Why

- **Nginx:**
  - It's the spec's default, and Runbot works the same way.
  - You already know it from Odoo deployments, so your time goes into the routing logic.
  - A reload is atomic per config, and old workers finish the requests they already have (Gate A).
- **Traefik:**
  - Its Docker provider puts the Docker socket inside the internet-facing process, which goes against spec 16.
  - With the file provider, it is the same idea as Nginx in a different syntax.
- **Caddy:**
  - It would mean less code in M8: built-in ACME, renewal, an internal CA and on-demand certificates.
  - The cost is that it hides the TLS work the spec wants you to be able to explain.
  - If M8's ACME work proves too heavy, Caddy is the swap. Only the agent's renderer changes.
- **Network options:**
  - A lets every app reach every other app, which breaks spec 16 and spec 12 (a preview reaching production).
  - B blocks Nginx as well, so apps would need host ports.
  - C uses plain Docker features.

## Consequences

- **Amends (applied 2026-11-06):**
  - overview: the Nginx row becomes "per-environment networks" instead of "shared apps network";
  - ADR-0005, step 2: the container joins its environment's network.
- **Explicit subnets:** `[INFERRED]` Docker's default address pools only allow a few dozen networks. Explicit subnets from our own range avoid that limit. The range must not overlap the LAN or a VPN, so it is a setting.
- **Idle keep-alive race:** `[INFERRED]` after a reload, old workers close idle keep-alive connections, and a client that sends a request at that exact moment can get a reset. The M6 zero-failure test measures this. A fix, if needed, is designed in M6.
- **Long-lived connections:** SSE and WebSocket connections to an old container break when it stops at the end of the drain grace period (D5).
