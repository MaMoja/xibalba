# Rules by network: operators and address lists

Besides single addresses (`ip`) and countries (`country`), a rule can ask
two more things about where a request comes from:

- **`asn`**: which network operator the address belongs to. Every network
  on the internet is run by an operator with a number (an "autonomous
  system number", AS number): an internet provider, a university, a
  hosting company, a cloud. "All addresses of hosting company X" is one
  number instead of hundreds of networks.
- **`address_list`**: whether the address is in a list you keep in a file.
  For lists that are long or change often: exits of VPN providers, Tor
  exits, a blocklist you get from elsewhere.

Both are off until you configure them, and both are answered from memory:
no request waits for a file or a network.

## What this is for, and what it cannot do

Typical use: requests from hosting companies and VPNs are far more often
automated than requests from home and mobile connections, so they get the
security check, or a harder one, while everyone else passes.

Xibalba does not know by itself which operators are hosting companies or
which addresses are VPN exits. **It ships no such list**: there is no
authoritative one, the lists that exist disagree, and a wrong entry locks
people out. You choose the numbers and the lists; Xibalba applies them
fast.

People use VPNs for good reasons, and whole organisations reach the
internet through a cloud provider's addresses. Prefer `challenge` to `deny`
for these rules.

## Network operators (`asn`)

### The database

Xibalba reads the `.mmdb` format, as for countries. Free databases of
network operators in that format:

| | DB-IP "IP to ASN Lite" | MaxMind "GeoLite2 ASN" |
|---|---|---|
| Account needed | No | Yes |
| Licence and conditions | Stated on its download page; read them there | MaxMind's own licence agreement |
| Download by Xibalba | `asn.download: true`. The default address follows the pattern of DB-IP's country database and has **not been tried against the real server yet**; if it fails, `/healthz` says so, and you can fetch the file yourself. | No: the download needs your account. Use MaxMind's update tool; Xibalba picks up the new file. |

The licence is yours to meet, as with the country database
([COUNTRIES.md](COUNTRIES.md#which-database)).

The reader looks for the number under `autonomous_system_number`, then
under `asn` (a number, or text such as `AS64500`). A database that holds
no operators, such as a country database, is never put in use: not at
start, where the message says what the file is, and not when it turns up
later as a replaced file or a download. The database in use stays, and
`/healthz` says why.

### Settings

| Setting | Meaning |
|---|---|
| `asn.database` | The database file, relative to the configuration file. Empty: operators are not known. |
| `asn.download` | `true`: download the database when the file is missing or a month old. Default `false`. |
| `asn.download_url` | Where to download from. `{year}` and `{month}` are filled in. |

The file is read again within a minute when it changes; no restart is
needed. The database is only loaded if a rule asks for an operator, or if
rules can be added in the web interface (`admin.allow_changes`).

### Rules

```yaml
asn:
  database: asn.mmdb
rules:
  list:
    - name: check-hosting
      match:
        asn: [64500, 64501]
      action: challenge
```

- Write the numbers without "AS": `64500`, not `AS64500`. Up to 20000
  numbers in one condition.
- To find an operator's number, look up one of its addresses in a "whois"
  service; the answer names the "origin" or "AS".
- An address whose operator the database does not know belongs to none:
  `asn: [...]` does not hold for it, `not: {asn: [...]}` does.
- While no database is loaded (the file is missing or damaged), rules with
  an `asn` condition are skipped altogether, so that a missing file cannot
  lock everybody out. That goes for rules that let through as well: "allow
  operator X, deny the rest" denies X too until the database is back.
  `/healthz` shows the part `asn` as `degraded`.
- An operator is as broad as a country: a rule that only names an operator
  cannot carry `exempt_from_limits`.

## Address lists (`address_list`)

```yaml
rules:
  address_lists:
    vpn: lists/vpn.txt
    tor: lists/tor-exits.txt
  list:
    - name: check-vpn-and-tor
      match:
        address_list: [vpn, tor]
      action: challenge
```

### The file

One address or network per line, IPv4 or IPv6:

```
# exits of a VPN provider
192.0.2.0/24
198.51.100.7
2001:db8::/32      # comments may follow an entry
```

- Empty lines are skipped, and so is everything from `#` or `;` on.
- Only the first field of a line is read, so tables work as they are:
  `203.0.113.0/24,AS64500,Example` counts as `203.0.113.0/24`.
- Up to 2,000,000 entries and 64 MiB per file, up to 32 lists; a line has
  at most 512 characters.
- A name is 1 to 40 small letters, digits, `-` and `_`.

A list decides who is checked or refused, so a line is only taken if it can
mean one thing. These stop Xibalba from starting, with file and line:

| Line | Why it is refused |
|---|---|
| `198.51.100.1 - 198.51.100.99`, `10.0.0.0 255.0.0.0` | A range or a mask: only the first address would be read. Write a network. |
| `10.0.0.5/8` | Bits set beyond the length. If the network is meant, write `10.0.0.0/8`. |
| `0.0.0.0/0`, `128.0.0.0/1`, `2000::/3` | Wider than /8 (IPv4) or /16 (IPv6): a large part of the internet, surely a slip. |
| `fe80::1%eth0` | An address with a zone has no meaning in a list. |
| `vpn.example.org` | Not an address. Names are not looked up. |

From a real run:

```
configuration x.yaml: 2 problems
  - line 5, rules.address_lists.vpn: lists/vpn.txt, line 3: holds a second address after 198.51.100.1; a range or an address with a mask is written as a network, such as 192.0.2.0/24
    fix: write one address or network per line, such as "192.0.2.7" or "2001:db8::/32"; text after "#" is ignored
  - line 5, rules.address_lists.vpn: lists/vpn.txt, line 4: "10.0.0.5/8" has bits set beyond its length; if the network is meant, write 10.0.0.0/8
    fix: write one address or network per line, such as "192.0.2.7" or "2001:db8::/32"; text after "#" is ignored
```

**An empty file is a valid list.** A nightly fetch that fails and leaves an
empty file turns "in the list" into never, and `not: {address_list: …}`
into always. Xibalba starts and writes a warning to the log for each list
without entries; have your fetch script check that the file is not empty
before it replaces the old one.

### Keeping a list current

**The files are read when Xibalba starts.** After you replace a file,
restart Xibalba (`systemctl restart xibalba`). A list fetched every night
by a timer or cron job therefore needs the restart as its last step. Check
the new file first, so that a broken download does not stop the service:

```
xibalba -check -config /etc/xibalba/xibalba.yaml && systemctl restart xibalba
```

Reading changed lists without a restart is planned.

### Where lists come from

- **Cloud providers** publish their own address ranges. Convert them to
  one network per line.
- **The Tor project** publishes the addresses of its exits.
- **VPN and data-centre lists** are collected by volunteers and companies,
  for example the project "lists_vpn" by X4BNet on GitHub. Xibalba's
  authors have not checked any of them. Look at who maintains a list, how
  often it changes and what it would do to your visitors before you use it.

### Cost

A list is sorted once at start; a lookup is then a search by halving, well
under a microsecond whatever the size. Measured on the development machine
with random addresses: 0.16 µs in a list of 500,000 networks, 0.3 µs in
2,000,000 IPv4 entries, 0.8 µs in 2,000,000 IPv6 entries. Memory: 8 bytes
per IPv4 entry, 32 per IPv6 entry. Reading 2,000,000 entries at start takes
one to two seconds.

## Both together, with countries

"Everyone outside Germany, Austria and Switzerland, everyone on a VPN and
everyone from a hosting company has to pass the security check":

```yaml
countries:
  database: countries.mmdb
asn:
  database: asn.mmdb
rules:
  address_lists:
    vpn: lists/vpn.txt
  list:
    - name: check-vpn
      match:
        address_list: [vpn]
      action: challenge
    - name: check-hosting
      match:
        asn: [64500, 64501]
      action: challenge
    - name: check-abroad
      match:
        not:
          country: [DE, AT, CH]
      action: challenge
```

Each rule can ask for a check of its own (a harder calculation, extra
checks); see [CHALLENGE.md](CHALLENGE.md#a-check-of-its-own-for-a-rule).
Put rules that let someone through, such as your own office network or
verified search engines, before these.

## Privacy

Both lookups happen on your server. No address is sent anywhere, and
nothing about a lookup is stored.
