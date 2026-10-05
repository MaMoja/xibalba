# Privacy

The facts you or your data protection officer need for your own assessment.
This is not legal advice.

| Question | Answer |
|---|---|
| Does Xibalba store IP addresses? | Not on disk. The address is used while a request is handled and is written to no file. In memory there are exceptions, each only when you switch the feature on; see the next rows. |
| Does Xibalba log who asks for what? | No. There is no access log. Even the most detailed log level (`debug`) names only the rule for a decision, not the address, path or user agent. One exception: if a fault in the program occurs while a request is handled, the path of that one request is logged to find the fault, but not the address. |
| With crawler rules? | For requests that claim to be a crawler verified by reverse DNS (Bingbot, Applebot), Xibalba remembers the result for that address in memory for up to 24 hours, so it does not ask again each time. Ordinary visitors are not affected. A restart forgets it. |
| With request limits? | Xibalba remembers clients' addresses in memory in order to count. Nothing of it is written to a file or the log. A client that sends nothing more is forgotten after twice the longest period configured: with a limit per minute after two minutes, with a limit per day after two days at most. A restart forgets everything. Addresses on the exempt list are not remembered at all. A limit on pages remembers how many different pages a client was given, not which. |
| With the trap? | Xibalba remembers in memory the addresses of clients that followed the hidden link, for `trap.remember` (24 hours by default, 30 days at most). Whoever does not follow the link is not recorded. Nothing is written to a file or the log. |
| With rules about countries? | The country is read on your server from the database file; no visitor's address leaves the server for it. Only if you switch on `countries.download` does Xibalba fetch the database from the provider once a month; the provider then sees your server's address. |
| What is counted? | How often each rule decided, without any link to persons. By default in memory only, until the next restart. With `statistics.directory` also in files by the hour, for `statistics.keep_days` days (400 by default): counts under the names of rules, crawlers and limits only; no address, path or user agent. |
| Does the web interface store anything about visitors? | No. It is for the operator only, is off by default, and shows the same counts. It sets one cookie for the operator's own login. |
| Are addresses stored when the operator blocks one in the web interface? | Yes, exactly those the operator entered, in the changes file, until removed or until the entry's end date (30 days unless chosen otherwise). They are deliberate entries, like an address in a rule. The log does not name them. |
| Are networks of origin stored? | Not by default. With `statistics.networks.enabled`, the largest networks of each hour (IPv4 `/24`, IPv6 `/48`; never a single address) are kept with counts for `statistics.networks.keep_days` (30 days unless changed, and at most a day longer). Switching the option off removes them at the next start. |
| With rules by network operator or address list? | The operator is read on your server from the database file, the list from your file; no visitor's address is sent anywhere and nothing about a lookup is stored. With `asn.download`, Xibalba fetches the database from the provider once a month; that request says nothing about visitors. |
| With link previews? | Xibalba asks your own website for the pages whose preview tags it shows, and keeps those tags in memory. A fetch carries the address of the page and nothing about the visitor whose request caused it. No connection to anyone else. |
| Does Xibalba set a cookie? | Only for visitors who passed the security check. |
| What is in the cookie? | An expiry time and a check value that ties it to the visitor's network and browser. The check value is a keyed hash; address and user agent cannot be recovered from it. No identifier of the person, nothing about pages visited. |
| What is the cookie for? | Only to avoid checking a visitor again after a passed check. |
| How long is it valid? | `challenge.pass_lifetime`, a week by default. |
| Does my website see the cookie? | No. Xibalba removes it before passing a request on. |
| Is data sent to third parties? | No. Xibalba's pages load nothing from other servers: no fonts, no scripts, no images. Without crawler rules, a country download or the like, Xibalba itself makes no outgoing connection except to your website. |
| Which outgoing connections can there be? | (1) With crawler rules: the address lists published by crawler operators (OpenAI, Anthropic, Perplexity, Google, DuckDuckGo, Common Crawl) are downloaded. Your server's address and the user agent `Xibalba/<version>` are sent, nothing about your visitors. Switch off with `crawlers.refresh: false`. (2) With crawler rules: for requests that claim to be Bingbot or Applebot, your server's DNS resolver is asked for the name of the requesting address; that address thereby reaches your resolver. (3) With `countries.download: true`: the country database, once a month. |
| What about the line "Protected by Xibalba"? | It holds two ordinary links to GitHub. Showing the page loads nothing from there. Only if a visitor clicks one of the links does their browser contact GitHub; the page they come from is not passed on. With a sponsor license the line can be switched off. |
| Is the license checked with anybody? | No. It is checked on your machine only. |
| What does my website receive in addition? | The visitor's address in the headers `X-Forwarded-For` and `X-Real-IP`, as from any web server in front. What your website does with it is up to you. |

The check page itself tells the visitor about the cookie.
