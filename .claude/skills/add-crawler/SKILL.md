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
   - `other`: anything else (SEO tools, archives, monitors).
   One operator usually has several crawlers in different classes. Add each separately.
4. **Write the entry** in `data/crawlers/<operator>.yaml` with `source` (URL) and
   `checked` (today's date).
5. **No verification method published?** Set `verify: none`. The engine then
   treats a name match as unidentified, so an allow preset will not let it through
   on the name alone. Say this in the entry's note.
6. **Test.** Add a case to the crawler data tests: a request with the right name
   from a verified address, and one with the right name from a wrong address.
7. **Report** what was added, the class chosen and the source.
