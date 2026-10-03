#!/usr/bin/env python3
"""Checks Xibalba behind real web servers, using the example configurations.

The handbook tells operators to put nginx or Caddy in front of Xibalba and
shows a configuration for each. This script proves those configurations work:
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
changed for the test; everything else is used as shipped.

Requirements: nginx and/or caddy, openssl. A web server that is not
installed is skipped.

Usage:

    make build
    python3 test/webserver/check.py [--binary bin/xibalba]

Exit code 0 means every check passed.
"""

import argparse
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


def nginx_config(tmp, port, cert, key):
    with open(os.path.join(ROOT, "examples", "nginx", "xibalba.conf")) as f:
        site = f.read()
    # The three things an operator changes too: port, and certificate paths.
    site = site.replace("listen 443 ssl;", f"listen 127.0.0.1:{port} ssl;")
    site = site.replace("/etc/ssl/certs/www.example.org.pem", cert).replace("/etc/ssl/private/www.example.org.key", key)
    site = site.replace("http://127.0.0.1:8080", f"http://127.0.0.1:{XIBALBA_PORT}")
    path = os.path.join(tmp, "nginx.conf")
    with open(path, "w") as f:
        f.write(f"""daemon off;
user root;
worker_processes 1;
pid {tmp}/nginx.pid;
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


def caddy_config(tmp, port):
    with open(os.path.join(ROOT, "examples", "caddy", "Caddyfile")) as f:
        site = f.read()
    # A public certificate cannot be obtained here, so the test lets Caddy
    # sign one with its own local authority, and uses a free port.
    site = site.replace("www.example.org {", f"www.example.org:{port} {{\n\ttls internal")
    site = site.replace("127.0.0.1:8080", f"127.0.0.1:{XIBALBA_PORT}")
    path = os.path.join(tmp, "Caddyfile")
    with open(path, "w") as f:
        f.write(f"""{{
	admin off
	auto_https disable_redirects
	skip_install_trust
	storage file_system {tmp}/caddy-storage
}}
{site}""")
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

            if shutil.which("nginx") and shutil.which("openssl"):
                cert, key = os.path.join(tmp, "cert.pem"), os.path.join(tmp, "key.pem")
                subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert,
                                "-days", "1", "-subj", "/CN=www.example.org"], check=True, capture_output=True)
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
