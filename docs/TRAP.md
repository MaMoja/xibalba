# The trap

Some crawlers collect every address they find in the text of a page, whether
a person could ever click it or not. The trap uses that: Xibalba hides a link
in its own pages that no person can see or reach. Whoever requests it is a
program of that kind, and is remembered.

The trap is off by default.

## Switching it on

```yaml
trap:
  enabled: true
rules:
  presets: [block-trapped]
```

With this, a client that follows the hidden link is denied for the next 24
hours.

| Setting | Meaning |
|---|---|
| `trap.enabled` | `true` hides the link in the pages and remembers who follows it. |
| `trap.remember` | How long a client is remembered. Default `24h`. |
| `trap.maze` | `true` answers the link with a maze (see below). Default `false`. |
| `trap.max_clients` | How many clients are remembered at most. |

Defaults and allowed values are in [CONFIGURATION.md](CONFIGURATION.md#trap).

## What happens to a caught client

Being caught does nothing by itself. Rules decide, with the condition
`trapped`:

```yaml
trap:
  enabled: true
rules:
  thresholds:
    - {weight: 10, action: challenge}
  list:
    # Instead of denying: make caught clients pass the check.
    - name: caught-in-trap
      match:
        trapped: true
      action: weigh
      weight: 10
```

The preset `block-trapped` is the short form for "deny them".

A rule or preset with `trapped` while `trap.enabled` is `false` is reported as
a mistake at start, because it could never match.

## Where the link is, and why people do not meet it

The link is in the pages Xibalba itself shows: the security check, the block
page, "too many requests" and "website unavailable". It is not added to the
pages of your website; Xibalba does not change those.

It sits inside a `template` element. Browsers treat the content of that
element as inert: it is not displayed, not announced by screen readers, not
reachable with the keyboard, and not part of the page's links. A person would
have to open the page source and copy the address by hand. The link is
marked `nofollow`, the pages are marked `noindex`, and its address changes
with every start of Xibalba.

A crawler that reads pages the way a browser does will not follow it either.
The trap catches the careless ones.

## The maze

With `trap.maze: true` the trap does not answer "not found" but with a page
of generated text and five links that each lead to another such page, without
end. A crawler that keeps following wastes its time on nothing.

- The text is made of meaningless syllables, in no language. Nothing that
  could be read as a statement appears under your domain.
- The pages are marked `noindex, nofollow`, load nothing, and are a few
  kilobytes each. Generating one takes a few microseconds and stores nothing.
- Every request into the maze renews the catch.

The maze is off by default. Decide for yourself whether serving such pages
suits your organisation.

## Looking at what happens

`GET /trap` on the operations listener (only while the trap is on) shows how
many requests reached the trap since the start and how many clients are
remembered right now. It holds no address.

## Privacy

- A caught client is remembered by its IPv4 address or IPv6 /64, in memory
  only, for `trap.remember`. Nothing is written to a file or a log.
- Clients that never follow the link are not recorded at all.
- A restart forgets everything.

## Known limits

- Only clients that were shown one of Xibalba's pages can find the link.
- A crawler that changes its address after being caught starts afresh.
- A client is remembered by address, not by network: neighbours of a caught
  client are not affected.
- People who share an address with a caught program (a company network with
  an eager link checker) are caught with it. Use `weigh` with a check
  instead of the deny preset if that worries you.
