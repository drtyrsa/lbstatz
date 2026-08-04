package listenbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MaxMetadataPerGet is the number of MBIDs the metadata endpoints accept per request.
// 100 is served fine; 150 makes the gateway give up with a 502.
const MaxMetadataPerGet = 100

// Tag is a folksonomy tag on an entity. GenreMBID is set only for tags MusicBrainz
// recognises as genres — the rest are free-form ("seattle", "90s", "energetic").
type Tag struct {
	Tag       string
	GenreMBID string
	Count     int
}

type ArtistMeta struct {
	MBID      string
	Name      string
	Area      string
	Type      string
	Gender    string
	BeginYear int
	EndYear   int
	Tags      []Tag
}

type RecordingMeta struct {
	MBID             string
	Name             string
	Length           int64
	FirstReleaseDate string
	ReleaseMBID      string
	ReleaseGroupMBID string
	ReleaseName      string
	ReleaseYear      int
	Artists          []ArtistMeta
	RecordingTags    []Tag
	ReleaseGroupTags []Tag
}

// maxBatchSplits bounds how far a failing batch is subdivided. Two splits take a full batch
// down to a quarter, which is enough to get past a batch the gateway finds too heavy, while
// stopping a genuine outage from fanning out into a flood of doomed requests.
const maxBatchSplits = 2

// attemptsBeforeSplit is the retry budget for a batch that can still be halved. The full
// ladder waits about a minute before giving up, but a rejected batch usually succeeds the
// instant it is split, so waiting that long first is a minute wasted.
const attemptsBeforeSplit = 2

// Recordings fetches metadata for up to MaxMetadataPerGet recording MBIDs. MBIDs with no
// metadata (merged or removed recordings) are simply absent from the result, so callers
// must treat a missing key as a definitive miss rather than retrying it forever.
func (c *Client) Recordings(ctx context.Context, mbids []string) (map[string]RecordingMeta, error) {
	return c.recordings(ctx, mbids, maxBatchSplits)
}

// recordings retries a server-rejected batch as two smaller ones. Response size varies
// hugely with how heavily tagged the entities are, so a batch that trips the gateway can
// succeed once halved — and re-sending the identical request, as a plain retry does, cannot.
func (c *Client) recordings(ctx context.Context, mbids []string, splits int) (map[string]RecordingMeta, error) {
	if len(mbids) == 0 {
		return map[string]RecordingMeta{}, nil
	}
	q := url.Values{}
	q.Set("recording_mbids", strings.Join(mbids, ","))
	q.Set("inc", "artist tag release")
	// The endpoint 308-redirects without the trailing slash, dropping the query on some clients.
	endpoint := fmt.Sprintf("%s/1/metadata/recording/?%s", c.BaseURL, q.Encode())

	attempts := maxAttempts
	if splits > 0 && len(mbids) > 1 {
		attempts = attemptsBeforeSplit
	}

	var resp map[string]apiRecordingMeta
	if err := c.getWithAttempts(ctx, endpoint, &resp, attempts); err != nil {
		if !shouldSplit(err, len(mbids), splits) {
			return nil, err
		}
		mid := len(mbids) / 2
		left, err := c.recordings(ctx, mbids[:mid], splits-1)
		if err != nil {
			return nil, err
		}
		right, err := c.recordings(ctx, mbids[mid:], splits-1)
		if err != nil {
			return nil, err
		}
		for k, v := range right {
			left[k] = v
		}
		return left, nil
	}

	out := make(map[string]RecordingMeta, len(resp))
	for mbid, r := range resp {
		out[mbid] = r.normalize(mbid)
	}
	return out, nil
}

// shouldSplit reports whether a failed batch is worth retrying in halves.
func shouldSplit(err error, batchSize, splitsLeft int) bool {
	var serverErr *ServerError
	return splitsLeft > 0 && batchSize > 1 && errors.As(err, &serverErr)
}

// Artists fetches metadata for up to MaxMetadataPerGet artist MBIDs.
func (c *Client) Artists(ctx context.Context, mbids []string) (map[string]ArtistMeta, error) {
	if len(mbids) == 0 {
		return map[string]ArtistMeta{}, nil
	}
	q := url.Values{}
	q.Set("artist_mbids", strings.Join(mbids, ","))
	q.Set("inc", "tag")
	endpoint := fmt.Sprintf("%s/1/metadata/artist/?%s", c.BaseURL, q.Encode())

	var resp []apiArtist
	if err := c.get(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	out := make(map[string]ArtistMeta, len(resp))
	for _, a := range resp {
		m := a.normalize()
		if m.MBID == "" {
			continue
		}
		m.Tags = pickTags(a.Tag.Artist, m.MBID)
		out[m.MBID] = m
	}
	return out, nil
}

type apiTag struct {
	Tag        string `json:"tag"`
	GenreMBID  string `json:"genre_mbid"`
	Count      int    `json:"count"`
	ArtistMBID string `json:"artist_mbid"`
}

type apiArtist struct {
	ArtistMBID string `json:"artist_mbid"`
	MBID       string `json:"mbid"`
	Name       string `json:"name"`
	Area       string `json:"area"`
	Type       string `json:"type"`
	Gender     string `json:"gender"`
	BeginYear  int    `json:"begin_year"`
	EndYear    int    `json:"end_year"`
	Tag        struct {
		Artist []apiTag `json:"artist"`
	} `json:"tag"`
}

func (a apiArtist) normalize() ArtistMeta {
	return ArtistMeta{
		MBID:      firstNonEmpty(a.ArtistMBID, a.MBID),
		Name:      a.Name,
		Area:      a.Area,
		Type:      a.Type,
		Gender:    a.Gender,
		BeginYear: a.BeginYear,
		EndYear:   a.EndYear,
	}
}

type apiRecordingMeta struct {
	Artist struct {
		Artists []apiArtist `json:"artists"`
	} `json:"artist"`
	Recording struct {
		Name             string `json:"name"`
		Length           int64  `json:"length"`
		FirstReleaseDate string `json:"first_release_date"`
	} `json:"recording"`
	Release struct {
		MBID             string `json:"mbid"`
		Name             string `json:"name"`
		ReleaseGroupMBID string `json:"release_group_mbid"`
		Year             int    `json:"year"`
	} `json:"release"`
	Tag struct {
		Artist       []apiTag `json:"artist"`
		Recording    []apiTag `json:"recording"`
		ReleaseGroup []apiTag `json:"release_group"`
	} `json:"tag"`
}

func (r apiRecordingMeta) normalize(mbid string) RecordingMeta {
	m := RecordingMeta{
		MBID:             mbid,
		Name:             r.Recording.Name,
		Length:           r.Recording.Length,
		FirstReleaseDate: r.Recording.FirstReleaseDate,
		ReleaseMBID:      r.Release.MBID,
		ReleaseGroupMBID: r.Release.ReleaseGroupMBID,
		ReleaseName:      r.Release.Name,
		ReleaseYear:      r.Release.Year,
		RecordingTags:    pickTags(r.Tag.Recording, ""),
		ReleaseGroupTags: pickTags(r.Tag.ReleaseGroup, ""),
	}
	for _, a := range r.Artist.Artists {
		am := a.normalize()
		if am.MBID == "" {
			continue
		}
		// Artist tags arrive in one flat list for the whole credit, each carrying its own MBID.
		am.Tags = pickTags(r.Tag.Artist, am.MBID)
		m.Artists = append(m.Artists, am)
	}
	return m
}

// pickTags converts API tags, keeping only those belonging to artistMBID when it is set.
func pickTags(tags []apiTag, artistMBID string) []Tag {
	var out []Tag
	for _, t := range tags {
		if t.Tag == "" {
			continue
		}
		if artistMBID != "" && t.ArtistMBID != artistMBID {
			continue
		}
		out = append(out, Tag{Tag: t.Tag, GenreMBID: t.GenreMBID, Count: t.Count})
	}
	return out
}

// ParseYear reads the leading year from a MusicBrainz partial date ("1991", "1991-09",
// "1991-09-10"), returning 0 when there isn't one.
func ParseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1000 || y > 3000 {
		return 0
	}
	return y
}
