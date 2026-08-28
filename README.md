# lbstatz

A command-line tool for local statistics over your [ListenBrainz](https://listenbrainz.org)
listening history. It downloads your listens into a local SQLite database and lets you
slice them however you like — top artists, albums and tracks, genres and countries, how old
the music you play is, or the raw listen log — with filters that ListenBrainz's own stats
pages don't offer.

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

Download listens into the local database, then fetch metadata for anything new, showing
live progress.

```sh
lbstatz sync                 # incremental: only listens newer than what you already have
lbstatz sync --remap         # also re-check listens that arrived unidentified
lbstatz sync --from-scratch  # wipe and re-download the entire history
lbstatz sync --no-enrich     # listens only, skipping the metadata pass
```

Incremental sync is the default: it tops up listens newer than what you have and, until
the whole history has been downloaded once, keeps backfilling older listens from where it
left off. It's fully resumable — interrupt any time with Ctrl-C, and network hiccups are
retried automatically. For a large history the first sync may take several runs; just run
`sync` again until it reports `Done`. Progress is shown against your total listen count.

Once the listens are in, `sync` runs [`enrich`](#enrich) to fill in release dates, artist
countries and genres for anything it hasn't seen before. That pass is resumable too.

#### `--remap`

Plenty of listens arrive with no MBIDs at all — a player submitted nothing but names, and
ListenBrainz's mapper couldn't match them. Those listens are dead weight for the genre,
country and era stats, which are all keyed off the recording MBID.

That's not permanent. MusicBrainz gains recordings constantly, ListenBrainz rebuilds its
mapper index, and mappings you make by hand on the website land too — so a listen that was
unidentifiable last year may well be identified now. `--remap` goes back and looks:

```sh
lbstatz sync --remap
```

It doesn't re-walk your whole history. It jumps from one unidentified listen to the next,
skipping the stretches that are already fully mapped, so a history whose gaps are clustered
— one player that submitted poor metadata for a few months — costs a handful of requests.
Scattered gaps cost no more than a full walk would. Anything it identifies flows straight
into the `enrich` pass in the same run, so the new MBIDs come back with metadata attached.

Run it occasionally rather than every time; a plain `sync` is enough day to day. It reports
how many listens it matched and how many still have none, and that residue never gets
marked as settled — the point is that MusicBrainz keeps growing, so it's always worth
another look later.

Two things it won't do. It's skipped while the history is still downloading for the first
time, since the backfill is already fetching those listens with current mappings. And it
can't be combined with `--from-scratch`, which re-downloads everything anyway.

You get a little of this for free without the flag: every `sync` walks back through the
listens it already has until it meets stored ones, and that overlap now picks up any
mappings it has gained. That covers the common case of ListenBrainz identifying a listen an
hour after you scrobbled it. `--remap` is for reaching further back than the overlap.

### `enrich`

Fetch metadata for the recordings and artists your listens reference. `sync` does this
automatically, so you rarely need it directly.

```sh
lbstatz enrich               # fill in whatever is still missing
lbstatz enrich --refresh     # discard stored metadata and fetch it again
```

Metadata is keyed by MBID, not by listen, so this never re-downloads listens and re-running
it costs nothing once it has caught up. Use `--refresh` periodically: unlike your listens,
which only ever grow, MusicBrainz genres and release dates get *edited*, and corrections
land on entities that were already fetched.

Three phases run in order, each resumable on its own:

| Phase | Source | Rate |
|-------|--------|------|
| Recordings | ListenBrainz `/1/metadata/recording` | 100 per request, 30 requests/min |
| Artists | ListenBrainz `/1/metadata/artist` | only artists no recording covered |
| Countries | MusicBrainz search, then per-artist lookup | 100 per search, 1 request/sec |

Expect the first pass over a large history to take a while — roughly a thousand recordings
a minute, and the country phase is capped at one MusicBrainz request per second. Interrupt
it whenever you like; it picks up where it left off.

It is meant to be left alone until it finishes. If MusicBrainz starts throttling, the pass
waits it out — up to about ten minutes, backing off further each time, and then continuing
at a slower steady pace that eases back once requests are getting through again. The
progress line says what it is waiting on and counts the wait down, so a pause is never
mistaken for a hang. Only a fault that outlasts that stops the run, and what it had already
fetched is saved either way.

The country phase needs MusicBrainz because ListenBrainz reports an artist's *area*, which
is often a city — "Berlin", "Washington, D.C." — rather than a country. The batched search
resolves most artists at once; those it leaves blank (its index reports nothing for
subdivisions like England or Scotland) fall back to a per-artist lookup that resolves them
properly to `GB`.

### `top`

Your most-played artists, albums, tracks, genres or countries. Filters compose, so you can
drill in.

```sh
lbstatz top artists
lbstatz top tracks  --artist "Radiohead"                 # top tracks by an artist
lbstatz top tracks  --album  "OK Computer"               # top tracks on an album
lbstatz top albums  --artist "Radiohead" --from 2024-01-01 --to 2024-12-31
lbstatz top albums  --last 30                            # the last 30 days
lbstatz top tracks  --track  "Karma Police"              # one row: total plays of a track
lbstatz top artists -n 20 --json
lbstatz top genres    -n 15                              # needs enrich
lbstatz top countries --last 365                         # needs enrich
lbstatz top genres    --min-votes 5                      # ignore thinly-voted tags
```

### `eras`

How old the music you listen to is — the answer to "am I listening to newer music than I
used to?".

```sh
lbstatz eras                                             # histogram by release decade
lbstatz eras --bucket year                               # one row per release year
lbstatz eras --trend                                     # the trend, per year of listening
lbstatz eras --artist "Radiohead"                        # same filters as everything else
lbstatz eras --from 2024-01-01 --to 2024-12-31           # the era mix of one listening year
```

`--bucket year` swaps decade buckets for single years. Buckets with no listens are left out
rather than printed as zeroes, so a sparse back catalogue doesn't bury the shape under empty
rows — every row is labelled, so gaps stay visible.

To break the histogram down by *when you listened* rather than when the music came out,
filter to one year at a time:

```fish
for y in (seq 2020 2026)
    echo "== $y =="
    lbstatz eras --from $y-01-01 --to $y-12-31
end
```

`--trend` reports two numbers per year you listened. **Median release year** rises more or
less automatically as time passes. **Median age** — how old the music was when you played
it — is the one that answers the question: if it climbs, you really did drift toward older
music.

```
Year  Listens  Median release  Median age
2021  14022    2018            3 yr
2022  11890    2016            6 yr
2023  13440    2011            12 yr
```

### `listens`

The raw listen log, most recent first — same filters as `top`.

```sh
lbstatz listens --artist "Björk" -n 50
lbstatz listens --album "Homogenic" --from 2023-06-01 --json
lbstatz listens --last 7                                 # everything from the last week
```

## Flags

Shared by `top`, `eras` and `listens`:

| Flag | Meaning |
|------|---------|
| `--from` | Start date, inclusive. `YYYY-MM-DD` or RFC3339. |
| `--to` | End date, inclusive (a bare date covers the whole day). |
| `--last` | Last N days, today included (`--last 7` = today plus the previous six). Cannot be combined with `--from`/`--to`. |
| `--artist` | Filter by artist, as a name or MBID. |
| `--album` | Filter by album (release), as a name or MBID. |
| `--track` | Filter by track (recording), as a name or MBID. |
| `-n, --limit` | Cap the number of rows (`0` = all, the default). |
| `--json` | Emit JSON instead of text. |

`top genres` also takes `--min-votes` (default `1`). `eras` takes `--bucket decade|year`
(default `decade`) and `--trend`; the two are mutually exclusive, since `--trend` replaces
the release axis that `--bucket` shapes.

## How things are matched

- **Names vs MBIDs.** Any `--artist/--album/--track` value that looks like a UUID is
  treated as an MBID; otherwise it's a name. Name matching is exact and case-insensitive.
- **Albums** are releases exactly as your listens report them — a deluxe edition and the
  standard edition count separately.
- **Missing MBIDs.** Many listens carry no MBIDs. Albums and tracks are keyed by MBID when
  present (so different credits of the same recording or release merge) and fall back to
  name otherwise, so nothing is dropped — though the genre, country and era stats can't see
  those listens at all. [`sync --remap`](#--remap) goes back and tries to identify them.
- **Artists** are grouped by their credited name, case-insensitively — the way your listens
  are labelled — so everything scrobbled as "Skrillex" is one artist, while a genuinely
  different credit like "Skrillex & Diplo" is its own. Each artist is tagged with its
  most-common MBID. Filtering `--artist <name>` matches that exact credit; `--artist <mbid>`
  matches *every* listen that artist appears on, including collaborations.

## How the metadata stats are built

- **Coverage.** Genres, countries and eras can only speak for listens that carry the right
  MBID and whose entities have metadata, so every one of them prints what share of the range
  it actually accounts for. A partly-enriched database should never read as a complete
  picture.
- **Release dates** come from the recording's *first* release date, not the release you
  happened to play. Otherwise a 2015 remaster of a 1972 album would count as 2015 and make
  your taste look far more current than it is.
- **Genres** are taken from the most specific level that has any: the recording's own tags,
  then its release group's, then the credited artists'. Only tags MusicBrainz recognises as
  genres count — the raw tag list is full of folksonomy noise like `seattle`, `90s` and
  `energetic`. Recording-level tags alone cover roughly two thirds of a library; the
  fallback chain gets it close to complete. A listen tagged with several genres counts
  toward each, so the genre counts sum to more than your number of listens.
- **Countries** are the credited artist's country, attributed to the listen's primary artist
  so a collaboration counts once. This is where the artist is *from*, which is not always
  where the music sounds like it's from.

## Tests

```sh
go test ./...
```

## License

[MIT](LICENSE)
