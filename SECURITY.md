# Security

Xibalba stands in front of other people's websites, so security problems are
treated as the highest priority.

## Reporting a problem

Please report privately through GitHub's **Report a vulnerability** button on
the repository's Security tab. Do not open a public issue for a security problem.

Include what an attacker can do, how to reproduce it, and the version or commit.
You will get an acknowledgement, and you will be told when a fix is released.

## Supported versions

Xibalba is in early development and has no stable release yet. Fixes are made
on the `main` branch.

## What counts

Examples of what to report:

- Reaching the protected website without passing the rules or the challenge.
- Forging, replaying or extending a pass token.
- Making Xibalba trust a spoofed client address or crawler identity.
- Crashing or exhausting Xibalba with a small number of requests.
- Reading or changing settings without logging in to the admin interface.
- Xibalba storing or exposing personal data it should not.
