#!/usr/bin/env python3
"""Checks Xibalba behind real web servers, using the example configurations.

The handbook tells operators to put a web server in front of Xibalba and
shows a configuration for nginx, Caddy, Apache, HAProxy and Traefik. This script proves those configurations work:
it starts a test website, Xibalba, and the web server with the file from
examples/, and then checks through HTTPS that

  * pages are passed through,
  * the website receives the visitor's real address, also when the client
    sends a made-up X-Forwarded-For,
  * a rule on the client address sees the real address,
  * the challenge works and its cookie is marked Secure,
  * the block page arrives unchanged, with its security headers,
  * an upgraded connection (websocket) passes in both directions.

Only the site name, port and certificate paths of the example files are
changed for the test; everything else is used as shipped. (For Apache the
protocol name of the upgraded connection is changed too: the test speaks a
small protocol of its own over it instead of websocket.)

Requirements: openssl and any of nginx, caddy, apache2, haproxy, traefik. A web server that is not
installed is skipped.

Usage:

    make build
    python3 test/webserver/check.py [--binary bin/xibalba]

Exit code 0 means every check passed.
"""

import argparse
import html
import hashlib
import http.client
import http.server
import json
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
RESULTS = []

# The test client connects from this address. It is a loopback address other
# than 127.0.0.1, so the web server sees a client that is NOT in Xibalba's
# list of trusted proxies, as a real visitor would be.
CLIENT = "127.0.0.2"
SPOOFED = "203.0.113.99"


def check(name, ok, detail=""):
    RESULTS.append(ok)
    print(("PASS  " if ok else "FAIL  ") + name + (("  -- " + str(detail)) if detail and not ok else ""))


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Website(http.server.BaseHTTPRequestHandler):
    """The protected website: reports what it received, and echoes lines over
    an upgraded connection."""

    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.headers.get("Upgrade", "").lower() == "echo":
            self.connection.sendall(b"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
            reader = self.connection.makefile("rb")
            while True:
                line = reader.readline()
                if not line:
                    break
                self.connection.sendall(b"echo: " + line)
            self.close_connection = True
            return
        body = json.dumps({"path": self.path, "headers": {k.lower(): v for k, v in self.headers.items()}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def wait_port(port, seconds=15):
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            socket.create_connection(("127.0.0.1", port), timeout=0.5).close()
            return True
        except OSError:
            time.sleep(0.1)
    return False


def request(port, method, path, headers=None, body=None):
    """One HTTPS request from CLIENT. Returns (status, headers, body)."""
    conn = http.client.HTTPSConnection("www.example.org", port, timeout=15)
    conn.sock = connect(port)
    h = {"User-Agent": "Mozilla/5.0 (webserver check)", "Accept-Language": "en", "Host": "www.example.org"}
    h.update(headers or {})
    conn.request(method, path, body=body, headers=h)
    resp = conn.getresponse()
    data = resp.read().decode("utf-8", "replace")
    result = (resp.status, resp.getheaders(), data)
    conn.close()
    return result


def connect(port):
    """A TLS connection from CLIENT to the web server on this machine, asking
    for the site www.example.org as a browser would (SNI)."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    raw = socket.create_connection(("127.0.0.1", port), timeout=15, source_address=(CLIENT, 0))
    return ctx.wrap_socket(raw, server_hostname="www.example.org")


def header(headers, name):
    return [v for k, v in headers if k.lower() == name.lower()]


def solve(nonce, difficulty):
    n = 0
    while True:
        digest = hashlib.sha256((nonce + str(n)).encode()).digest()
        if int.from_bytes(digest[:4], "big") >> (32 - difficulty) == 0:
            return str(n)
        n += 1


def run_checks(name, port):
    # 1. Pages pass through, and the website learns the real address.
    status, _, body = request(port, "GET", "/page?x=1")
    seen = json.loads(body)["headers"] if status == 200 else {}
    check(f"{name}: a page is passed through", status == 200 and json.loads(body)["path"] == "/page?x=1", (status, body[:200]))
    check(f"{name}: the website receives the visitor's address", seen.get("x-real-ip") == CLIENT, seen.get("x-real-ip"))
    check(f"{name}: the website is told the visitor came over HTTPS", seen.get("x-forwarded-proto") == "https", seen.get("x-forwarded-proto"))
    check(f"{name}: the website receives the site name the visitor asked for", seen.get("host") == "www.example.org", seen.get("host"))

    # 2. A made-up X-Forwarded-For does not change who the visitor is.
    status, _, body = request(port, "GET", "/page", {"X-Forwarded-For": SPOOFED, "X-Real-IP": SPOOFED})
    seen = json.loads(body)["headers"] if status == 200 else {}
    check(f"{name}: a made-up X-Forwarded-For is not believed", seen.get("x-real-ip") == CLIENT, seen.get("x-real-ip"))

    # 3. A rule on the client address sees the real address: /office is
    #    allowed only from the spoofed address, so it must stay blocked.
    status, _, _ = request(port, "GET", "/office", {"X-Forwarded-For": SPOOFED})
    check(f"{name}: an address rule cannot be passed with a made-up address", status == 403, status)

    # 4. The block page arrives unchanged.
    status, headers, body = request(port, "GET", "/admin")
    csp = header(headers, "Content-Security-Policy")
    check(f"{name}: the block page arrives with status 403 and its policy",
          status == 403 and "This request was blocked" in body and csp and csp[0].startswith("default-src 'none'"), (status, csp))

    # 5. The challenge, start to finish.
    status, _, page = request(port, "GET", "/wiki/start")
    token = re.search(r'name="token" value="([^"]+)"', page)
    nonce = re.search(r'data-nonce="([0-9a-f]+)"', page)
    difficulty = re.search(r'data-difficulty="(\d+)"', page)
    check(f"{name}: a challenged path shows the challenge", status == 403 and bool(token and nonce and difficulty), status)
    if token and nonce and difficulty:
        form = urllib.parse.urlencode({"token": token.group(1), "return": "/wiki/start", "method": "pow",
                                       "solution": solve(nonce.group(1), int(difficulty.group(1)))})
        status, headers, _ = request(port, "POST", "/.xibalba/verify", {"Content-Type": "application/x-www-form-urlencoded"}, form)
        cookies = header(headers, "Set-Cookie")
        check(f"{name}: a correct answer is accepted", status == 303 and header(headers, "Location") == ["/wiki/start"], (status, header(headers, "Location")))
        check(f"{name}: the pass cookie is marked Secure", bool(cookies) and "; Secure" in cookies[0] and "HttpOnly" in cookies[0], cookies)
        if cookies:
            status, _, body = request(port, "GET", "/wiki/start", {"Cookie": cookies[0].split(";")[0]})
            seen = json.loads(body)["headers"] if status == 200 else {}
            check(f"{name}: with the pass the website answers", status == 200, status)
            check(f"{name}: the website does not receive the pass cookie", "cookie" not in seen, seen.get("cookie"))

    # 6. An upgraded connection passes in both directions.
    try:
        tls = connect(port)
        tls.sendall(b"GET /socket HTTP/1.1\r\nHost: www.example.org\r\nUser-Agent: Mozilla/5.0 (webserver check)\r\n"
                    b"Accept-Language: en\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
        reader = tls.makefile("rb")
        status_line = reader.readline().decode()
        while reader.readline() not in (b"\r\n", b""):
            pass
        replies = []
        for message in (b"hello\n", b"again\n"):
            tls.sendall(message)
            replies.append(reader.readline())
        tls.close()
        check(f"{name}: an upgraded connection (websocket) passes both ways",
              " 101 " in status_line and replies == [b"echo: hello\n", b"echo: again\n"], (status_line.strip(), replies))
    except OSError as e:
        check(f"{name}: an upgraded connection (websocket) passes both ways", False, e)


def decisions(ops_port):
    with urllib.request.urlopen(f"http://127.0.0.1:{ops_port}/decisions", timeout=5) as resp:
        return {s["source"]: s["count"] for s in json.load(resp)["sources"]}


def run_verdict_checks(name, port, ops_port, shown):
    """The web server asks Xibalba about each request and talks to the
    website itself. shown: the status a visitor sees with the security check
    (the web servers differ in whose status they pass on)."""
    before = decisions(ops_port)

    status, _, body = request(port, "GET", "/page?x=1")
    seen = json.loads(body) if status == 200 else {}
    check(f"{name}: an allowed request reaches the website", status == 200 and seen.get("path") == "/page?x=1", (status, body[:200]))

    status, headers, body = request(port, "GET", "/admin")
    csp = header(headers, "Content-Security-Policy")
    check(f"{name}: a denied request gets the block page with status 403 and its policy",
          status == 403 and "This request was blocked" in body and csp and csp[0].startswith("default-src 'none'"), (status, csp, body[:200]))

    status, _, _ = request(port, "GET", "/office", {"X-Forwarded-For": SPOOFED})
    check(f"{name}: an address rule cannot be passed with a made-up address", status == 403, status)

    status, _, body = request(port, "POST", "/form", {"Content-Type": "application/x-www-form-urlencoded"}, "a=1")
    check(f"{name}: a rule on the request method sees the visitor's method", status == 403 and "This request was blocked" in body, (status, body[:200]))
    status, _, _ = request(port, "GET", "/form")
    check(f"{name}: ... and lets the other methods pass", status == 200, status)

    # An address dressed up as one of Xibalba's own is decided like any other.
    for path in ("/.xibalba/../admin", "/.xibalba/%2e%2e/admin", "/.xibalba/..%2fadmin", "/.xibalba/../wiki/start"):
        status, _, body = request(port, "GET", path)
        check(f"{name}: {path} does not reach the website", status != 200 and "\"path\"" not in body, (status, body[:120]))

    # Headers a visitor sends cannot stand in for the web server's.
    status, _, body = request(port, "GET", "/admin", {"X-Forwarded-Uri": "/page", "X-Forwarded-Method": "GET", "X-Forwarded-Host": "other.example",
                                                      "X-Xibalba-Verdict": "pass"})
    check(f"{name}: a visitor's own X-Forwarded-Uri and X-Xibalba-Verdict change nothing", status == 403 and "This request was blocked" in body, (status, body[:120]))

    for path in ("/.xibalba/check", "/.xibalba/page"):
        status, _, body = request(port, "GET", path, {"X-Forwarded-Uri": "/page", "X-Forwarded-Method": "GET"})
        check(f"{name}: a visitor cannot ask {path} himself", status == 404, (status, body[:100]))

    status, headers, page = request(port, "GET", "/wiki/start?a=1")
    token = re.search(r'name="token" value="([^"]+)"', page)
    nonce = re.search(r'data-nonce="([0-9a-f]+)"', page)
    difficulty = re.search(r'data-difficulty="(\d+)"', page)
    ret = re.search(r'name="return" value="([^"]*)"', page)
    check(f"{name}: a challenged path shows the security check (status {shown})",
          status == shown and bool(token and nonce and difficulty) and bool(ret) and html.unescape(ret.group(1)) == "/wiki/start?a=1", (status, page[:200]))
    if token and nonce and difficulty:
        form = urllib.parse.urlencode({"token": token.group(1), "return": "/wiki/start?a=1", "method": "pow",
                                       "solution": solve(nonce.group(1), int(difficulty.group(1)))})
        status, headers, _ = request(port, "POST", "/.xibalba/verify", {"Content-Type": "application/x-www-form-urlencoded"}, form)
        cookies = header(headers, "Set-Cookie")
        check(f"{name}: a correct answer is accepted", status == 303 and header(headers, "Location") == ["/wiki/start?a=1"], (status, header(headers, "Location")))
        check(f"{name}: the pass cookie is marked Secure", bool(cookies) and "; Secure" in cookies[0], cookies)
        if cookies:
            status, _, body = request(port, "GET", "/wiki/start?a=1", {"Cookie": cookies[0].split(";")[0]})
            check(f"{name}: with the pass the website answers", status == 200, status)

    after = decisions(ops_port)
    counted = {k: after.get(k, 0) - before.get(k, 0) for k in after}
    check(f"{name}: every request is counted once, although refused ones are asked about twice",
          counted.get("rule:block-admin") == 5 and counted.get("rule:challenge-wiki") == 3 and counted.get("rule:block-post") == 1, counted)


def nginx_config(tmp, port, cert, key, example="xibalba.conf", xibalba=None, website=None):
    with open(os.path.join(ROOT, "examples", "nginx", example)) as f:
        site = f.read()
    if website:
        site = site.replace("http://127.0.0.1:3000", f"http://127.0.0.1:{website}")
    # The three things an operator changes too: port, and certificate paths.
    site = site.replace("listen 443 ssl;", f"listen 127.0.0.1:{port} ssl;")
    site = site.replace("/etc/ssl/certs/www.example.org.pem", cert).replace("/etc/ssl/private/www.example.org.key", key)
    site = site.replace("http://127.0.0.1:8080", f"http://127.0.0.1:{xibalba or XIBALBA_PORT}")
    path = os.path.join(tmp, "nginx-" + example)
    with open(path, "w") as f:
        f.write(f"""daemon off;
user root;
worker_processes 1;
pid {tmp}/nginx-{example}.pid;
error_log {tmp}/nginx-error.log;
events {{}}
http {{
    access_log off;
    client_body_temp_path {tmp}/n-body;
    proxy_temp_path {tmp}/n-proxy;
    fastcgi_temp_path {tmp}/n-fastcgi;
    uwsgi_temp_path {tmp}/n-uwsgi;
    scgi_temp_path {tmp}/n-scgi;
{site}
}}
""")
    return path


def caddy_config(tmp, port, example="Caddyfile", xibalba=None, website=None):
    with open(os.path.join(ROOT, "examples", "caddy", example)) as f:
        site = f.read()
    if website:
        site = site.replace("127.0.0.1:3000", f"127.0.0.1:{website}")
    # A public certificate cannot be obtained here, so the test lets Caddy
    # sign one with its own local authority, and uses a free port.
    site = site.replace("www.example.org {", f"www.example.org:{port} {{\n\ttls internal")
    site = site.replace("127.0.0.1:8080", f"127.0.0.1:{xibalba or XIBALBA_PORT}")
    path = os.path.join(tmp, example)
    with open(path, "w") as f:
        f.write(f"""{{
	admin off
	auto_https disable_redirects
	skip_install_trust
	storage file_system {tmp}/caddy-storage-{example}
}}
{site}""")
    return path


def apache_config(tmp, port, cert, key):
    with open(os.path.join(ROOT, "examples", "apache", "xibalba.conf")) as f:
        site = f.read()
    site = site.replace("<VirtualHost *:443>", f"<VirtualHost 127.0.0.1:{port}>")
    site = site.replace("/etc/ssl/certs/www.example.org.pem", cert).replace("/etc/ssl/private/www.example.org.key", key)
    site = site.replace("http://127.0.0.1:8080/", f"http://127.0.0.1:{XIBALBA_PORT}/")
    site = site.replace("upgrade=websocket", "upgrade=echo")  # the test's own protocol
    modules = "/usr/lib/apache2/modules"
    load = "".join(f"LoadModule {name}_module {modules}/mod_{name}.so\n" for name in
                   ["mpm_event", "authz_core", "ssl", "socache_shmcb", "proxy", "proxy_http", "headers"])
    path = os.path.join(tmp, "apache.conf")
    with open(path, "w") as f:
        f.write(f"""ServerRoot {tmp}
PidFile {tmp}/apache.pid
ErrorLog {tmp}/apache-error.log
Mutex file:{tmp} default
ServerName localhost
Listen 127.0.0.1:{port}
{load}
{site}
""")
    return path


def haproxy_config(tmp, port, cert, key):
    with open(os.path.join(ROOT, "examples", "haproxy", "haproxy.cfg")) as f:
        site = f.read()
    pem = os.path.join(tmp, "haproxy.pem")
    with open(pem, "w") as out:
        out.write(open(cert).read() + open(key).read())
    site = site.replace("bind :443 ssl crt /etc/haproxy/certs/www.example.org.pem", f"bind 127.0.0.1:{port} ssl crt {pem}")
    site = site.replace("127.0.0.1:8080", f"127.0.0.1:{XIBALBA_PORT}")
    path = os.path.join(tmp, "haproxy.cfg")
    with open(path, "w") as f:
        f.write(site)
    return path


def traefik_config(tmp, port, cert, key):
    with open(os.path.join(ROOT, "examples", "traefik", "dynamic.yml")) as f:
        dynamic = f.read()
    dynamic = dynamic.replace("/etc/ssl/certs/www.example.org.pem", cert).replace("/etc/ssl/private/www.example.org.key", key)
    dynamic = dynamic.replace("http://127.0.0.1:8080", f"http://127.0.0.1:{XIBALBA_PORT}")
    dynamic_path = os.path.join(tmp, "dynamic.yml")
    with open(dynamic_path, "w") as f:
        f.write(dynamic)
    with open(os.path.join(ROOT, "examples", "traefik", "traefik.yml")) as f:
        static = f.read()
    static = static.replace('address: ":443"', f'address: "127.0.0.1:{port}"').replace("/etc/traefik/dynamic.yml", dynamic_path)
    path = os.path.join(tmp, "traefik.yml")
    with open(path, "w") as f:
        f.write(static)
    return path


XIBALBA_PORT = 0


def main():
    global XIBALBA_PORT
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--binary", default=os.path.join(ROOT, "bin", "xibalba"))
    args = parser.parse_args()
    if not os.path.exists(args.binary):
        raise SystemExit(f"{args.binary} not found: run 'make build' first")

    site_port, XIBALBA_PORT, ops_port = free_port(), free_port(), free_port()
    site = http.server.ThreadingHTTPServer(("127.0.0.1", site_port), Website)
    threading.Thread(target=site.serve_forever, daemon=True).start()

    procs = []
    with tempfile.TemporaryDirectory() as tmp:
        config = os.path.join(tmp, "xibalba.yaml")
        with open(config, "w") as f:
            f.write(f"""upstream:
  url: "http://127.0.0.1:{site_port}"
server:
  listen: "127.0.0.1:{XIBALBA_PORT}"
  trusted_proxies: ["127.0.0.1"]
ops:
  listen: "127.0.0.1:{ops_port}"
challenge:
  difficulty: 10
rules:
  list:
    - name: allow-office
      match:
        path: {{prefix: "/office"}}
        ip: ["{SPOOFED}"]
      action: allow
    - name: block-office
      match:
        path: {{prefix: "/office"}}
      action: deny
    - name: block-admin
      match:
        path: {{prefix: "/admin"}}
      action: deny
    - name: challenge-wiki
      match:
        path: {{prefix: "/wiki"}}
      action: challenge
""")
        try:
            procs.append(subprocess.Popen([args.binary, "-config", config], stderr=subprocess.DEVNULL))
            if not wait_port(ops_port):
                raise SystemExit("xibalba did not start")

            cert, key = os.path.join(tmp, "cert.pem"), os.path.join(tmp, "key.pem")
            if shutil.which("openssl"):
                subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert,
                                "-days", "1", "-subj", "/CN=www.example.org"], check=True, capture_output=True)

            if shutil.which("nginx") and shutil.which("openssl"):
                port = free_port()
                conf = nginx_config(tmp, port, cert, key)
                test = subprocess.run(["nginx", "-t", "-c", conf], capture_output=True, text=True)
                check("nginx: the example configuration is accepted by nginx -t", test.returncode == 0, test.stderr.strip())
                version = subprocess.run(["nginx", "-v"], capture_output=True, text=True).stderr.strip()
                print(f"      ({version})")
                if test.returncode == 0:
                    procs.append(subprocess.Popen(["nginx", "-c", conf], stderr=subprocess.DEVNULL))
                    if wait_port(port):
                        run_checks("nginx", port)
                    else:
                        check("nginx: starts", False)
            else:
                print("SKIP  nginx: not installed (or openssl missing)")

            if shutil.which("caddy"):
                port = free_port()
                conf = caddy_config(tmp, port)
                env = dict(os.environ, XDG_DATA_HOME=os.path.join(tmp, "caddy-data"), XDG_CONFIG_HOME=os.path.join(tmp, "caddy-config"), HOME=tmp)
                test = subprocess.run(["caddy", "validate", "--config", conf, "--adapter", "caddyfile"], capture_output=True, text=True, env=env)
                check("caddy: the example configuration is accepted by caddy validate", test.returncode == 0, (test.stdout + test.stderr).strip()[-400:])
                version = subprocess.run(["caddy", "version"], capture_output=True, text=True).stdout.strip()
                print(f"      (Caddy {version})")
                if test.returncode == 0:
                    procs.append(subprocess.Popen(["caddy", "run", "--config", conf, "--adapter", "caddyfile"],
                                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env))
                    if wait_port(port):
                        time.sleep(1.0)  # Caddy issues its local certificate right after binding
                        run_checks("caddy", port)
                    else:
                        check("caddy: starts", False)
            else:
                print("SKIP  caddy: not installed")

            # Apache, HAProxy and Traefik: check the file, start, run the checks.
            apache = shutil.which("apache2") or shutil.which("httpd")
            others = [
                ("apache", apache, apache_config,
                 lambda conf: [apache, "-t", "-f", conf], lambda conf: [apache, "-X", "-f", conf], [apache, "-v"]),
                ("haproxy", shutil.which("haproxy"), haproxy_config,
                 lambda conf: ["haproxy", "-c", "-f", conf], lambda conf: ["haproxy", "-f", conf], ["haproxy", "-v"]),
                ("traefik", shutil.which("traefik"), traefik_config,
                 None, lambda conf: ["traefik", "--configFile=" + conf], ["traefik", "version"]),
            ]
            for name, found, make, test_cmd, run_cmd, version_cmd in others:
                if not found or not shutil.which("openssl"):
                    print(f"SKIP  {name}: not installed (or openssl missing)")
                    continue
                port = free_port()
                conf = make(tmp, port, cert, key)
                if test_cmd:
                    test = subprocess.run(test_cmd(conf), capture_output=True, text=True)
                    check(f"{name}: the example configuration is accepted by the server's own check", test.returncode == 0,
                          (test.stdout + test.stderr).strip()[-400:])
                    if test.returncode != 0:
                        continue
                version = subprocess.run(version_cmd, capture_output=True, text=True)
                print("      (" + (version.stdout + version.stderr).strip().splitlines()[0] + ")")
                log = open(os.path.join(tmp, name + ".log"), "w")
                procs.append(subprocess.Popen(run_cmd(conf), stdout=log, stderr=log))
                if wait_port(port):
                    time.sleep(0.5)
                    run_checks(name, port)
                else:
                    check(f"{name}: starts", False, open(os.path.join(tmp, name + ".log")).read()[-400:])

            # The other way round: the web server talks to the website
            # itself and only asks Xibalba (docs/VERDICT.md). A second
            # Xibalba without a website of its own answers.
            v_port, v_ops = free_port(), free_port()
            v_config = os.path.join(tmp, "xibalba-verdict.yaml")
            with open(v_config, "w") as f:
                f.write(f"""server:
  listen: "127.0.0.1:{v_port}"
  trusted_proxies: ["127.0.0.1"]
verdict:
  enabled: true
ops:
  listen: "127.0.0.1:{v_ops}"
challenge:
  difficulty: 10
rules:
  list:
    - name: allow-office
      match:
        path: {{prefix: "/office"}}
        ip: ["{SPOOFED}"]
      action: allow
    - name: block-office
      match:
        path: {{prefix: "/office"}}
      action: deny
    - name: block-admin
      match:
        path: {{prefix: "/admin"}}
      action: deny
    - name: block-post
      match:
        path: {{prefix: "/form"}}
        method: [POST]
      action: deny
    - name: challenge-wiki
      match:
        path: {{prefix: "/wiki"}}
      action: challenge
""")
            procs.append(subprocess.Popen([args.binary, "-config", v_config], stderr=subprocess.DEVNULL))
            if not wait_port(v_ops):
                raise SystemExit("xibalba (verdicts) did not start")

            if shutil.which("nginx") and shutil.which("openssl"):
                port = free_port()
                conf = nginx_config(tmp, port, cert, key, "xibalba-verdict.conf", v_port, site_port)
                test = subprocess.run(["nginx", "-t", "-c", conf], capture_output=True, text=True)
                check("nginx, asking: the example configuration is accepted by nginx -t", test.returncode == 0, test.stderr.strip())
                if test.returncode == 0:
                    procs.append(subprocess.Popen(["nginx", "-c", conf], stderr=subprocess.DEVNULL))
                    if wait_port(port):
                        run_verdict_checks("nginx, asking", port, v_ops, 403)
                    else:
                        check("nginx, asking: starts", False)
            else:
                print("SKIP  nginx, asking: not installed (or openssl missing)")

            if shutil.which("caddy"):
                port = free_port()
                conf = caddy_config(tmp, port, "Caddyfile.verdict", v_port, site_port)
                env = dict(os.environ, XDG_DATA_HOME=os.path.join(tmp, "caddy-data2"), XDG_CONFIG_HOME=os.path.join(tmp, "caddy-config2"), HOME=tmp)
                test = subprocess.run(["caddy", "validate", "--config", conf, "--adapter", "caddyfile"], capture_output=True, text=True, env=env)
                check("caddy, asking: the example configuration is accepted by caddy validate", test.returncode == 0, (test.stdout + test.stderr).strip()[-400:])
                if test.returncode == 0:
                    procs.append(subprocess.Popen(["caddy", "run", "--config", conf, "--adapter", "caddyfile"],
                                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env))
                    if wait_port(port):
                        time.sleep(1.0)
                        run_verdict_checks("caddy, asking", port, v_ops, 401)
                    else:
                        check("caddy, asking: starts", False)
            else:
                print("SKIP  caddy, asking: not installed")
        finally:
            for p in reversed(procs):
                p.terminate()
            for p in procs:
                try:
                    p.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    p.kill()
            site.shutdown()

    failed = RESULTS.count(False)
    print(f"\n{len(RESULTS) - failed} passed, {failed} failed")
    sys.exit(1 if failed or not RESULTS else 0)


if __name__ == "__main__":
    main()
