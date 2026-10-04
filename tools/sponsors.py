#!/usr/bin/env python3
"""Keep the list of sponsors in README.md up to date.

Asks GitHub who sponsors the project and writes the public sponsors between
the two marker lines in README.md: from 25 dollars a month with picture, from
10 dollars a month by name. A sponsor who chose to stay private on GitHub is
never listed. Names and pictures are those of the sponsor's GitHub account.

Usage:
  tools/sponsors.py                 ask GitHub (needs SPONSORS_TOKEN), rewrite README.md
  tools/sponsors.py --from FILE     take GitHub's answer from a JSON file instead (tests)
  tools/sponsors.py --check ...     only say whether README.md would change
"""
import html
import json
import os
import pathlib
import re
import sys
import urllib.request

LOGIN = "MaMoja"
NAME_FROM, PICTURE_FROM = 10, 25  # dollars a month
START, END = "<!-- sponsors:start -->", "<!-- sponsors:end -->"
README = pathlib.Path(__file__).resolve().parent.parent / "README.md"
VALID_LOGIN = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$")

QUERY = """
query($login: String!, $after: String) {
  user(login: $login) {
    sponsorshipsAsMaintainer(first: 100, after: $after, activeOnly: true, includePrivate: true) {
      pageInfo { hasNextPage endCursor }
      nodes {
        privacyLevel
        tier { monthlyPriceInDollars isOneTime }
        sponsorEntity {
          ... on User { login name }
          ... on Organization { login name }
        }
      }
    }
  }
}
"""


def ask_github(token: str) -> list:
    nodes, after = [], None
    while True:
        body = json.dumps({"query": QUERY, "variables": {"login": LOGIN, "after": after}}).encode()
        request = urllib.request.Request("https://api.github.com/graphql", data=body, headers={
            "Authorization": "bearer " + token, "Content-Type": "application/json", "User-Agent": "xibalba-sponsors"})
        with urllib.request.urlopen(request, timeout=30) as response:
            answer = json.load(response)
        if answer.get("errors"):
            # The message may quote the query, never the token.
            raise SystemExit("GitHub refused the question: " + "; ".join(e.get("message", "?") for e in answer["errors"]))
        page = answer["data"]["user"]["sponsorshipsAsMaintainer"]
        nodes += page["nodes"]
        if not page["pageInfo"]["hasNextPage"]:
            return nodes
        after = page["pageInfo"]["endCursor"]


def public_sponsors(nodes: list) -> list:
    """(login, name, dollars) of every sponsor who may be named, largest first."""
    out = {}
    for node in nodes:
        tier, who = node.get("tier") or {}, node.get("sponsorEntity") or {}
        login = who.get("login") or ""
        if node.get("privacyLevel") != "PUBLIC" or tier.get("isOneTime") or not VALID_LOGIN.match(login):
            continue
        dollars = int(tier.get("monthlyPriceInDollars") or 0)
        if dollars >= NAME_FROM and dollars >= out.get(login, ("", 0))[1]:
            out[login] = ((who.get("name") or login).strip()[:80] or login, dollars)
    return sorted(((login, name, dollars) for login, (name, dollars) in out.items()),
                  key=lambda s: (-s[2], s[0].lower()))


def render(sponsors: list) -> str:
    if not sponsors:
        return "No public sponsors yet. [Be the first.](https://github.com/sponsors/%s)" % LOGIN
    lines = []
    pictured = [s for s in sponsors if s[2] >= PICTURE_FROM]
    named = [s for s in sponsors if s[2] < PICTURE_FROM]
    if pictured:
        lines.append("<p>")
        for login, name, _ in pictured:
            safe = html.escape(name, quote=True)
            lines.append('<a href="https://github.com/%s" title="%s"><img src="https://github.com/%s.png?size=96" width="64" height="64" alt="%s"></a>'
                         % (login, safe, login, safe))
        lines.append("</p>")
        lines.append("")
    if named:
        lines.append("Backers: " + ", ".join('<a href="https://github.com/%s">%s</a>' % (login, html.escape(name))
                                             for login, name, _ in named))
    return "\n".join(lines).rstrip()


def rewrite(text: str, block: str) -> str:
    if text.count(START) != 1 or text.count(END) != 1 or text.index(START) > text.index(END):
        raise SystemExit("README.md needs the lines %s and %s, once each and in this order" % (START, END))
    head, rest = text.split(START)
    _, tail = rest.split(END)
    return head + START + "\n" + block + "\n" + END + tail


def main() -> None:
    args = sys.argv[1:]
    check = "--check" in args
    if "--from" in args:
        nodes = json.loads(pathlib.Path(args[args.index("--from") + 1]).read_text())
    else:
        token = os.environ.get("SPONSORS_TOKEN", "")
        if not token:
            print("::warning::SPONSORS_TOKEN is not set: nothing done. See docs/MAINTAINING.md.")
            return
        nodes = ask_github(token)
    listed = public_sponsors(nodes)
    if os.environ.get("GITHUB_ACTIONS"):
        # Shown on the run's page. Numbers only: private sponsors stay private.
        print("::notice::GitHub answered: %d sponsorships, %d listed in the README" % (len(nodes), len(listed)))
    old = README.read_text()
    new = rewrite(old, render(listed))
    if new == old:
        print("The list of sponsors is up to date.")
    elif check:
        print("The list of sponsors would change.")
    else:
        README.write_text(new)
        print("The list of sponsors was updated.")


if __name__ == "__main__":
    main()
