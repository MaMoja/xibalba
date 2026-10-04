#!/usr/bin/env python3
"""Build the pages of the GitHub wiki from docs/.

docs/ is the only place where documentation is written. This script copies
each page under its wiki name, rewrites the links between pages, points links
to files of the repository at GitHub, and writes Home, the sidebar and the
footer. Usage: tools/wiki.py OUTPUT_DIRECTORY
"""
import pathlib
import re
import sys

REPO = "https://github.com/MaMoja/xibalba"
ROOT = pathlib.Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"

# Sections of the sidebar: (heading, [(file in docs/, wiki page name, one line)]).
SECTIONS = [
    ("Design", [
        ("SPEC.md", "Specification", "What Xibalba is meant to do"),
        ("CHALLENGE.md", "Challenge", "The security check: how it works, its settings"),
        ("PARITY.md", "Parity", "What Anubis offers and where Xibalba stands"),
    ]),
    ("Administrative guides", [
        ("GETTING-STARTED.md", "Getting-Started", "Install and set up, step by step"),
        ("ENVIRONMENTS.md", "Environments", "nginx, Caddy, Apache, HAProxy, Traefik, Docker, systemd, Kubernetes"),
        ("CONFIGURATION.md", "Configuration", "Every setting, its default and its allowed values"),
        ("RULES.md", "Rules", "How to write rules and how they are evaluated"),
        ("CRAWLERS.md", "Crawlers", "Crawler classes, presets, how identity is verified"),
        ("LIMITS.md", "Limits", "Request limits per client"),
        ("COUNTRIES.md", "Countries", "Rules by country"),
        ("TRAP.md", "Trap", "The hidden link that catches crawlers, and the maze"),
        ("STATISTICS.md", "Statistics", "Counters kept on disk by the hour"),
        ("METRICS.md", "Metrics", "The numbers for a monitoring system"),
        ("OPERATIONS.md", "Operations", "Running, updating, troubleshooting"),
        ("PRIVACY.md", "Privacy", "What is stored and what is not"),
        ("SPONSORS.md", "Sponsors", "What is free and what the sponsor license adds"),
        ("FAQ.md", "FAQ", "Questions operators ask"),
        ("de/HANDBUCH.md", "Handbuch", "The operator handbook in German"),
    ]),
    ("User guides", [
        ("VISITORS.md", "Why-am-I-seeing-a-security-check", "For visitors of a protected website"),
    ]),
    ("Developer guides", [
        ("ARCHITECTURE.md", "Architecture", "How the program is divided and how failures are contained"),
        ("DEVELOPMENT.md", "Development", "Building, testing, adding a component"),
        ("MAINTAINING.md", "Maintaining", "Releases and the license key"),
        ("DECISIONS.md", "Decisions", "Why things are the way they are"),
        ("ROADMAP.md", "Roadmap", "What is built and what comes next"),
    ]),
]

NAMES = {src: name for _, pages in SECTIONS for src, name, _ in pages}
LINK = re.compile(r"(\]\()([^)\s]+)(\))")


def rewrite(text: str, source: pathlib.Path) -> str:
    """Rewrite the relative links of one page."""

    def one(match: re.Match) -> str:
        target = match.group(2)
        if re.match(r"[a-z]+:|#", target):
            return match.group(0)
        path, _, anchor = target.partition("#")
        resolved = (source.parent / path).resolve()
        try:
            in_docs = resolved.relative_to(DOCS).as_posix()
        except ValueError:
            in_docs = None
        if in_docs in NAMES:
            new = NAMES[in_docs]
        else:
            if not resolved.exists():
                raise SystemExit(f"{source}: link to {target} leads nowhere")
            kind = "tree" if resolved.is_dir() else "blob"
            new = f"{REPO}/{kind}/main/{resolved.relative_to(ROOT).as_posix()}"
        return match.group(1) + new + ("#" + anchor if anchor else "") + match.group(3)

    return LINK.sub(one, text)


def main() -> None:
    out = pathlib.Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    for old in out.glob("*.md"):
        old.unlink()

    listed = set(NAMES)
    present = {p.relative_to(DOCS).as_posix() for p in DOCS.rglob("*.md")}
    if listed != present:
        raise SystemExit(f"tools/wiki.py and docs/ differ: {sorted(listed ^ present)}")

    for src, name in NAMES.items():
        source = DOCS / src
        note = f"\n\n---\n*This page is built from [`docs/{src}`]({REPO}/blob/main/docs/{src}). To change it, change that file.*\n"
        (out / f"{name}.md").write_text(rewrite(source.read_text(), source) + note)

    home = [
        "# Xibalba", "",
        "Xibalba is a small program that stands in front of a website and keeps AI crawlers "
        "and other abusive bots away, without sending anything about visitors to a third party. "
        "It is one file without dependencies, runs on a Raspberry Pi, and is free software.", "",
        "New here? Start with [Getting started](Getting-Started). "
        "Sent here by a security check? Read [Why am I seeing a security check?](Why-am-I-seeing-a-security-check).", "",
    ]
    side = ["**[Home](Home)**", ""]
    for heading, pages in SECTIONS:
        home += [f"## {heading}", "", "| Page | What it covers |", "|------|----------------|"]
        side += [f"**{heading}**", ""]
        for _, name, line in pages:
            title = name.replace("-", " ")
            home.append(f"| [{title}]({name}) | {line} |")
            side.append(f"- [{title}]({name})")
        home.append("")
        side.append("")
    (out / "Home.md").write_text("\n".join(home))
    (out / "_Sidebar.md").write_text("\n".join(side))
    (out / "_Footer.md").write_text(
        f"Built from the [`docs/`]({REPO}/tree/main/docs) directory of the repository. Changes made here are overwritten.\n")
    print(f"{len(NAMES) + 3} wiki pages written to {out}")


if __name__ == "__main__":
    main()
