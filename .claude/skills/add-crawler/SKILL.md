---
name: add-crawler
description: Use when adding or updating a crawler or bot definition in Xibalba's data/crawlers files, or when refreshing the crawler list.
---

# Add or update a crawler definition

Crawler names and IP ranges change often and are easy to get wrong from memory.
A wrong allow entry is a hole; a wrong deny entry blocks a customer's search traffic.

1. **Find the operator's own documentation** for the crawler (not a blog post or
   a third-party list). Fetch it now; do not rely on what you remember.
2. **Record from that page:**
   - the exact user agent token,
   - what the crawler is for, in the operator's words,
   - how to verify it: URL of the published IP range file, or the reverse DNS
     domain, or "none published",
   - whether it honours robots.txt.
3. **Pick the class** by purpose:
   - `training`: collects content for model training,
   - `ai-search`: builds an index for answers that link to sources,
   - `user-fetch`: fetches because a person just asked,
   - `search-engine`: classic search,
   - `archive`: builds a public copy of the web for anyone to download,
   - `other`: anything else (SEO tools, previews, monitors).
   One operator usually has several crawlers in different classes. Add each separately.
4. **Write the entry** in `data/crawlers/<operator>.yaml` with `source` (URL) and
   `checked` (today's date).
   The format is described in `docs/CRAWLERS.md` ("Your own crawlers"):
   `verify.ranges_url` (https only), `verify.ranges`, `verify.reverse_dns`.
5. **No verification method published, or its format not confirmed?** Leave
   `verify` out. The engine then never treats the crawler as verified, so an
   allow preset will not let it through. Say this in the entry's `note`; a
   test requires the note.
6. **Robots.txt-only tokens** (Google-Extended, Applebot-Extended) have no
   user agent and are not crawlers. Do not add them; mention them in the note
   of the operator's main crawler.
7. **Test.** `go test ./internal/crawlers ./internal/config` checks every
   built-in entry (source, date, uniqueness) and that `docs/CRAWLERS.md` lists
   it. Add the row to the table there.
8. **Report** what was added, the class chosen and the source.
