#!/usr/bin/env python3
"""Checks Xibalba's visitor pages in a real browser.

The Go tests prove that the server side of the challenge is right. This
script proves the part they cannot: that a real browser, with and without
JavaScript, gets through the challenge; that the pages load nothing from
anywhere else; that they can be used with the keyboard alone; and that an
automated accessibility checker finds nothing to complain about.

It starts a small website and Xibalba by itself, on free ports, and stops
them again.

Requirements (not needed for the normal test suite):

    pip install playwright          # and a Chromium for it
    npm install axe-core            # optional, for the accessibility check

Usage:

    make build
    python3 test/browser/check.py [--binary bin/xibalba] [--axe path/to/axe.min.js]
                                  [--screenshots DIR]

Exit code 0 means every check passed.
"""

import argparse
import asyncio
import os
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

from playwright.async_api import async_playwright

RESULTS = []


def check(name, ok, detail=""):
    RESULTS.append(ok)
    print(("PASS  " if ok else "FAIL  ") + name + (("  -- " + str(detail)) if detail and not ok else ""))


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_for(url, seconds=10):
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url).read()
            return
        except urllib.error.HTTPError:
            return
        except OSError:
            time.sleep(0.05)
    raise SystemExit(f"{url} did not answer")


def fetch(url, lang):
    req = urllib.request.Request(url, headers={"Accept-Language": lang, "User-Agent": "Mozilla/5.0 (browser check)"})
    try:
        return urllib.request.urlopen(req).read().decode()
    except urllib.error.HTTPError as e:
        return e.read().decode()


async def run(base, axe_source, shots):
    async with async_playwright() as p:
        browser = await p.chromium.launch()

        # 1. With JavaScript: the page solves the task and the visitor lands
        #    on the website, in both languages, light and dark, desktop and phone.
        for name, lang, scheme, width, height in [("desktop", "de", "light", 900, 560), ("phone", "en", "dark", 390, 700)]:
            ctx = await browser.new_context(locale=lang, color_scheme=scheme, viewport={"width": width, "height": height},
                                            extra_http_headers={"Accept-Language": lang})
            page = await ctx.new_page()
            problems, hosts, steps = [], set(), []
            page.on("pageerror", lambda e: problems.append(str(e)))
            page.on("console", lambda m: problems.append(m.text) if "Content Security Policy" in m.text or "Refused" in m.text else None)
            page.on("request", lambda r: hosts.add(re.sub(r"^(https?://[^/]+).*", r"\1", r.url)))
            page.on("response", lambda r: steps.append((r.request.method, r.status)))
            started = time.time()
            await page.goto(base + "/wiki/start.html?x=1")
            try:
                await page.wait_for_selector("text=My website", timeout=30000)
                landed = True
            except Exception:
                landed = False
            took = time.time() - started
            check(f"javascript/{name}: challenge is solved and the website is shown ({took:.2f}s)", landed)
            check(f"javascript/{name}: steps are challenge, answer, website", steps[:3] == [("GET", 403), ("POST", 303), ("GET", 200)], steps)
            check(f"javascript/{name}: nothing is loaded from another host", hosts == {base}, hosts)
            check(f"javascript/{name}: no script error and no policy violation", not problems, problems)
            cookies = await ctx.cookies()
            check(f"javascript/{name}: the pass cookie is HttpOnly and SameSite=Lax",
                  len(cookies) == 1 and cookies[0]["httpOnly"] and cookies[0]["sameSite"] == "Lax", cookies)
            response = await page.goto(base + "/wiki/other.html")
            check(f"javascript/{name}: no second challenge with the pass", response.status == 200, response.status)
            await ctx.close()

        # 2. While the script works: the status is announced and the page stays responsive.
        ctx = await browser.new_context(locale="de", viewport={"width": 900, "height": 520}, extra_http_headers={"Accept-Language": "de"})
        page = await ctx.new_page()

        async def make_unsolvable(route):
            response = await route.fetch()
            body = re.sub(r'data-difficulty="\d+"', 'data-difficulty="32"', await response.text())
            await route.fulfill(response=response, body=body)

        await page.route(base + "/wiki/working.html", make_unsolvable)
        await page.goto(base + "/wiki/working.html")
        await asyncio.sleep(0.6)
        state = await page.evaluate("""(() => { const s = document.getElementById('xibalba-status');
            return {hidden: s.hidden, text: s.textContent, role: s.getAttribute('role'), live: s.getAttribute('aria-live'),
                    manualHidden: document.getElementById('xibalba-manual').hidden}; })()""")
        check("working: the status is visible and announced politely",
              not state["hidden"] and state["text"].strip() != "" and state["role"] == "status" and state["live"] == "polite", state)
        check("working: the path without JavaScript is hidden while the script runs", state["manualHidden"], state)
        delay = await page.evaluate("new Promise(r => { const t = performance.now(); setTimeout(() => r(performance.now() - t), 0); })")
        check(f"working: the page stays responsive ({delay:.0f} ms timer delay)", delay < 200, delay)
        if shots:
            await page.screenshot(path=os.path.join(shots, "challenge_working.png"))
        await ctx.close()

        # 3. Without JavaScript, keyboard only: wait, press the button, get in.
        ctx = await browser.new_context(java_script_enabled=False, locale="de", viewport={"width": 900, "height": 620},
                                        extra_http_headers={"Accept-Language": "de"})
        page = await ctx.new_page()
        response = await page.goto(base + "/wiki/noscript.html")
        check("no javascript: the challenge page is shown", response.status == 403, response.status)
        if shots:
            await page.screenshot(path=os.path.join(shots, "challenge_no_javascript.png"))
        await page.keyboard.press("Tab")
        focused = await page.locator(":focus").evaluate("e => e.tagName") if await page.locator(":focus").count() else None
        check("no javascript: the first Tab reaches the button", focused == "BUTTON", focused)
        await page.keyboard.press("Enter")
        try:
            await page.wait_for_selector("[role=alert]", timeout=15000)
            early = True
        except Exception:
            early = False
        check("no javascript: pressing at once gives a notice, not a dead end", early)
        if shots:
            await page.screenshot(path=os.path.join(shots, "challenge_too_early.png"))
        await asyncio.sleep(1.3)
        await page.keyboard.press("Tab")
        await page.keyboard.press("Enter")
        try:
            await page.wait_for_selector("text=My website", timeout=15000)
            landed = True
        except Exception:
            landed = False
        check("no javascript: after waiting, the button leads to the website", landed)
        await ctx.close()

        # 4. Blocked page: styled under its policy, keyboard reaches the language switch.
        ctx = await browser.new_context(locale="de", viewport={"width": 900, "height": 520}, extra_http_headers={"Accept-Language": "de"})
        page = await ctx.new_page()
        problems = []
        page.on("console", lambda m: problems.append(m.text) if "Content Security Policy" in m.text or "Refused" in m.text else None)
        response = await page.goto(base + "/admin")
        width = await page.evaluate("getComputedStyle(document.querySelector('main')).maxWidth")
        check("blocked: status 403 and the style block is applied under the policy", response.status == 403 and width != "none" and not problems, (response.status, width, problems))
        await page.keyboard.press("Tab")
        await page.keyboard.press("Enter")
        opened = await page.evaluate("document.querySelector('details').open")
        check("blocked: the other language opens with the keyboard", opened)
        # The trap link must exist for programs and not exist for people.
        trap = await page.evaluate("""() => {
            const t = document.querySelector('template');
            const inside = t ? t.content.querySelectorAll('a[href^="/.xibalba/trap/"]').length : 0;
            const live = document.querySelectorAll('a[href^="/.xibalba/trap/"]').length;
            return {inside, live, shown: t ? getComputedStyle(t).display : ''};
        }""")
        check("blocked: the trap link is in the page text but inert, invisible and not a link for people",
              trap["inside"] == 1 and trap["live"] == 0 and trap["shown"] == "none", trap)
        links = await page.evaluate("Array.from(document.querySelectorAll('footer a')).map(a => [a.textContent, a.href, a.rel])")
        check("blocked: the Xibalba line shows the two project links",
              [l[1] for l in links] == ["https://github.com/MaMoja/xibalba", "https://github.com/sponsors/MaMoja"]
              and all(l[2] == "noopener noreferrer" for l in links), links)
        reachable = []
        for _ in range(4):
            await page.keyboard.press("Tab")
            reachable.append(await page.evaluate("document.activeElement.tagName + ':' + (document.activeElement.textContent || '')"))
        check("blocked: both links can be reached with the keyboard", sum(1 for r in reachable if r.startswith("A:")) >= 2, reachable)
        if shots:
            await page.screenshot(path=os.path.join(shots, "blocked.png"))
        await ctx.close()

        # 5. Automated accessibility check (WCAG 2.1 A and AA, plus best practices).
        if axe_source:
            strip = lambda html: re.sub(r"<script>.*?</script>", "", html, flags=re.S)
            challenge = strip(fetch(base + "/wiki/a.html", "de"))
            working = challenge.replace('aria-live="polite" hidden>', 'aria-live="polite">Die Prüfung läuft …') \
                               .replace('<div id="xibalba-manual">', '<div id="xibalba-manual" hidden>')
            variants = {
                "challenge, button shown": challenge,
                "challenge, script working": working,
                "challenge, English": strip(fetch(base + "/wiki/a.html", "en")),
                "blocked": fetch(base + "/admin", "de"),
            }
            for scheme in ["light", "dark"]:
                ctx = await browser.new_context(color_scheme=scheme, bypass_csp=True, viewport={"width": 900, "height": 700})
                page = await ctx.new_page()
                for name, html in variants.items():
                    await page.set_content(html)
                    await page.evaluate("document.querySelectorAll('details').forEach(d => d.open = true)")
                    await page.add_script_tag(content=axe_source)
                    result = await page.evaluate("axe.run(document, {runOnly: ['wcag2a','wcag2aa','wcag21a','wcag21aa','best-practice']})")
                    violations = [(v["id"], v["impact"], v["nodes"][0]["html"][:80]) for v in result["violations"]]
                    check(f"accessibility/{scheme}: {name} ({len(result['passes'])} rules passed)", not violations, violations)
                await ctx.close()
        else:
            print("SKIP  accessibility check: pass --axe path/to/axe.min.js to run it")

        await browser.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--binary", default="bin/xibalba")
    parser.add_argument("--axe", default=os.environ.get("AXE_JS", ""))
    parser.add_argument("--screenshots", default="")
    args = parser.parse_args()

    if not os.path.exists(args.binary):
        raise SystemExit(f"{args.binary} not found: run 'make build' first")
    axe_source = open(args.axe).read() if args.axe else ""
    if args.screenshots:
        os.makedirs(args.screenshots, exist_ok=True)

    site_port, public_port, ops_port = free_port(), free_port(), free_port()
    with tempfile.TemporaryDirectory() as tmp:
        os.makedirs(os.path.join(tmp, "site", "wiki"))
        for name in ["start", "other", "working", "noscript", "a"]:
            with open(os.path.join(tmp, "site", "wiki", name + ".html"), "w") as f:
                f.write("<!doctype html><title>Site</title><h1>My website</h1>")
        config = os.path.join(tmp, "xibalba.yaml")
        with open(config, "w") as f:
            f.write(f"""upstream:
  url: "http://127.0.0.1:{site_port}"
server:
  listen: "127.0.0.1:{public_port}"
ops:
  listen: "127.0.0.1:{ops_port}"
trap:
  enabled: true
challenge:
  wait: 1s
rules:
  default_action: challenge
  list:
    - name: block-admin
      match:
        path: {{prefix: "/admin"}}
      action: deny
""")
        site = subprocess.Popen([sys.executable, "-m", "http.server", str(site_port), "--bind", "127.0.0.1",
                                 "--directory", os.path.join(tmp, "site")], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        xibalba = subprocess.Popen([args.binary, "-config", config], stderr=subprocess.DEVNULL)
        try:
            wait_for(f"http://127.0.0.1:{site_port}/")
            wait_for(f"http://127.0.0.1:{ops_port}/healthz")
            asyncio.run(run(f"http://127.0.0.1:{public_port}", axe_source, args.screenshots))
        finally:
            xibalba.terminate()
            site.terminate()
            xibalba.wait()
            site.wait()

    failed = RESULTS.count(False)
    print(f"\n{len(RESULTS) - failed} passed, {failed} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
