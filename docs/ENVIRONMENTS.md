# Environments

Where Xibalba runs and what stands in front of it. For each environment there
is a file in [`examples/`](../examples) that you copy and adapt. This page
says what each one does and how far it is tested.

Xibalba is one program that listens on a port, passes requests on to your
website, and needs one piece of information from whatever stands in front of
it: the visitor's real address. Everything below is a variation of that.

## Overview

| Environment | Example | Tested |
|---|---|---|
| nginx | [`examples/nginx/xibalba.conf`](../examples/nginx/xibalba.conf) | Yes, with nginx 1.24 |
| Caddy | [`examples/caddy/Caddyfile`](../examples/caddy/Caddyfile) | Yes, with Caddy 2.6 |
| Apache | [`examples/apache/xibalba.conf`](../examples/apache/xibalba.conf) | Yes, with Apache 2.4.58 |
| HAProxy | [`examples/haproxy/haproxy.cfg`](../examples/haproxy/haproxy.cfg) | Yes, with HAProxy 2.8 |
| Traefik | [`examples/traefik/`](../examples/traefik) | Yes, with Traefik 3.1 |
| Docker, Docker Compose | [`Dockerfile`](../Dockerfile), [`examples/docker/`](../examples/docker) | Built and started by the project's CI at every change |
| systemd | [`examples/systemd/xibalba.service`](../examples/systemd/xibalba.service) | Checked with `systemd-analyze verify`; not started under systemd |
| Kubernetes | [`examples/kubernetes/xibalba.yaml`](../examples/kubernetes/xibalba.yaml) | No. The configuration in it is checked; it was not run in a cluster. |
| Behind Cloudflare or another CDN | Described below | No |
| Windows, macOS, FreeBSD | Described below | The program builds for them; it was not run there. |

"Tested" for the five web servers means: `make webserver-check` starts the
real server with the example file and checks through HTTPS that pages pass,
that the website receives the visitor's real address also when a client
sends a made-up one, that an address rule cannot be passed with a made-up
address, that the security check works and its cookie is marked Secure, that
the block page arrives unchanged, and that websockets pass in both
directions. Only the site name, the port and the certificate paths of the
example are changed for the test.

## The one setting that matters everywhere

Whatever stands in front of Xibalba must be listed in
`server.trusted_proxies`, or Xibalba sees every visitor as that one server:
address rules match everybody or nobody, request limits count all visitors
together, and the trap catches all of them at once.

```yaml
server:
  trusted_proxies: ["127.0.0.1"]
```

Xibalba then reads the visitor's address from the `X-Forwarded-For` header,
from the right, and believes only what a trusted proxy added. A client
cannot make up an address. Each example file sets this header as needed and
says which line to put in `xibalba.yaml`.

## Web servers

Each example passes every request to Xibalba on `127.0.0.1:8080`, hands on
the site name the visitor asked for, the visitor's address and whether the
visitor came over HTTPS, and lets websockets through.

- **nginx.** The example sets the headers explicitly, including the two
  lines websockets need.
- **Caddy.** One line; Caddy does the rest by itself, also the certificate.
- **Apache.** Needs `mod_ssl`, `mod_proxy`, `mod_proxy_http` and
  `mod_headers`. The example passes addresses on as the visitor wrote them
  (`nocanon`), so Xibalba's rules see what your website would have seen.
  Websockets need Apache 2.4.47 or later.
- **HAProxy.** Wants certificate and key in one file. The example removes an
  `X-Forwarded-For` header the client sent before adding its own.
- **Traefik.** Two files: where Traefik listens, and the route. Traefik
  removes forwarding headers a client sent by itself.

## Containers

The image is built from the [`Dockerfile`](../Dockerfile) and holds nothing
but the program: no shell, no package manager. It runs as an unprivileged
user (65532) and writes only to `/var/lib/xibalba`.

```sh
docker build -t xibalba .
```

Images are not published yet (**planned**); build your own.

- **Configuration:** mount your `xibalba.yaml` at
  `/etc/xibalba/xibalba.yaml`.
- **Listen on all addresses** inside the container: `server.listen:
  "0.0.0.0:8080"`. What is reachable from outside is decided by the port
  mapping.
- **Keep `/var/lib/xibalba` in a volume** and put `challenge.key_file`,
  `crawlers.cache_dir` and `countries.database` there, so they survive a new
  container.
- **Health check:** the image asks the running Xibalba itself:
  `xibalba -config … -healthcheck` exits with 0 when Xibalba is healthy.
  Keep `ops.listen` at `127.0.0.1:9090`; inside the container that is
  reachable for the health check and for nothing else.
- **Trusted proxies:** in a container network the web server in front has an
  address of that network, not 127.0.0.1. List that address or network.

[`examples/docker/compose.yaml`](../examples/docker/compose.yaml) starts
Xibalba in front of a test website:

```sh
cd examples/docker
docker compose up --build
```

## systemd

[`examples/systemd/xibalba.service`](../examples/systemd/xibalba.service)
runs Xibalba as its own user, checks the configuration before every start,
restarts it after a failure, and takes away everything the program does not
need (no access to home directories, read-only system, no new privileges).
Its state directory is `/var/lib/xibalba`. The commands to install it are at
the top of the file.

## Kubernetes

[`examples/kubernetes/xibalba.yaml`](../examples/kubernetes/xibalba.yaml)
puts Xibalba as a second container into the website's pod. The Service
points at Xibalba's port; Xibalba reaches the website on `localhost`. The
probes use `xibalba -healthcheck`.

Two things to mind:

- List the pod network of your ingress controller under
  `server.trusted_proxies`, and make sure the ingress passes on
  `X-Forwarded-For`.
- With more than one replica, give all of them the same signing key
  (`challenge.key_file` from a Secret), or a visitor checked by one replica
  is checked again by the next. Request limits and the trap count per
  replica. Shared state is **planned** (milestone M9).

## Behind Cloudflare or another CDN

A CDN is one more proxy in front. Xibalba needs its addresses under
`server.trusted_proxies`, so that the visitor's address is read from what
the CDN reports:

```yaml
server:
  trusted_proxies:
    - "127.0.0.1"          # your own web server
    - "192.0.2.0/24"       # the CDN's networks: take the current list from the CDN
```

Take the list of networks from the CDN's own published list and keep it
current; a network missing from it makes every visitor arriving through it
look like the CDN. Xibalba uses the `X-Forwarded-For` header; a header of
the CDN's own (such as `CF-Connecting-IP`) is not read. Loading the CDN's
list automatically is **planned**.

Tell the CDN not to cache Xibalba's own pages. They are sent with
`Cache-Control: no-store`; a CDN set to ignore that would hand one visitor's
security check to another.

## Windows, macOS, FreeBSD

Xibalba is written without anything specific to Linux, and the project's CI
builds it for Windows, macOS and FreeBSD at every change. It has not been
run or tested there, and no installer or service definition is shipped.
Build with:

```sh
GOOS=windows GOARCH=amd64 go build -o xibalba.exe ./cmd/xibalba
```

## Compared with Anubis

Anubis documents the same environments. The differences that matter in
practice:

- Xibalba has a list of trusted proxies and reads the visitor's address
  itself; the web server only has to pass on or set `X-Forwarded-For`.
- One Xibalba serves one website (`upstream.url`). Several websites behind
  one Xibalba, chosen by host name, are **planned**.
- A mode in which the web server only asks Xibalba "may this request pass?"
  (nginx `auth_request`, Caddy `forward_auth`, Traefik `forwardAuth`) is
  **planned** (milestone M9). Today Xibalba is always in the path.
- Published images and packages (deb, rpm) are **planned**.
