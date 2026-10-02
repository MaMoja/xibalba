# Configuration

Xibalba reads one YAML file. Every setting is optional; a setting that is left
out uses its default. An empty file is valid.

```sh
xibalba -config /etc/xibalba/xibalba.yaml
```

| Flag | Meaning |
|---|---|
| `-config <path>` | Configuration file. Default: `xibalba.yaml` in the current directory. |
| `-check` | Validate the file, print the result, and exit without starting. |
| `-version` | Print the version and exit. |

## How mistakes are reported

Xibalba refuses to start with a file it does not fully understand. It lists
every problem at once, with the line, the setting and the fix:

```text
configuration xibalba.yaml: 2 problems
  - line 2, log.level: "loud" is not a log level
    fix: use one of: debug, info, warn, error
  - line 5, ops.listen: "localhost" is not a listen address: it must have the form host:port
    fix: use host:port, for example "127.0.0.1:9090"
```

A setting that does not exist (usually a typo) is also an error. Xibalba never
ignores a setting silently.

## Settings

### `log`

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn`, `error` | Lowest severity that is written. |
| `log.format` | `json` | `json`, `text` | `json` writes one object per line for log collectors. `text` writes `key=value` lines for reading by eye. |

Logs go to standard error. Every line carries a `component` attribute naming
the part of the program that wrote it.

### `ops`

The operations listener serves internal endpoints. It is separate from the
public side so that health checks keep working when the public side is under load.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `ops.listen` | `127.0.0.1:9090` | `host:port` | Address to listen on. Use port `0` to let the system pick a free port. |

Keep this listener off the public internet. The default is reachable from the
same machine only.

Endpoints:

| Path | Answer |
|---|---|
| `GET /healthz` | JSON health report. Status `200` while Xibalba can serve, `503` when a component is down. |
| `GET /version` | JSON with the version, commit and Go version of the running build. |

Example health report:

```json
{
  "state": "ok",
  "components": {
    "ops": { "state": "ok" }
  }
}
```

`state` is `ok`, `degraded` (working with reduced function) or `down`. The
top-level state is the worst state of any component. A component that is not
`ok` carries a `detail` text that says why.

### `shutdown_timeout`

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `shutdown_timeout` | `10s` | A duration greater than zero, such as `5s`, `1m` | How long running requests get to finish after Xibalba is asked to stop (SIGINT or SIGTERM). |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean shutdown, or `-check` / `-version` succeeded. |
| `1` | Invalid configuration, a component could not start, or a component failed while running. |
| `2` | Invalid command line. |

## Complete example

[`xibalba.example.yaml`](../xibalba.example.yaml) lists every setting with its
default. A test keeps that file in step with the code.
