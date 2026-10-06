# hue

A command line tool for Philips Hue, written to solve one specific annoyance:
**Smart home apps fire lighting commands without verifying them.** It sends "off" and
assumes it worked. When a Zigbee message is lost, a light stays on and nothing
notices. `hue sweep` notices.

```
$ hue sweep
2026-10-05T13:12:03Z dry-run off group="Hallway" kind=room rule=motion-idle light="Hall 1" reason="no motion for 23m (threshold 15m)"
2026-10-05T13:12:03Z dry-run off group="Hallway" kind=room rule=motion-idle light="Hall 2" reason="no motion for 23m (threshold 15m)"
2026-10-05T13:12:03Z dry-run off group="Kitchen" kind=room rule=outlier light="Kitchen 3" reason="1 of 5 lights on (20% of the group)"
2026-10-05T13:12:03Z dry-run swept lights=3 groups=2 hint="nothing changed; add --no-dry-run to apply"
```

Every line starts with the time of the sweep, so the output reads the same in
a terminal and in a log file. `dry-run` marks a preview; a real run leaves it
out. A run with nothing to do is a single `swept lights=0 groups=0` line.

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
- That protection follows the lights: a light in a room with recent motion is
  never switched off by a zone that includes it, even when the zone itself
  has no sensor and looks like it has stragglers.

A sensor is only consulted when it is enabled and reports a `motion_report`
timestamp. Disabled or ancient sensors fall through to the outlier rule.

Zones have no sensors of their own, so a zone borrows the sensors of the rooms
its lights are in — but only when **every** light in the zone is in a room
with a sensor. A sensor sees its own room and nothing else: a "Lower Level"
zone spanning a sensed kitchen and an unsensed lounge is judged by the
outlier rule, so one room going quiet cannot switch off the other.

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

Rooms and zones overlap, so a sweep works in rounds. If a zone has 4 lights on
it is left alone, but if two of those are switched off as stragglers in a
neighbouring zone, its remaining 2 now qualify too. Each round treats the
lights already chosen as off and looks again, until nothing more qualifies.
Groups that only qualified this way say `once other groups were swept`. To
keep a lamp on whatever its neighbours do, list it in `exclude_lights`.

### No memory between runs

The sweep keeps no state of its own. Each run decides from what the bridges
report at that moment: the lights' on/off state and the motion sensors' own
timestamps. Run it every hour or few hours and a light that was lost stays on
until the next run at most.

The Hue API has no "when did this light turn on" timestamp, so the outlier rule
cannot tell a straggler from a lamp switched on a minute ago in a room with no
sensor. Where that matters, a motion sensor or `exclude_lights` protects it.

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

### More than one bridge

Several bridges can be combined, for instance when lights and motion sensors
are spread across them. Pair each with a name:

```bash
hue auth --name main  --bridge 192.0.2.10
hue auth --name annex --bridge 192.0.2.11
```

Every command then reads both bridges and treats them as one home:

- **Rooms and zones with the same name are merged**, ignoring case. A Kitchen
  on each bridge becomes one Kitchen holding both bridges' lights and sensors,
  so a sensor on one bridge decides for lights on the other.
- **`room_aliases`** merges rooms named differently on each bridge.
- **Changes go to the bridge that owns each light**; `hue off Kitchen` sends
  one grouped request per bridge.
- **`hue ls` adds a BRIDGE column**, and `hue probe` takes `--bridge <name>`.
- **If a bridge cannot be read, the sweep is skipped** for that run rather than
  judging rooms on half the evidence: a room's sensor may be on the missing
  bridge. `hue ls` and `hue status` carry on with a warning.

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
hue sweep --idle 30m             # override the motion threshold for one run
hue sweep -v                     # explain the groups it left alone
hue sweep --json                 # the full plan and results as JSON
hue off Kitchen --no-dry-run     # flags may come before or after the target
hue probe                        # what resource types does this bridge have?
hue probe --bridge annex motion  # one bridge's raw motion resources
```

`hue sweep -v` is the command to reach for when it did not do what you
expected — it prints the reasoning for every group it skipped:

```
$ hue sweep -v
2026-10-05T13:12:03Z dry-run note group="Lounge" msg="4/4 on, no rule applies (motion 1m ago, under the 15m threshold, so someone is probably there)"
2026-10-05T13:12:03Z dry-run note group="Study" msg="3/5 on, no rule applies (no usable motion sensor; 60% of the group is on, over max_on_fraction 34%)"
2026-10-05T13:12:03Z dry-run swept lights=0 groups=0
```

## Configuration

`hue config init` writes a fully populated file. Location, in order of
precedence: `--config`, `$HUE_CONFIG`, `$XDG_CONFIG_HOME/hue/config.json`,
`~/.config/hue/config.json`.

```json
{
  "bridges": [
    {
      "name": "main",
      "host": "192.0.2.10",
      "app_key": "your-application-key",
      "cert_sha256": "3f2a...",
      "timeout": "10s"
    },
    {
      "name": "annex",
      "host": "192.0.2.11",
      "app_key": "another-application-key",
      "cert_sha256": "9b1c..."
    }
  ],
  "room_aliases": {"Yard": "Terrace"},
  "sweep": {
    "rooms": ["Kitchen", "Hallway", "Outside"],
    "exclude_lights": ["Night Light", "porch *"],
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
| `bridges` | written by `hue auth` | Each bridge's name, address, key and pinned certificate. `name` is required once there are two |
| `room_aliases` | `{}` | Rename rooms or zones before bridges are merged, case-insensitive |
| `sweep.rooms` | `[]` (everything) | Rooms **and zones** to sweep, by name, case-insensitive |
| `sweep.exclude_lights` | `[]` | Never switch these off; wildcards allowed |
| `sweep.include_plugs` | `true` | Allow smart plugs to be switched off |
| `sweep.outlier.*` | see above | The outlier rule's thresholds |
| `sweep.motion.idle_threshold` | `15m` | How long a room must be still |

Durations are strings: `"45s"`, `"15m"`, `"1h30m"`. Any key you leave out keeps
its default. `enabled` is deliberately a tri-state — omit it for the default,
or set it explicitly to `false` to turn a rule off.

Environment overrides: `HUE_BRIDGE_HOST`, `HUE_APP_KEY` (only with a single
bridge), `HUE_CONFIG`.

Older files with a single `"bridge": {...}` entry still load, as a one-bridge
list; the next `hue auth` rewrites it as `bridges`.

## Running it on a schedule

The sweep is meant to run occasionally, every hour to every few hours, to catch
lights that were lost.

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
  </array>
  <key>StartInterval</key><integer>3600</integer>
  <key>StandardOutPath</key><string>/tmp/hue-sweep.log</string>
  <key>StandardErrorPath</key><string>/tmp/hue-sweep.err</string>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.brybry192.hue.sweep.plist
```

Or cron, hourly:

```cron
0 * * * * /usr/local/bin/hue sweep --no-dry-run >> /tmp/hue-sweep.log 2>&1
```

The log then gains one line per light switched off and one summary line per
run, so a quiet run costs a single line. Add `-v` to also log why each group
was left alone, as `note` lines.

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
  merge.go                  several bridges -> one home
internal/config/            config file, defaults, validation
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
  idle and the presence veto, sensors that cannot be trusted, overlapping
  groups swept in rounds, exclusions and wildcards, plugs, room scoping, and a light that belongs to
  both a room and a zone.
- **`internal/hue`** — assembling rooms and zones from raw resources, including
  multi-light devices, dangling references and sensor inheritance; device
  classification across real Hue model IDs; duration formatting.
- **`internal/config`** — defaults surviving a partial file, the tri-state
  `enabled`, validation, several bridges, `0600` permissions and atomic
  writes.
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
the new certificate, or set `"insecure": true` on that bridge to stop pinning.

### The sweep found nothing

Run `hue sweep -v`, which prints why each group was left alone. The usual
answers are recent motion (`someone is probably there`) and too many lights on
for them to look forgotten (`exceeds max_on_count`).

### `no bridges found`

`hue auth` with no `--bridge` uses Signify's discovery service, which needs
outbound internet and only reports bridges sharing your public IP. Pass the
address directly instead: `hue auth --bridge 192.0.2.10`.

## License

MIT
