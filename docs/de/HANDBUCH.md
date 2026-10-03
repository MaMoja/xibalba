# Xibalba: Handbuch für Betreiber

Dieses Handbuch richtet sich an die Person, die Xibalba vor einer Website
einrichtet und betreibt. Es erklärt, was Xibalba tut, wie man es in Betrieb
nimmt und **wo man was einstellt**.

Stand: Xibalba ist in früher Entwicklung. Was heute funktioniert und was noch
fehlt, steht in [Abschnitt 12](#12-was-xibalba-noch-nicht-kann). Alles in
diesem Handbuch beschreibt den heutigen Stand; was noch nicht gebaut ist, ist
als „geplant“ gekennzeichnet.

**Inhalt**

1. [Was Xibalba tut](#1-was-xibalba-tut)
2. [So ist es aufgebaut](#2-so-ist-es-aufgebaut)
3. [Installation](#3-installation)
4. [Einrichtung in sieben Schritten](#4-einrichtung-in-sieben-schritten)
5. [Wo stelle ich was ein?](#5-wo-stelle-ich-was-ein)
6. [Regeln: wer darf, wer nicht](#6-regeln-wer-darf-wer-nicht)
7. [Die Sicherheitsprüfung](#7-die-sicherheitsprüfung)
8. [Die Seiten für Besucher anpassen](#8-die-seiten-für-besucher-anpassen)
9. [Betrieb: prüfen, beobachten, ändern](#9-betrieb-prüfen-beobachten-ändern)
10. [Datenschutz](#10-datenschutz)
11. [Fehlersuche](#11-fehlersuche)
12. [Was Xibalba noch nicht kann](#12-was-xibalba-noch-nicht-kann)
13. [Weiterführende Dokumente](#13-weiterführende-dokumente)

---

## 1. Was Xibalba tut

Xibalba steht vor Ihrer Website und entscheidet bei jeder Anfrage, was mit ihr
geschieht:

| Entscheidung | Was der Anfragende erlebt |
|---|---|
| **Durchlassen** (`allow`) | Die Anfrage geht an Ihre Website, als wäre Xibalba nicht da. |
| **Prüfen** (`challenge`) | Der Browser des Besuchers löst eine kurze Rechenaufgabe. Das dauert meist unter einer Sekunde, der Besucher muss nichts tun. Danach wird er eine Woche lang nicht mehr geprüft. Einfache Crawler scheitern daran. |
| **Blockieren** (`deny`) | Der Anfragende erhält eine sachliche Seite „Diese Anfrage wurde blockiert“. Ihre Website wird nicht kontaktiert. |

Welche Anfrage welche Entscheidung bekommt, legen Sie mit **Regeln** fest.
Ohne Regeln lässt Xibalba alles durch.

Xibalba speichert dabei **nichts über Ihre Besucher**: keine IP-Adressen,
keine aufgerufenen Seiten. Es zählt nur, welche Regel wie oft entschieden hat.

## 2. So ist es aufgebaut

```mermaid
flowchart LR
    B[Besucher oder Bot] -->|HTTPS| W[Ihr Webserver<br>nginx, Caddy, Load Balancer<br>hält das Zertifikat]
    W -->|HTTP, lokal| X[Xibalba<br>Port 8080]
    X -->|HTTP, lokal| S[Ihre Website<br>z. B. Port 3000]
    A[Sie] -.->|nur lokal| O[Xibalba Betriebsport 9090<br>Zustand und Zähler]
```

Drei Dinge sind wichtig:

- **Xibalba macht selbst kein HTTPS.** Davor steht Ihr Webserver, der das
  Zertifikat hält und die Anfragen an Xibalba weiterreicht.
- **Ihre Website darf nur noch über Xibalba erreichbar sein.** Ist sie
  zusätzlich direkt aus dem Internet erreichbar, gehen Bots einfach an Xibalba
  vorbei. Lassen Sie die Website nur auf `127.0.0.1` oder in einem internen
  Netz lauschen.
- **Der Betriebsport (9090) ist nur für Sie.** Er zeigt Zustand und Zähler und
  darf nicht aus dem Internet erreichbar sein. In der Voreinstellung ist er es
  nicht.

## 3. Installation

Xibalba ist ein einzelnes Programm ohne weitere Abhängigkeiten zur Laufzeit.
Fertige Pakete gibt es noch nicht (geplant); derzeit wird es aus dem
Quelltext gebaut. Dafür brauchen Sie auf dem Rechner, auf dem Sie bauen,
[Go](https://go.dev/dl/) ab Version 1.24 sowie `git` und `make`.

```sh
git clone https://github.com/MaMoja/xibalba.git
cd xibalba
make build
```

Das Ergebnis ist die Datei `bin/xibalba`. Für einen Server mit anderer
Bauart, etwa einen Raspberry Pi, erzeugt `make cross` die Dateien
`dist/xibalba-linux-amd64` und `dist/xibalba-linux-arm64`. Auf dem Zielrechner
wird nur diese eine Datei benötigt.

Eine übliche Ablage auf einem Linux-Server:

| Was | Wohin | Hinweis |
|---|---|---|
| Programm | `/usr/local/bin/xibalba` | |
| Konfiguration | `/etc/xibalba/xibalba.yaml` | |
| Eigene Regeldateien | `/etc/xibalba/rules/` | optional |
| Schlüsseldatei | `/var/lib/xibalba/xibalba.key` | legt Xibalba selbst an; das Verzeichnis muss für den Xibalba-Benutzer beschreibbar sein |

Lassen Sie Xibalba unter einem eigenen Benutzer ohne besondere Rechte laufen.
Es braucht keine Root-Rechte, solange es nicht auf einem Port unter 1024
lauscht.

**Als Dienst starten.** Das folgende Beispiel für systemd zeigt die übliche
Form. Es ist ein Vorschlag und in der Entwicklungsumgebung nicht getestet;
eine mitgelieferte Dienstdatei ist geplant.

```ini
# /etc/systemd/system/xibalba.service   (Beispiel, nicht getestet)
[Unit]
Description=Xibalba
After=network-online.target
Wants=network-online.target

[Service]
User=xibalba
Group=xibalba
ExecStartPre=/usr/local/bin/xibalba -check -config /etc/xibalba/xibalba.yaml
ExecStart=/usr/local/bin/xibalba -config /etc/xibalba/xibalba.yaml
Restart=on-failure
StateDirectory=xibalba

[Install]
WantedBy=multi-user.target
```

Xibalba beendet sich bei `SIGTERM` sauber: laufende Anfragen dürfen noch
fertig werden (Einstellung `shutdown_timeout`).

## 4. Einrichtung in sieben Schritten

### Schritt 1: Konfigurationsdatei anlegen

Kopieren Sie die Vorlage. Sie enthält jede Einstellung mit Erklärung und
Voreinstellung.

```sh
cp xibalba.example.yaml /etc/xibalba/xibalba.yaml
```

Pflicht ist nur eine Angabe: wo Ihre Website erreichbar ist.

```yaml
upstream:
  url: "http://127.0.0.1:3000"
```

### Schritt 2: Prüfen, ohne zu starten

```sh
xibalba -check -config /etc/xibalba/xibalba.yaml
```

Xibalba prüft die gesamte Datei und nennt jeden Fehler mit Zeile, Einstellung
und Abhilfe. Die Meldungen des Programms sind derzeit englisch. Ein Beispiel:

```text
configuration xibalba.yaml: 4 problems
  - line 4, rules.default_action: "block" is not an action a default can take
    fix: use one of: allow, deny, challenge
  - line 6, rules.list[0].name: "Block Bots" is not a valid rule name
    fix: use lower-case letters, digits, dot, underscore and hyphen, starting with a letter or digit, at most 64 characters
  - line 8, rules.list[0].match.user_agent.regex: the regular expression is not valid: missing closing ): `(GPT|Claude`
    fix: Xibalba uses RE2 syntax, which has no lookahead or backreferences; for plain text use contains
  - line 13, rules.list[1].match.header.X-Forwarded-For: X-Forwarded-For is written by the client and cannot be trusted as an address
    fix: use the ip condition, which tests the address Xibalba established itself
```

Mit einer fehlerhaften Datei startet Xibalba nicht. Auch ein Tippfehler im
Namen einer Einstellung ist ein Fehler und wird nie stillschweigend übergangen.

### Schritt 3: Starten und Zustand ansehen

```sh
xibalba -config /etc/xibalba/xibalba.yaml
```

In einem zweiten Fenster:

```sh
curl http://127.0.0.1:8080/          # Ihre Website, durch Xibalba hindurch
curl http://127.0.0.1:9090/healthz   # Zustand
```

```json
{
  "state": "ok",
  "components": {
    "ops": {
      "state": "ok"
    },
    "public": {
      "state": "ok"
    },
    "rules": {
      "state": "ok"
    },
    "upstream": {
      "state": "ok"
    }
  }
}
```

### Schritt 4: Ihren Webserver davor schalten

Ihr Webserver nimmt die Anfragen aus dem Internet an und reicht sie an
Xibalba auf Port 8080 weiter. Er muss Xibalba dabei die echte Adresse des
Besuchers mitteilen (`X-Forwarded-For`) und ob die Verbindung verschlüsselt
war (`X-Forwarded-Proto`).

Die folgenden Beispiele zeigen die übliche Form. Sie sind in der
Entwicklungsumgebung nicht getestet; prüfen Sie sie gegen die Dokumentation
Ihres Webservers.

nginx:

```nginx
server {
    listen 443 ssl;
    server_name www.example.org;
    # ssl_certificate und ssl_certificate_key wie bisher

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Caddy:

```caddy
www.example.org {
    reverse_proxy 127.0.0.1:8080
}
```

### Schritt 5: Xibalba sagen, welchem Webserver es glauben darf

Die Angabe `X-Forwarded-For` ist einfacher Text, den jeder Absender selbst
schreiben kann. Xibalba glaubt ihr deshalb nur, wenn die Verbindung von einer
Adresse kommt, die Sie als Ihren eigenen Webserver eingetragen haben.

```yaml
server:
  trusted_proxies: ["127.0.0.1", "::1"]   # Webserver auf demselben Rechner
```

| Ihre Situation | Eintrag |
|---|---|
| Webserver auf demselben Rechner | `["127.0.0.1", "::1"]` |
| Load Balancer im internen Netz | dessen Adresse oder Netz, z. B. `["10.0.0.0/8"]` |
| Besucher erreichen Xibalba direkt | `[]` (leer lassen) |

Zwei typische Fehler:

- **Eintrag fehlt:** Alle Besucher scheinen dieselbe Adresse zu haben, nämlich
  die Ihres Webservers. Adressregeln greifen dann für alle oder für niemanden.
- **Zu viel eingetragen:** Wer von einer eingetragenen Adresse aus anfragt,
  kann sich als beliebiger Besucher ausgeben. Tragen Sie nur Ihre eigenen
  Server ein.

### Schritt 6: Schlüsseldatei festlegen

Für die Sicherheitsprüfung signiert Xibalba kleine Nachweise mit einem
geheimen Schlüssel. Ohne Schlüsseldatei erzeugt es bei jedem Start einen
neuen, und alle Besucher werden nach jedem Neustart erneut geprüft.

```yaml
challenge:
  key_file: /var/lib/xibalba/xibalba.key
```

Die Datei legt Xibalba beim ersten Start selbst an, nur für den eigenen
Benutzer lesbar. Das Verzeichnis muss vorhanden und für diesen Benutzer
beschreibbar sein. Behandeln Sie die Datei wie ein Passwort.

### Schritt 7: Regeln im Probelauf einführen

Schreiben Sie Ihre ersten Regeln ([Abschnitt 6](#6-regeln-wer-darf-wer-nicht))
und schalten Sie zunächst den Probelauf ein:

```yaml
rules:
  dry_run: true
```

Im Probelauf wertet Xibalba jede Anfrage aus und zählt die Entscheidungen,
lässt aber alles durch. Sehen Sie sich die Zähler eine Weile an
([Abschnitt 9](#9-betrieb-prüfen-beobachten-ändern)). Stimmen die Zahlen,
setzen Sie `dry_run: false` und starten Xibalba neu.

## 5. Wo stelle ich was ein?

Alle Einstellungen stehen in der Konfigurationsdatei. Nach jeder Änderung:
mit `-check` prüfen, dann Xibalba neu starten.

| Ich möchte … | Einstellung | Mehr dazu |
|---|---|---|
| angeben, wo meine Website läuft | `upstream.url` | Schritt 1 |
| den Port ändern, auf dem Xibalba Anfragen annimmt | `server.listen` | [Referenz](../CONFIGURATION.md#server) |
| die echte Besucheradresse hinter meinem Webserver erhalten | `server.trusted_proxies` | Schritt 5 |
| einen Bot aussperren | Regel mit `action: deny` in `rules.list` | Abschnitt 6 |
| einen Bereich nur nach Prüfung zugänglich machen | Regel mit `action: challenge` | Abschnitt 6 |
| mein eigenes Netz immer durchlassen | Regel mit `ip` und `action: allow`, ganz oben | Abschnitt 6 |
| alles prüfen, was keine Regel ausdrücklich erlaubt | `rules.default_action: challenge` | Abschnitt 6 |
| Regeln ausprobieren, ohne jemanden auszusperren | `rules.dry_run: true` | Schritt 7 |
| Regeln in eigene Dateien auslagern | `rules.files` | Abschnitt 6 |
| die Prüfung schwerer oder leichter machen | `challenge.difficulty` | Abschnitt 7 |
| festlegen, wie lange ein Besucher nicht erneut geprüft wird | `challenge.pass_lifetime` | Abschnitt 7 |
| Besucher ohne JavaScript zulassen oder abweisen | `challenge.no_javascript` | Abschnitt 7 |
| dass Besucher nach einem Neustart nicht erneut geprüft werden | `challenge.key_file` | Schritt 6 |
| meinen Namen statt „Der Betreiber dieser Website“ zeigen | `pages.operator` | Abschnitt 8 |
| eine Kontaktangabe auf der Blockseite zeigen | `pages.contact` | Abschnitt 8 |
| einen Text auf den Besucherseiten ändern | `pages.texts` | Abschnitt 8 |
| die Sprache für Besucher ohne Deutsch oder Englisch festlegen | `pages.default_language` | Abschnitt 8 |
| festlegen, was bei einem internen Fehler passiert | `rules.on_error` | [Referenz](../CONFIGURATION.md#rules) |
| mehr oder weniger ins Protokoll schreiben | `log.level`, `log.format` | [Referenz](../CONFIGURATION.md#log) |
| den Betriebsport ändern | `ops.listen` | [Referenz](../CONFIGURATION.md#ops) |

## 6. Regeln: wer darf, wer nicht

Eine Regel hat einen Namen, Bedingungen und eine Aktion:

```yaml
rules:
  list:
    - name: block-example-bot
      match:
        user_agent: {contains: "ExampleBot"}
      action: deny
```

**So wird ausgewertet:** Xibalba geht die Regeln von oben nach unten durch.
Die erste Regel, deren Bedingungen zutreffen und deren Aktion `allow`, `deny`
oder `challenge` ist, entscheidet. Die Reihenfolge zählt also: Ausnahmen
stehen über den Regeln, von denen sie ausnehmen. Trifft keine Regel zu, gilt
`rules.default_action`.

### Bedingungen

| Bedingung | prüft | Beispiel |
|---|---|---|
| `user_agent` | die Kennung, mit der sich das Programm meldet | `{contains: "ExampleBot"}` |
| `path` | den Pfad der angefragten Seite | `{prefix: "/suche"}` |
| `host` | den Namen der Website | `{equals: "intern.example.org"}` |
| `method` | die Art der Anfrage | `["POST", "PUT"]` |
| `header` | eine weitere Kopfzeile | `Accept-Language: {present: false}` |
| `ip` | die Adresse des Anfragenden | `["192.0.2.0/24"]` |
| `all`, `any`, `not` | Verknüpfungen: alle, eine davon, keine | siehe Rezepte |

Für Texte gibt es `equals` (genau gleich), `contains` (enthält), `prefix`
(beginnt mit), `suffix` (endet mit) und `regex` (regulärer Ausdruck). Groß-
und Kleinschreibung wird nicht unterschieden, außer Sie schreiben
`case_sensitive: true` dazu.

### Worauf Sie sich verlassen können

**Nur die Adresse (`ip`) stellt Xibalba selbst fest.** Die Kennung
(`user_agent`) und alle Kopfzeilen schreibt der Anfragende selbst und kann
dabei lügen.

- Eine `deny`-Regel auf eine Kennung hält Bots auf, die sich ehrlich zu
  erkennen geben. Einen Bot, der sich als Browser ausgibt, hält sie nicht auf;
  dafür ist die Sicherheitsprüfung da.
- Eine `allow`-Regel nur auf eine Kennung lässt jeden herein, der diese
  Kennung sendet. Verbinden Sie sie mit `ip` (Rezept 3).

### Rezepte

**1. Einen Bot aussperren, der sich zu erkennen gibt**

```yaml
- name: block-example-bot
  match:
    user_agent: {contains: "ExampleBot"}
  action: deny
```

**2. Das eigene Netz immer durchlassen** (ganz oben in der Liste)

```yaml
- name: allow-office
  match:
    ip: ["192.0.2.0/24", "2001:db8:1234::/48"]
  action: allow
```

**3. Einen erwünschten Bot nur von seinen echten Adressen zulassen**

```yaml
- name: block-fake-goodbot
  match:
    user_agent: {contains: "GoodBot"}
    not:
      ip: ["198.51.100.0/24"]
  action: deny
```

Wer sich „GoodBot“ nennt, aber nicht aus dem Netz des Betreibers kommt, wird
blockiert.

**4. Teure Seiten nur nach Prüfung**

```yaml
- name: challenge-search
  match:
    path: {prefix: "/suche"}
  action: challenge
```

**5. Alles prüfen, außer was ausdrücklich erlaubt ist**

```yaml
rules:
  default_action: challenge
  list:
    - name: allow-office
      match:
        ip: ["192.0.2.0/24"]
      action: allow
    - name: allow-status
      match:
        path: {equals: "/status"}
      action: allow
```

Bedenken Sie dabei die Hinweise in
[Abschnitt 7](#bevor-sie-einen-bereich-prüfen-lassen): Programme und
Suchmaschinen bestehen die Prüfung nicht.

**6. Schwache Hinweise zusammenzählen**

Manche Merkmale reichen allein nicht für eine Entscheidung, zusammen aber
schon. Geben Sie ihnen ein Gewicht und legen Sie Schwellen fest:

```yaml
rules:
  thresholds:
    - {weight: 10, action: challenge}
    - {weight: 20, action: deny}
  list:
    - name: weigh-no-language
      match:
        header:
          Accept-Language: {present: false}
      action: weigh
      weight: 10
    - name: weigh-scripted-client
      match:
        user_agent: {prefix: "curl/"}
      action: weigh
      weight: 10
```

Eine Anfrage ohne Sprachangabe erreicht 10 und wird geprüft. Kommt sie
zusätzlich von `curl`, erreicht sie 20 und wird blockiert.

### Regeln in eigenen Dateien

```yaml
rules:
  files:
    - rules/eigene.yaml      # Pfad relativ zur Konfigurationsdatei
```

Eine Regeldatei enthält eine Liste unter `rules:`. Die Regeln aus
`rules.list` werden zuerst ausgewertet, danach die Dateien in der angegebenen
Reihenfolge. Eine kommentierte Vorlage liegt in
[`examples/rules/basic.yaml`](../../examples/rules/basic.yaml).

### Pfade lassen sich nicht umgehen

Eine Regel auf `/admin` greift auch bei `//admin`, `/x/../admin`, `/%61dmin`
oder `/admin;x=1`. Xibalba bringt den Pfad vor dem Vergleich in eine
einheitliche Form.

Achtung: `prefix: "/admin"` trifft auch `/administrator`. Wenn Sie nur das
Verzeichnis meinen, schreiben Sie `regex: "^/admin(/|$)"`.

Die vollständige Beschreibung aller Möglichkeiten steht in
[RULES.md](../RULES.md) (englisch).

## 7. Die Sicherheitsprüfung

Entscheidet eine Regel `challenge`, sieht der Besucher statt der gewünschten
Seite kurz die Seite „Kurze Sicherheitsprüfung“. Sein Browser löst eine
Rechenaufgabe und lädt danach die gewünschte Seite. Der Besucher muss nichts
tun. Danach erhält er einen Nachweis in Form eines Cookies und wird für die
eingestellte Dauer nicht erneut geprüft.

Besucher ohne JavaScript können stattdessen einige Sekunden warten und auf
„Weiter“ drücken.

### Einstellungen

```yaml
challenge:
  difficulty: 18            # Rechenaufwand, 8 bis 24
  no_javascript: button     # button oder deny
  wait: 3s                  # Wartezeit ohne JavaScript
  challenge_lifetime: 5m    # Zeit für die Prüfung
  pass_lifetime: 168h       # Gültigkeit des Nachweises (168h = eine Woche)
  bind_network: true        # Nachweis an das Netz des Besuchers binden
  key_file: /var/lib/xibalba/xibalba.key
  cookie_name: "xibalba-pass"
```

| Einstellung | Bedeutung | Wann ändern? |
|---|---|---|
| `difficulty` | Rechenaufwand für den Browser. Jede Stufe verdoppelt ihn. | Erhöhen, wenn Massenabrufe trotz Prüfung durchkommen. Senken, wenn sich Besucher mit alten Geräten über Wartezeit beklagen. |
| `no_javascript` | `button`: Besucher ohne JavaScript warten und drücken „Weiter“. `deny`: sie erfahren, dass JavaScript nötig ist. | `deny` ist strenger, schließt aber Besucher ohne JavaScript aus. Für öffentliche Stellen empfiehlt sich `button`. |
| `wait` | Wartezeit, bevor „Weiter“ gilt. | Selten. |
| `challenge_lifetime` | So lange hat ein Besucher Zeit. Danach erhält er einfach eine neue Aufgabe. | Selten. |
| `pass_lifetime` | So lange wird ein Besucher nicht erneut geprüft. Angabe in Stunden (`h`), eine Einheit für Tage gibt es nicht. | Kürzer für strengeren Schutz, länger für weniger Unterbrechungen. |
| `bind_network` | Der Nachweis gilt nur aus dem Netz, in dem er erworben wurde. | Nur abschalten, wenn viele Ihrer Besucher ständig das Netz wechseln. |
| `key_file` | Datei mit dem Signaturschlüssel. | Für den echten Betrieb immer setzen (Schritt 6). |
| `cookie_name` | Name des Cookies. | Nur bei Namenskonflikt. |

Richtwerte für `difficulty`, gemessen auf dem Entwicklungsrechner. Telefone
und ältere Rechner sind um ein Mehrfaches langsamer; das ist nicht gemessen.

| `difficulty` | Dauer auf einem Arbeitsplatzrechner |
|---|---|
| 16 | etwa 0,03 s |
| 18 (Voreinstellung) | etwa 0,1 s |
| 20 | etwa 0,4 s |
| 22 | etwa 1,6 s |
| 24 | etwa 6 s |

### Was die Prüfung leistet und was nicht

Sie hält Programme auf, die Seiten abrufen, ohne sich wie ein Browser zu
verhalten. Das ist der größte Teil des Massenverkehrs, der Last erzeugt.
Außerdem lässt sich ein einmal erworbener Nachweis nicht auf viele Rechner
verteilen.

Sie hält kein Programm auf, das einen echten Browser fernsteuert. Ist
`no_javascript: button` eingestellt, kann auch ein Crawler warten und das
Formular absenden; das ist der bewusste Preis dafür, dass Besucher ohne
JavaScript nicht ausgeschlossen werden.

### Bevor Sie einen Bereich prüfen lassen

- **Formulare:** Sendet ein Besucher ohne Nachweis ein Formular an einen
  geprüften Pfad, bekommt er die Prüfung, und seine Eingaben gehen dabei
  verloren. Lassen Sie deshalb auch die Seite prüfen, auf der das Formular
  steht. Dann hat der Besucher den Nachweis schon, wenn er absendet.
- **Programme statt Menschen:** Schnittstellen (API), Feed-Leser,
  Überwachung und Webhooks bestehen die Prüfung nicht und erhalten den Status
  403. Nehmen Sie sie mit einer `allow`-Regel **über** der `challenge`-Regel
  aus, möglichst anhand der Adresse.
- **Suchmaschinen:** Auch deren Crawler bestehen die Prüfung nicht. Wenn
  geprüfte Seiten in Suchmaschinen erscheinen sollen, erlauben Sie die
  Suchmaschinen vorher. Gepflegte Listen dafür sind geplant.
- **Zwischenspeicher:** Ein Cache oder CDN vor Xibalba darf geprüfte Seiten
  nicht speichern, sonst liefert er sie ohne Prüfung aus.

### Alle Nachweise ungültig machen

Xibalba anhalten, die Schlüsseldatei löschen, Xibalba starten. Es legt einen
neuen Schlüssel an, und alle bisherigen Nachweise sind wertlos.

Die technische Beschreibung steht in [CHALLENGE.md](../CHALLENGE.md) (englisch).

## 8. Die Seiten für Besucher anpassen

Xibalba zeigt Besuchern drei eigene Seiten: die Sicherheitsprüfung, „Diese
Anfrage wurde blockiert“ und „Die Website ist gerade nicht erreichbar“. Sie
funktionieren ohne jede Einstellung, auf Deutsch oder Englisch je nach
Browser des Besuchers; die jeweils andere Sprache ist auf derselben Seite
aufklappbar.

### Ihren Namen und eine Kontaktangabe zeigen

```yaml
pages:
  operator: "Stadt Musterhausen"
  contact: "webmaster@musterhausen.example"
```

Aus „Der Betreiber dieser Website lässt Anfragen dieser Art nicht zu.“ wird
„Stadt Musterhausen lässt Anfragen dieser Art nicht zu.“ Die Kontaktangabe
erscheint als eigene Zeile auf der Blockseite.

Braucht der Name je Sprache eine andere Form, setzen Sie ihn pro Sprache:

```yaml
pages:
  texts:
    de:
      operator: "Die Stadt Musterhausen"
    en:
      operator: "The City of Musterhausen"
```

### Einen Text ändern

```yaml
pages:
  texts:
    de:
      blocked_title: "Zugriff nicht möglich"
      blocked_text: "{operator} erlaubt keine automatisierten Abrufe. Bei Fragen nennen Sie bitte die folgende Referenz."
```

`{operator}` steht für den Betreiber. Texte, die Sie nicht nennen, behalten
ihren eingebauten Wortlaut. Was Sie schreiben, wird wörtlich als Text
angezeigt; HTML wird nicht ausgeführt.

| Textname | Wo er erscheint | Eingebauter deutscher Text |
|---|---|---|
| `operator` | in anderen Texten an der Stelle `{operator}` | Der Betreiber dieser Website |
| `challenge_title` | Überschrift der Sicherheitsprüfung | Kurze Sicherheitsprüfung |
| `challenge_text` | erster Absatz der Sicherheitsprüfung | {operator} schützt diese Seiten vor automatisierten Massenabrufen. Ihr Browser löst dafür eine kurze Rechenaufgabe. Das dauert meist nur wenige Sekunden; Sie müssen nichts tun. |
| `challenge_cookie` | zweiter Absatz der Sicherheitsprüfung | Danach wird ein Cookie gespeichert, das nur festhält, dass die Prüfung bestanden wurde. |
| `challenge_working` | Statuszeile während der Prüfung | Die Prüfung läuft … |
| `challenge_done` | Statuszeile nach der Prüfung | Prüfung bestanden. Sie werden weitergeleitet. |
| `challenge_manual` | Hinweis für Besucher ohne JavaScript | Ihr Browser führt kein JavaScript aus. Bitte warten Sie einige Sekunden und wählen Sie dann „Weiter“. |
| `challenge_button` | Schaltfläche für Besucher ohne JavaScript | Weiter |
| `challenge_needs_script` | statt der Schaltfläche, wenn `no_javascript: deny` | Für diese Prüfung muss JavaScript eingeschaltet sein. Bitte schalten Sie JavaScript ein und laden Sie die Seite neu. |
| `challenge_too_early` | Hinweis, wenn „Weiter“ zu früh gedrückt wurde | Das war etwas zu schnell. Bitte warten Sie einige Sekunden und wählen Sie dann erneut „Weiter“. |
| `challenge_retry` | Hinweis, wenn die Prüfung neu gestartet wurde | Die Prüfung konnte nicht abgeschlossen werden und wurde neu gestartet. |
| `blocked_title` | Überschrift der Blockseite | Diese Anfrage wurde blockiert |
| `blocked_text` | Absatz der Blockseite | {operator} lässt Anfragen dieser Art nicht zu. Wenn Sie das für einen Fehler halten, nehmen Sie bitte Kontakt auf und nennen Sie die folgende Referenz. |
| `reference_label` | vor der Referenz | Referenz: |
| `contact_label` | vor der Kontaktangabe | Kontakt: |
| `unavailable_title` | Überschrift, wenn die Website nicht antwortet | Die Website ist gerade nicht erreichbar |
| `unavailable_text` | Absatz dazu | Bitte versuchen Sie es in einigen Minuten erneut. |
| `language_name` | Beschriftung des Sprachumschalters | Deutsch |

### Sprache

```yaml
pages:
  default_language: de    # de oder en
```

Gilt für Besucher, deren Browser weder Deutsch noch Englisch anfragt.

Logo und Akzentfarbe sind geplant.

## 9. Betrieb: prüfen, beobachten, ändern

### Zustand

```sh
curl http://127.0.0.1:9090/healthz
```

Der Bericht nennt jeden Teil einzeln. `ok` heißt in Ordnung, `degraded` heißt
eingeschränkt, `down` heißt ausgefallen. Bei einem Problem steht unter
`detail`, was los ist.

| Teil | Bedeutung | Nicht `ok`, wenn … |
|---|---|---|
| `public` | nimmt Anfragen der Besucher an | er nicht mehr lauscht |
| `upstream` | Verbindung zu Ihrer Website | die letzte Anfrage an die Website fehlschlug |
| `rules` | Auswertung der Regeln | eine Anfrage nicht ausgewertet werden konnte |
| `ops` | der Betriebsport selbst | er nicht mehr lauscht |

Ist Ihre Website nicht erreichbar, bleibt Xibalba in Betrieb: Besucher
erhalten die Seite „Die Website ist gerade nicht erreichbar“, und `upstream`
zeigt `degraded` mit der Ursache. Der Abruf von `/healthz` antwortet nur dann
mit einem Fehlerstatus (503), wenn Xibalba selbst ausgefallen ist. Das ist
der Wert, den eine Überwachung prüfen sollte.

### Zähler

```sh
curl http://127.0.0.1:9090/decisions
```

```json
{
  "dry_run": false,
  "since": "2026-10-03T08:57:15Z",
  "totals": {
    "allow": 0,
    "challenge": 1,
    "deny": 0
  },
  "failures": 0,
  "challenge": {
    "served": 1,
    "passed": 0,
    "solved": 0,
    "failed": 0
  },
  "sources": [
    {
      "source": "rule:challenge-search",
      "action": "challenge",
      "reference": "5f54bba9",
      "count": 1
    },
    {
      "source": "threshold:10",
      "action": "challenge",
      "reference": "97e55a91",
      "count": 0
    },
    {
      "source": "default",
      "action": "allow",
      "reference": "37a8eec1",
      "count": 0
    }
  ]
}
```

| Feld | Bedeutung |
|---|---|
| `totals` | Entscheidungen seit dem Start, je Aktion |
| `sources` | wie oft jede Regel, jede Schwelle und die Voreinstellung entschieden hat |
| `reference` | Kurzcode der Regel; derselbe Code steht auf der Blockseite |
| `challenge.served` | so oft wurde die Prüfseite gezeigt |
| `challenge.solved` | so viele Antworten waren richtig |
| `challenge.failed` | so viele Antworten wurden abgelehnt |
| `challenge.passed` | so viele Anfragen kamen mit gültigem Nachweis durch |
| `failures` | Anfragen, die wegen eines internen Fehlers nicht ausgewertet werden konnten |

Viel `served` und wenig `solved` ist das Bild eines Crawlers, der die Prüfung
nicht besteht. Die Zähler beginnen bei jedem Start bei null; dauerhafte
Statistik ist geplant.

### Ein Besucher meldet, er sei zu Unrecht blockiert

Auf der Blockseite steht eine Referenz, etwa `5f54bba9`. Sie bezeichnet die
Regel, nicht den Besucher. Suchen Sie den Code in `/decisions`; dort steht
der Name der Regel. So finden Sie die Ursache, ohne dass Xibalba festhalten
muss, wer blockiert wurde.

### Protokoll

Xibalba schreibt auf die Standardfehlerausgabe; unter systemd landet das im
Journal. Jede Zeile nennt mit `component=` den Teil, der sie geschrieben hat.
Für Menschen lesbar wird es mit:

```yaml
log:
  format: text
```

Einzelne Anfragen und Entscheidungen werden nicht protokolliert, auch nicht
blockierte. Ein Ausfall Ihrer Website wird einmal beim Beginn und einmal beim
Ende gemeldet, nicht für jede Anfrage.

### Etwas ändern

1. Datei ändern.
2. `xibalba -check -config /etc/xibalba/xibalba.yaml`
3. Xibalba neu starten.

Xibalba liest die Konfiguration nur beim Start. Ein Neuladen im laufenden
Betrieb ist geplant. Mit gesetzter Schlüsseldatei behalten Besucher ihren
Nachweis über den Neustart hinweg.

### Aktualisieren

Neue Fassung bauen, die Programmdatei ersetzen, neu starten. Vorher
`CHANGELOG.md` lesen: Solange es keine Version 1.0 gibt, können sich
Einstellungen ändern; jede solche Änderung ist dort aufgeführt.

## 10. Datenschutz

Dieser Abschnitt nennt die Tatsachen, die Sie oder Ihre
Datenschutzbeauftragten für die eigene Bewertung brauchen. Er ist keine
Rechtsberatung.

| Frage | Antwort |
|---|---|
| Speichert Xibalba IP-Adressen? | Nein. Die Adresse wird nur während der Bearbeitung einer Anfrage verwendet und in keine Datei geschrieben. |
| Protokolliert Xibalba, wer was aufruft? | Nein. Es gibt kein Zugriffsprotokoll. Auch die ausführlichste Protokollstufe (`debug`) nennt bei einer Entscheidung nur die Regel, nicht Adresse, Pfad oder Kennung. Eine Ausnahme: Tritt bei der Bearbeitung einer Anfrage ein Programmfehler auf, wird zur Fehlersuche der Pfad dieser einen Anfrage protokolliert, nicht aber die Adresse. |
| Was wird gezählt? | Wie oft jede Regel entschieden hat. Ohne Bezug zu Personen, nur im Arbeitsspeicher, bis zum nächsten Neustart. |
| Setzt Xibalba ein Cookie? | Nur bei Besuchern, die die Sicherheitsprüfung bestanden haben. |
| Was steht in dem Cookie? | Ein Ablaufzeitpunkt und ein Prüfwert, der es an Netz und Browserkennung bindet. Der Prüfwert ist ein Hash mit geheimem Schlüssel; Adresse und Kennung lassen sich daraus nicht zurückgewinnen. Keine Kennung der Person, nichts über aufgerufene Seiten. |
| Wozu dient das Cookie? | Allein dazu, einen Besucher nach bestandener Prüfung nicht erneut zu prüfen. |
| Wie lange gilt es? | `challenge.pass_lifetime`, in der Voreinstellung eine Woche. |
| Sieht meine Website das Cookie? | Nein. Xibalba entfernt es, bevor es eine Anfrage weiterreicht. |
| Werden Daten an Dritte übertragen? | Nein. Die Seiten von Xibalba laden nichts von anderen Servern: keine Schriften, keine Skripte, keine Bilder. Xibalba selbst nimmt keine Verbindung nach außen auf, außer zu Ihrer Website. |
| Was erhält meine Website zusätzlich? | Die Adresse des Besuchers in den Kopfzeilen `X-Forwarded-For` und `X-Real-IP`, wie bei jedem vorgeschalteten Webserver. Was Ihre Website damit tut, liegt bei Ihnen. |

Die Prüfseite weist den Besucher selbst auf das Cookie hin (Text
`challenge_cookie`).

## 11. Fehlersuche

| Beobachtung | Ursache | Abhilfe |
|---|---|---|
| Xibalba startet nicht | Fehler in der Konfiguration, belegter Port oder Problem mit der Schlüsseldatei | Die Meldung nennt Datei, Zeile und Abhilfe. `xibalba -check -config …` zeigt alle Konfigurationsfehler auf einmal. |
| `start-up failed … address already in use` | Der Port ist belegt | Anderen Port in `server.listen` oder `ops.listen` wählen oder das andere Programm beenden. |
| Besucher sehen „Die Website ist gerade nicht erreichbar“ | Ihre Website antwortet nicht oder zu langsam | `/healthz` ansehen: unter `upstream` steht die Ursache. `upstream.url` prüfen. Bei langsamer Website `upstream.response_header_timeout` erhöhen. |
| Eine Adressregel greift für alle oder für niemanden | `server.trusted_proxies` fehlt; Xibalba sieht nur die Adresse Ihres Webservers | Schritt 5. |
| Nach jedem Neustart werden alle erneut geprüft | Keine Schlüsseldatei | `challenge.key_file` setzen (Schritt 6). Im Protokoll steht dazu eine Warnung. |
| Besucher werden bei jedem Seitenaufruf erneut geprüft | Der Browser nimmt das Cookie nicht an oder sendet es nicht zurück | Prüfen, ob ein Cache vor Xibalba die Prüfseite speichert. Prüfen, ob Ihr Webserver `X-Forwarded-Proto` sendet und in `trusted_proxies` steht. Wechselt die Adresse der Besucher ständig, `bind_network: false` erwägen. |
| Ein Formular verliert nach der Prüfung die Eingaben | Das Ziel des Formulars wird geprüft, die Seite mit dem Formular nicht | Auch die Formularseite prüfen lassen (Abschnitt 7). |
| Eine Schnittstelle oder Überwachung erhält plötzlich 403 | Sie fällt unter eine `challenge`- oder `deny`-Regel | In `/decisions` nachsehen, welche Regel zählt. `allow`-Regel darüber setzen. |
| Eine Regel greift nicht | Eine Regel weiter oben entscheidet zuerst, oder die Bedingung trifft nicht zu | In `/decisions` sehen Sie, welche Regel stattdessen zählt. Mit `curl -A "…"` gezielt nachstellen. |
| Eine Regel blockiert zu viel | `prefix` oder `contains` trifft mehr als gedacht | Genauer fassen (siehe „Pfade lassen sich nicht umgehen“ in Abschnitt 6). Erst im Probelauf testen. |
| Unsicher, was eine Änderung bewirkt | | `rules.dry_run: true`, Zähler beobachten, dann scharf schalten. |

Gezielt nachstellen, was ein bestimmter Anfragender erlebt:

```sh
curl -i -A "ExampleBot/1.0" http://127.0.0.1:8080/
```

```text
HTTP/1.1 403 Forbidden
```

## 12. Was Xibalba noch nicht kann

Damit Sie wissen, woran Sie sind:

- **Kein HTTPS in Xibalba selbst.** Ein Webserver davor ist nötig.
- **Keine gepflegten Bot-Listen.** Sie schreiben Ihre Regeln selbst. Fertige,
  geprüfte Listen („Trainings-Crawler blockieren, KI-Suche erlauben“) sind der
  nächste Entwicklungsschritt.
- **Keine Weboberfläche.** Einstellungen stehen in der Datei, Zähler ruft man
  mit `curl` ab.
- **Keine dauerhafte Statistik.** Die Zähler beginnen bei jedem Start bei null.
- **Kein Neuladen im Betrieb.** Änderungen brauchen einen Neustart.
- **Keine fertigen Pakete.** Xibalba wird aus dem Quelltext gebaut.
- **Kein Logo, keine Akzentfarbe** auf den Besucherseiten.
- **Keine Begrenzung der Anfragerate.**
- **Meldungen des Programms sind englisch.**
- **Die Besucherseiten sind mit einem automatischen Prüfwerkzeug und per
  Tastatur geprüft, aber noch nicht mit einem echten Screenreader.**

Die Reihenfolge der weiteren Arbeit steht in [ROADMAP.md](../ROADMAP.md).

## 13. Weiterführende Dokumente

| Dokument | Inhalt | Sprache |
|---|---|---|
| [CONFIGURATION.md](../CONFIGURATION.md) | jede Einstellung mit Voreinstellung und erlaubten Werten | Englisch |
| [RULES.md](../RULES.md) | alles, was Regeln können | Englisch |
| [CHALLENGE.md](../CHALLENGE.md) | die Sicherheitsprüfung im Detail | Englisch |
| [`xibalba.example.yaml`](../../xibalba.example.yaml) | Vorlage der Konfigurationsdatei mit allen Einstellungen | Englisch |
| [`examples/rules/basic.yaml`](../../examples/rules/basic.yaml) | kommentierte Beispielregeln | Englisch |
| [CHANGELOG.md](../../CHANGELOG.md) | was sich zwischen den Fassungen geändert hat | Englisch |
