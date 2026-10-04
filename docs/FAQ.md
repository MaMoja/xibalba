# Questions and answers

For operators. Visitors who met a check page: see
[Why am I seeing a security check?](VISITORS.md)

## Does Xibalba stop every crawler?

No. It stops crawlers that say who they are, crawlers that cannot pass the
security check, crawlers that ask for too much, and crawlers that follow
every link. A crawler that drives a real browser slowly from many addresses
looks like a visitor and gets through. The aim is to make crawling your site
expensive, not impossible.

## Will search engines still find my site?

Yes, if you let them through. The presets `allow-search-engines`,
`allow-ai-search` and `allow-ai-user-fetch` let the genuine ones through
before anything checks or denies. Genuine means the request comes from the
operator's own addresses; the name alone is not believed. See
[Crawlers](CRAWLERS.md).

## What do visitors notice?

With `challenge`, a page for a few seconds at their first visit, then nothing
for a week. Without JavaScript they wait a moment and press a button. With
only `deny` rules for named crawlers, visitors notice nothing at all.

## Does it work without JavaScript?

Yes. The check has a path with a short wait and a button. You can switch
that path off (`challenge.no_javascript: deny`) if you want to require
JavaScript.

## Is it accessible?

The pages are checked against WCAG 2.1 AA with an automated tool and by
keyboard, in light and dark mode, in German and English. They have not yet
been tested with a screen reader by a person.

## Can a visitor behind the same address as a crawler get locked out?

With `deny` rules on addresses or `deny` limits, yes: everybody at that
address is treated alike. That is why the documentation recommends
`challenge` wherever people may be affected. A person passes the check and
carries on.

## What about API clients, feed readers, git, monitoring?

Programs cannot pass the check. `challenge-browsers` only checks what says it
is a browser, so programs that say what they are pass. For stricter setups
there are presets for feeds, git and container registries, and you can allow
your own clients by address. See [Rules](RULES.md#presets).

## How much does it cost in speed?

Deciding about a request takes a few microseconds. Xibalba makes no network
call while a request waits. It runs on a Raspberry Pi.

## Does it need a database or other services?

No. One program, one configuration file. Statistics, if switched on, are
plain text files.

## Can I run several websites behind one Xibalba?

Not yet: one Xibalba protects one website. Run one per website. Several
websites chosen by host name are planned.

## Can I change how the pages look and what they say?

A contact line and the default language are free to set. Your own name,
your own wording and removing the "Protected by Xibalba" line need a sponsor
license. Everything that protects your site is free. See
[Sponsors](SPONSORS.md).

## How is this different from Anubis?

Xibalba is a separate program written from scratch, inspired by Anubis. The
comparison, feature by feature, is in [Parity](PARITY.md). In short: Xibalba
answers honestly when it blocks, verifies the identity of AI crawlers and not
only of search engines, trusts forwarding headers only from proxies you name,
has request limits, and documents what it stores. Anubis has more languages,
published packages and a mode in which the web server only asks for a
verdict.

## Where do I report a security problem?

See `SECURITY.md` in the repository. Please do not open a public issue for it.
