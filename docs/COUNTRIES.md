# Countries

With a country database, rules can test which country a client's address is
registered in:

```yaml
countries:
  database: countries.mmdb
rules:
  list:
    - name: check-everyone-abroad
      match:
        not:
          country: [DE, AT, CH]
      action: challenge
```

Xibalba ships no database. You choose one, put the file next to the
configuration, and keep it current, or you let Xibalba download the free
database of DB-IP. Nothing about countries is active until you set
`countries.database` and write a rule with `country`.

## Which database

Xibalba reads the `.mmdb` format. Two free country databases use it:

| | DB-IP "IP to Country Lite" | MaxMind "GeoLite2 Country" |
|---|---|---|
| Cost | Free | Free |
| Account needed | No | Yes |
| Published | Monthly | See MaxMind |
| Licence | Creative Commons Attribution 4.0 (as stated on its download page, checked 2026-10-03) | MaxMind's own licence agreement; read it at MaxMind |
| Condition | A link "IP Geolocation by DB-IP" to db-ip.com on pages that use the results | See MaxMind's agreement |
| Download by Xibalba | Yes, `countries.download: true` | No: the download needs your account. Use MaxMind's own update tool; Xibalba picks up the new file. |
| Where | https://db-ip.com/db/download/ip-to-country-lite | https://www.maxmind.com |

**The licence is yours to meet.** Xibalba only reads the file. If you use the
DB-IP database, DB-IP asks for the attribution link named above; where you
place it (for example in your site's imprint or privacy notice) is for you to
decide. Read the terms on the provider's page before you start; they may have
changed since this was written.

Commercial databases in the same format work as well. The reader looks for
the country code under `country.iso_code`, then `country_code`, then
`country`.

For rules by network operator (hosting companies, clouds) and by address
lists (VPN exits), see [NETWORKS.md](NETWORKS.md).

## Settings

| Setting | Meaning |
|---|---|
| `countries.database` | The database file, relative to the configuration file. Empty: no countries. |
| `countries.download` | `true`: download the database when the file is missing or a month old. Default `false`. |
| `countries.download_url` | Where to download from. Default: DB-IP's free country database. |

Defaults and allowed values are in [CONFIGURATION.md](CONFIGURATION.md#countries).

## Keeping the database current

Addresses change hands. A database that is a year old is wrong in many places.

- **By hand or with your own job:** replace the file. Write the new file
  under another name and rename it into place, so Xibalba never reads half a
  file. Xibalba notices the new file within a minute; no restart is needed.
- **By Xibalba:** `countries.download: true`. The file is fetched over HTTPS
  when it is missing or a month old, checked, and only then put in place. The
  directory of the file must be writable for Xibalba's user. A failed download
  is tried again after 5, 10 and 15 minutes, then every six hours; the
  database in place stays in use.

The component `countries` in `/healthz` is `degraded` when no database is
loaded, when the last download or file was unusable, or when the data is more
than 100 days old.

**Not tested against the live download.** The development environment cannot
reach db-ip.com. The download was tested against a local server, and the
reader against the test databases that MaxMind publishes with the format
description. After the first start, check `/healthz` and the log line
`country database loaded`.

## Writing rules

`country` takes a list of ISO 3166-1 codes of two letters, in either case.
The condition holds if the client's address is registered in one of them.

```yaml
countries:
  database: countries.mmdb
rules:
  list:
    # Your own networks first, whatever the database says about them.
    - name: allow-office
      match:
        ip: ["192.0.2.0/24"]
      action: allow

    # Deny some countries outright.
    - name: deny-countries
      match:
        country: [XX, YY]
      action: deny

    # Score instead of deciding.
    - name: weigh-abroad
      match:
        not:
          country: [DE, AT, CH]
      action: weigh
      weight: 5
```

Things to know:

- **The United Kingdom is `GB`,** not `UK`. Xibalba refuses `UK` and `EU`
  with a hint. Other two-letter combinations are accepted as written, so
  check your codes.
- **Unknown addresses are in no country.** Private addresses (10.x,
  192.168.x), this machine itself and addresses missing from the database
  have no country. `country: [DE]` does not match them; `not: {country:
  [DE]}` does. If you restrict "everyone except", put an `allow` rule for
  your own networks first, as above.
- **While no database is loaded, rules with a country condition are skipped
  completely.** A rule such as "deny everyone outside Germany" would
  otherwise shut out everybody because a file is missing. Health shows the
  state.
- **A country is where an address is registered,** not where a person is.
  VPNs, mobile networks, company networks and cloud servers put people and
  programs in "other" countries. Prefer `challenge` or `weigh` to `deny`
  when you restrict by country: a person behind a foreign address can then
  still get in.
- A crawler that rents servers in an allowed country is not stopped by this.

A message for a wrong code looks like this:

```text
  - line 11, rules.list[0].match.not.country[3]: "UK" is not the code of the United Kingdom
    fix: use "GB"
```

## Cost and privacy

- A lookup takes less than a microsecond and reads from memory. The database
  is loaded into memory as a whole (the free country databases are tens of
  megabytes) and only if a rule uses `country`.
- No client address leaves your server for this. The lookup is local.
- The download, if switched on, tells the database provider your server's
  address and the user agent `Xibalba/<version>`, once a month.
