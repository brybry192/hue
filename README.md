# hue

A command line tool for Philips Hue, written to solve one specific annoyance:
**Smart home apps fire lighting commands without verifying them.** It sends "off" and
assumes it worked. When a Zigbee message is lost, a light stays on and nothing
notices. `hue sweep` notices.

```
$ hue sweep
Hallway (room): no motion for 23m (threshold 15m) -> motion-idle
  would off  Hall 1 (on for 1h12m)
  would off  Hall 2 (on for 1h12m)

Kitchen (room): 1 of 5 lights on (20% of the group) -> outlier
  would off  Kitchen 3 (on for 42m)

would switch off 3 lights in 2 groups
this was a dry run; re-run with --no-dry-run to switch them off
```

## Contents

- [How the sweep decides](#how-the-sweep-decides)
- [Install](#install)
- [Getting started](#getting-started)
- [Commands](#commands)
- [Configuration](#configuration)
- [Running it on a schedule](#running-it-on-a-schedule)
- [Architecture](#architecture)
- [Testing](#testing)
- [Raw API with curl](#raw-api-with-curl)
- [Troubleshooting](#troubleshooting)

## How the sweep decides

Switching lights off automatically is easy to get wrong: nobody wants the
kitchen going dark while they are cooking in it. The sweep therefore never
switches off a light just because it is on. Two rules have to justify it.

### The motion rule

If a group has a working motion sensor, **that sensor decides** — it is direct
evidence about whether anyone is there.

- No motion for longer than `idle_threshold` (default **15m**) → switch off
  every light that is on in that group.
- Motion more recently than that → **leave the group completely alone**, even
  if it looks like an outlier. One light on in a room someone is standing in is
  a deliberate choice, not a straggler.

A sensor is only consulted when it is enabled and reports a `motion_report`
timestamp. Disabled or ancient sensors fall through to the outlier rule.

### The outlier rule

For groups with no usable sensor, the shape of the group is the evidence. A
light is an outlier when **all** of these hold:

| Condition | Default | Config key |
|---|---|---|
| The group is big enough to have a minority | 3+ lights | `min_group_size` |
| Few enough lights are on | at most 2 | `max_on_count` |
| They are a small share of the group | at most 34% | `max_on_fraction` |

So 1 of 5 on is swept; 3 of 5 is not (that is most of the room, so it was
probably switched on deliberately). 2 of 12 outside is swept; 2 of 4 is not.
Both the count and the fraction must pass, which is what stops a big zone being
swept merely because two of its lights are on.

### The grace period

The Hue API exposes no "when did this light turn on" timestamp, so the tool
keeps its own record of what it has seen. A light must have been observed on
for `min_on_duration` (default **10m**) before it can be switched off.

This is what stops the sweep fighting you: switch a light on and a sweep two
minutes later will leave it be. The cost is that the first ever run switches
nothing off — it has no history yet — so on a 5-minute cron the tool starts
acting on the second run. `--force` skips the wait, and `min_on_duration: "0s"`
disables it.

### Other safeties

- **Dry run by default.** Every command that changes a light previews instead;
  `--no-dry-run` applies.
- **Smart plugs are swept like lights** by default. If a plug powers something
  that must stay on, such as a fridge, set `include_plugs` to false or list it
  in `exclude_lights`.
- **`exclude_lights`** never gets switched off, matched on light or device name
  with shell-style wildcards.
- A light in both a room and a zone is **switched off once**, not twice.
- **Unreachable lights are ignored.** A light whose power is cut at the wall
  keeps its last reported state on the bridge, often "on". The sweep checks
  each device's Zigbee connectivity and leaves unreachable lights out
  entirely: they are not switched, and they do not count toward the group's
  size or its on-count. `hue ls` shows them as `?` / `unreachable`.

## Install

Go 1.26 or newer.

```bash
git clone https://github.com/brybry192/hue.git
cd hue
go build -o hue .          # ./hue
# or install onto your PATH:
go install github.com/brybry192/hue@latest
```

With a version stamp:

```bash
go build -ldflags "-X github.com/brybry192/hue/internal/cli.Version=$(git describe --tags --always)" -o hue .
```

## Getting started

```bash
# 1. Pair with the bridge. Press its round link button when asked.
hue auth --bridge 192.0.2.10     # or just `hue auth` to auto-discover

# 2. Look at what the bridge knows about.
hue ls

# 3. See what a sweep would do. This changes nothing.
hue sweep

# 4. Once you are happy with it:
hue sweep --no-dry-run
```

`hue auth` stores the bridge address, the application key and the bridge's TLS
certificate fingerprint in `~/.config/hue/config.json` with `0600`
permissions.

## Commands

| Command | What it does |
|---|---|
| `hue ls [room...]` | List rooms and zones with their lights, switches, remotes and sensors |
| `hue sweep` | Find and switch off lights that were left on |
| `hue on <target>` | Switch a room, zone or light on |
| `hue off <target>` | Switch a room, zone or light off |
| `hue status` | Bridge identity, totals and the active sweep settings |
| `hue auth` | Pair with a bridge |
| `hue config show\|path\|init` | Inspect or create the config file |
| `hue probe [type]` | Inventory the bridge's resource types, or dump one as raw JSON |

Useful flags:

```bash
hue ls --on                      # only lights that are on
hue ls --rooms --json            # machine readable, rooms only
hue sweep --no-dry-run           # actually do it
hue sweep --rooms Kitchen,Outside    # override the configured scope
hue sweep --idle 30m --min-on 20m    # override thresholds for one run
hue sweep --force                # ignore the grace period
hue sweep -v                     # explain the groups it left alone
hue sweep --log                  # one timestamped line per event, for log files
hue sweep --json                 # the full plan and results as JSON
hue off Kitchen --no-dry-run     # flags may come before or after the target
hue probe                        # what resource types does this bridge have?
```

`hue sweep -v` is the command to reach for when it did not do what you
expected — it prints the reasoning for every group it skipped:

```
$ hue sweep -v
  . Lounge: motion 1m ago, under the 15m threshold, so someone is probably there
  . Porch: Porch Left already handled by another group
  . Study: 3/5 on, no rule applies (no usable motion sensor; 60% of the group
    is on, over max_on_fraction 34%)
```

## Configuration

`hue config init` writes a fully populated file. Location, in order of
precedence: `--config`, `$HUE_CONFIG`, `$XDG_CONFIG_HOME/hue/config.json`,
`~/.config/hue/config.json`.

```json
{
  "bridge": {
    "host": "192.0.2.10",
    "app_key": "your-application-key",
    "cert_sha256": "3f2a...",
    "timeout": "10s"
  },
  "sweep": {
    "rooms": ["Kitchen", "Hallway", "Outside"],
    "exclude_lights": ["Night Light", "porch *"],
    "min_on_duration": "10m",
    "include_plugs": true,
    "outlier": {
      "enabled": true,
      "min_group_size": 3,
      "max_on_count": 2,
      "max_on_fraction": 0.34
    },
    "motion": {
      "enabled": true,
      "idle_threshold": "15m"
    }
  }
}
```

| Key | Default | Meaning |
|---|---|---|
| `sweep.rooms` | `[]` (everything) | Rooms **and zones** to sweep, by name, case-insensitive |
| `sweep.exclude_lights` | `[]` | Never switch these off; wildcards allowed |
| `sweep.min_on_duration` | `10m` | Grace period before a light may be swept |
| `sweep.include_plugs` | `true` | Allow smart plugs to be switched off |
| `sweep.outlier.*` | see above | The outlier rule's thresholds |
| `sweep.motion.idle_threshold` | `15m` | How long a room must be still |

Durations are strings: `"45s"`, `"15m"`, `"1h30m"`. Any key you leave out keeps
its default. `enabled` is deliberately a tri-state — omit it for the default,
or set it explicitly to `false` to turn a rule off.

Environment overrides: `HUE_BRIDGE_HOST`, `HUE_APP_KEY`, `HUE_CONFIG`,
`HUE_STATE`.

## Running it on a schedule

The sweep is designed to be run repeatedly and is safe to run often; the grace
period means a light has to be genuinely forgotten before anything happens.

A `launchd` agent at `~/Library/LaunchAgents/com.brybry192.hue.sweep.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.brybry192.hue.sweep</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/hue</string>
    <string>sweep</string>
    <string>--no-dry-run</string>
    <string>--log</string>
  </array>
  <key>StartInterval</key><integer>300</integer>
  <key>StandardOutPath</key><string>/tmp/hue-sweep.log</string>
  <key>StandardErrorPath</key><string>/tmp/hue-sweep.err</string>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.brybry192.hue.sweep.plist
```

Or cron, every five minutes:

```cron
*/5 * * * * /usr/local/bin/hue sweep --no-dry-run --log >> /tmp/hue-sweep.log 2>&1
```

`--log` writes one line per light switched off, then a summary, each
starting with the time of the sweep. A run with nothing to do is one line:

```
2026-10-05T13:12:03Z off group="Study" rule=outlier light="Lamp" on_for=12m
2026-10-05T13:12:03Z swept lights=1 groups=1
2026-10-05T13:17:03Z swept lights=0 groups=0
```

Add `-v` to log why each group was left alone as `note` lines.

Note that a scheduled job needs its own Local Network permission on macOS — see
[Troubleshooting](#troubleshooting).

## Architecture

```
main.go                     argument handling and exit codes only
internal/cli/               one file per command; all I/O goes through App
  cli.go                    dispatch, shared flags, interspersed flag parsing
  list.go sweep.go onoff.go status.go auth.go configcmd.go probe.go
internal/hue/               the CLIP v2 client
  resources.go              the API's JSON shapes
  client.go                 HTTP, TLS pinning, retries
  auth.go                   discovery and pairing
  model.go                  resources -> rooms/zones/lights/sensors
internal/config/            config file, defaults, validation
internal/state/             what the last sweep saw ("on since")
internal/sweep/             the decision logic, as a pure function
```

Three deliberate choices:

**CLIP v2 only.** The legacy v1 API is deprecated and newer bridge models do
not serve it at all. v2 is also the only version that exposes
`motion.motion_report.changed`, the timestamp the whole motion rule depends on.

**The decision is a pure function.** `sweep.Build(Inputs) Plan` takes a
snapshot, the config and the current time, and returns what it would do. It
performs no I/O, which is why the rules can be tested exhaustively and why
`--dry-run` is the same code path as a real run — only the execution step
differs.

**Rooms and zones are one concept.** A room's children are *devices*; a zone's
children are *light services* directly. `model.go` normalises both into a
`GroupView`, and because a zone carries no sensors of its own it inherits them
from the rooms its lights live in. That is what lets an `Outside` zone be swept
by a porch motion sensor.

### TLS

The bridge serves a self-signed certificate for an IP address, so the system
trust store can never validate it. Rather than disabling verification, `hue
auth` records the certificate's SHA-256 fingerprint and every later connection
is pinned to it (trust on first use). `bridge.insecure: true` opts out.

## Testing

```bash
go test ./...                    # everything
go test ./... -race              # with the race detector
go test ./... -cover             # coverage
go test ./internal/sweep -v      # just the decision rules
```

The suite needs no bridge and no network.

- **`internal/sweep`** — the rules, exhaustively: outlier boundaries, motion
  idle and the presence veto, sensors that cannot be trusted, the grace period,
  exclusions and wildcards, plugs, room scoping, and a light that belongs to
  both a room and a zone.
- **`internal/hue`** — assembling rooms and zones from raw resources, including
  multi-light devices, dangling references and sensor inheritance; device
  classification across real Hue model IDs; duration formatting.
- **`internal/config` / `internal/state`** — defaults surviving a partial file,
  the tri-state `enabled`, validation, `0600` permissions, atomic writes, and
  the on-since bookkeeping that the grace period relies on.
- **`internal/cli`** — every command end to end against a fake bridge
  (`httptest` TLS server) with a deliberately varied topology: a room with one
  straggler, a room gone idle, a room with someone in it, and a zone overlapping
  a room. These assert the exact writes sent to the bridge, so dry-run really
  does mean no writes.

## Raw API with curl

Everything this tool does is a few HTTPS calls. `-k` is needed because the
bridge's certificate is self-signed.

### Pair and get an application key

Press the bridge's link button, then within ~30 seconds:

```bash
curl -sk -X POST https://192.0.2.10/api \
  -H 'Content-Type: application/json' \
  -d '{"devicetype":"hue-cli#laptop","generateclientkey":true}'
```

```json
[{"success":{"username":"APPLICATION_KEY","clientkey":"..."}}]
```

Before the button is pressed you get the expected refusal:

```json
[{"error":{"type":101,"address":"","description":"link button not pressed"}}]
```

Keep the key handy:

```bash
KEY=your-application-key
BRIDGE=192.0.2.10
```

### Identify the bridge

```bash
curl -sk https://$BRIDGE/api/config | jq
```

```json
{"name":"Hue Bridge","apiversion":"1.x.0","modelid":"BSB00x", ...}
```

### Read state

```bash
# every light, trimmed to the interesting bits
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource/light |
  jq -r '.data[] | "\(.metadata.name)\t\(.on.on)\t\(.dimming.brightness // "-")"'

# rooms, with the devices in them
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource/room |
  jq -r '.data[] | "\(.metadata.name): \(.children | length) devices"'

# zones contain light services directly, not devices
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource/zone | jq '.data[].children'
```

### The motion timestamp the sweep relies on

```bash
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource/motion |
  jq -r '.data[] | "\(.id) motion=\(.motion.motion) changed=\(.motion.motion_report.changed)"'
```

```
7f3c... motion=false changed=2026-10-01T11:37:12.482Z
```

`motion_report.changed` is when the motion value last flipped. With
`motion=false` that is when the room went still, which is what gets compared
against `idle_threshold`.

### Change state

```bash
# one light off
curl -sk -X PUT -H "hue-application-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"on":{"on":false}}' \
  https://$BRIDGE/clip/v2/resource/light/LIGHT_ID

# a whole room, via its grouped_light service
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource/room |
  jq -r '.data[] | select(.metadata.name=="Kitchen") |
         .services[] | select(.rtype=="grouped_light") | .rid'

curl -sk -X PUT -H "hue-application-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"on":{"on":false}}' \
  https://$BRIDGE/clip/v2/resource/grouped_light/GROUPED_LIGHT_ID
```

A successful write returns `{"errors":[],"data":[{"rid":"...","rtype":"light"}]}`.
Note that the bridge can return HTTP 200 with a non-empty `errors` array, so
check the body rather than just the status.

### Everything at once

```bash
# what resource types does this bridge expose? (same as `hue probe`)
curl -sk -H "hue-application-key: $KEY" \
  https://$BRIDGE/clip/v2/resource | jq -r '.data[].type' | sort | uniq -c | sort -rn
```

### Watch events as they happen

```bash
curl -skN -H "hue-application-key: $KEY" \
  https://$BRIDGE/eventstream/clip/v2
```

## Troubleshooting

### `connect: no route to host` on macOS, while curl works

If `hue` cannot reach the bridge but `curl -k https://BRIDGE/api/config`
can, the network is fine and macOS is blocking the binary. macOS gates access
to devices on your local network per application, and it fails closed and
silently for command line tools — there is no prompt, just `EHOSTUNREACH`.

The giveaway is that Apple's own signed binaries are exempt while anything you
build is not:

```bash
nc -vz 192.0.2.10 443    # succeeds  (Apple-signed, entitled)
curl -sk https://192.0.2.10/api/config   # succeeds
go run . status              # "no route to host"
python3 -c "import socket; socket.create_connection(('192.0.2.10',443))"
                             # also fails - so it is not this tool
```

Connections to your router and to the internet keep working throughout, which
is why this looks so much like a routing problem and is not one.

To fix it, grant **Local Network** access to the application that *launches*
the binary — the permission is attributed to the responsible process, not to
`hue` itself:

1. **System Settings → Privacy & Security → Local Network**
2. Enable the terminal you run `hue` from (Terminal, iTerm, Ghostty, …). If you
   run it from an editor, an IDE or an agent, enable that application instead —
   it is the responsible process, not your terminal.
3. **Quit that application entirely (⌘Q) and reopen it.** The permission is
   cached per process, so a new tab or window is not enough.

If the application is not listed, or it is already enabled and still fails,
clear the cached decision so the prompt can fire again:

```bash
tccutil reset LocalNetwork
# or for one app:
tccutil reset LocalNetwork com.googlecode.iterm2
```

Then run the command again from a freshly launched terminal and allow it.

For a scheduled sweep, note that a `launchd` agent or cron job is its own
responsible process and needs its own approval; running the sweep under a
`launchd` agent you have approved once is the reliable arrangement.

### `bridge rejected the application key`

The key is missing or no longer valid. Re-run `hue auth`.

### `bridge certificate does not match the pinned fingerprint`

Expected if you replaced the bridge or reset it. Re-run `hue auth` to record
the new certificate, or set `"insecure": true` under `bridge` to stop pinning.

### The sweep found nothing

Run `hue sweep -v`, which prints why each group was left alone. The two usual
answers are the grace period on a first run (`held: first time seen on`) and
recent motion (`someone is probably there`).

### `no bridges found`

`hue auth` with no `--bridge` uses Signify's discovery service, which needs
outbound internet and only reports bridges sharing your public IP. Pass the
address directly instead: `hue auth --bridge 192.0.2.10`.

## License

MIT
