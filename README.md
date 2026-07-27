# lbstatz

A command-line tool for local statistics over your [ListenBrainz](https://listenbrainz.org)
listening history. It downloads your listens into a local SQLite database and lets you
slice them however you like — top artists, albums and tracks, or the raw listen log —
with filters that ListenBrainz's own stats pages don't offer.

## Install

```sh
go build -o lbstatz .
```

This produces a single self-contained binary (no cgo, pure-Go SQLite).

## Configure

`lbstatz` reads a TOML config file named `lbstatz.toml` sitting next to the binary.
Override the location with `--config/-c`.

```toml
token = "your-listenbrainz-token"   # optional for public listens
username = "your-username"          # required
# db_path = "~/.local/share/lbstatz/lbstatz.db"   # defaults to lbstatz.db next to this file
```

See [`lbstatz.example.toml`](lbstatz.example.toml). Your token is secret — the default
`.gitignore` keeps `lbstatz.toml` out of version control.

## Usage

### `sync`

Download listens into the local database, showing live progress.

```sh
lbstatz sync                 # incremental: only listens newer than what you already have
lbstatz sync --from-scratch  # wipe and re-download the entire history
```

Incremental sync is the default and only fetches what's new. Interrupt any time with
Ctrl-C; whatever was downloaded is kept and the next run resumes.

### `top`

Your most-played artists, albums or tracks. Filters compose, so you can drill in.

```sh
lbstatz top artists
lbstatz top tracks  --artist "Radiohead"                 # top tracks by an artist
lbstatz top tracks  --album  "OK Computer"               # top tracks on an album
lbstatz top albums  --artist "Radiohead" --from 2024-01-01 --to 2024-12-31
lbstatz top tracks  --track  "Karma Police"              # one row: total plays of a track
lbstatz top artists -n 20 --json
```

### `listens`

The raw listen log, most recent first — same filters as `top`.

```sh
lbstatz listens --artist "Björk" -n 50
lbstatz listens --album "Homogenic" --from 2023-06-01 --json
```

## Flags

Shared by `top` and `listens`:

| Flag | Meaning |
|------|---------|
| `--from` | Start date, inclusive. `YYYY-MM-DD` or RFC3339. |
| `--to` | End date, inclusive (a bare date covers the whole day). |
| `--artist` | Filter by artist, as a name or MBID. |
| `--album` | Filter by album (release), as a name or MBID. |
| `--track` | Filter by track (recording), as a name or MBID. |
| `-n, --limit` | Cap the number of rows (`0` = all, the default). |
| `--json` | Emit JSON instead of text. |

## How things are matched

- **Names vs MBIDs.** Any `--artist/--album/--track` value that looks like a UUID is
  treated as an MBID; otherwise it's a name. Name matching is exact and case-insensitive.
- **Albums** are releases exactly as your listens report them — a deluxe edition and the
  standard edition count separately.
- **Missing MBIDs.** Many listens carry no MBIDs. Entities are keyed by MBID when present
  and fall back to name text otherwise, so nothing is dropped.
- **Collaborations.** Each distinct artist credit ("Röyksopp & Susanne Sundfør") is its
  own artist. Filtering `--artist <name>` matches that exact credit, while filtering
  `--artist <mbid>` matches *every* credit that artist appears on — handy for "all albums
  this artist plays on, including collaborations".

## Tests

```sh
go test ./...
```
