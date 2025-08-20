package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-flac/flacvorbis/v2"
	"github.com/go-flac/go-flac/v2"
)

const base = "https://musicbrainz.org/ws/2"

type Client struct {
	http *http.Client
	ua   string
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second},
		ua:   "GenreBender/1.0 (https://example.com/contact)",
	}
}

type Genre struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Tag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type recordingResp struct {
	Genres []Genre `json:"genres"`
	Tags   []Tag   `json:"tags"`
}

type releaseGroupResp struct {
	Genres []Genre `json:"genres"`
	Tags   []Tag   `json:"tags"`
}

type artistCred struct {
	Name string `json:"name"`
}

type relRelease struct {
	Title string `json:"title"`
}

type recording struct {
	ID     string       `json:"id"`
	Score  int          `json:"score"`
	Title  string       `json:"title"`
	Length int          `json:"length"`
	AC     []artistCred `json:"artist-credit"`
	Rels   []relRelease `json:"releases"`
}

type releaseGroup struct {
	ID    string       `json:"id"`
	Score int          `json:"score"`
	Title string       `json:"title"`
	AC    []artistCred `json:"artist-credit"`
	First string       `json:"first-release-data"`
}

func (c *Client) getJSON(ctx context.Context, url string, v any) error {
	var lastErr error

	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", c.ua)
		resp, err := c.http.Do(req)

		if err != nil {
			lastErr = err
		} else {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					body, _ := io.ReadAll(resp.Body)
					lastErr = json.Unmarshal(body, v)
				} else {
					b, _ := io.ReadAll(resp.Body)
					lastErr = fmt.Errorf("mb %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
				}
			}()
		}
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Duration(900+attempt*300) * time.Millisecond)
	}
	return lastErr
}

func (c *Client) RecordingGenres(ctx context.Context, recMBID string) (genres []string, tags []string, _ error) {
	var r recordingResp
	err := c.getJSON(ctx, fmt.Sprintf("%s/recording/%s?inc=genres+tags&fmt=json", base, recMBID), &r)
	if err != nil {
		return nil, nil, err
	}

	return pickGenres(r.Genres), pickTags(r.Tags), nil
}

func (c *Client) ReleaseGroupGenres(ctx context.Context, rgMBID string) (genres []string, tags []string, _ error) {
	var r releaseGroupResp
	err := c.getJSON(ctx, fmt.Sprintf("%s/release-group/%s?inc=genres+tags&fmt=json", base, rgMBID), &r)
	if err != nil {
		return nil, nil, err
	}

	return pickGenres(r.Genres), pickTags(r.Tags), nil
}

func (c *Client) SearchRecordingMBID(ctx context.Context, artist, title, album string, durationMS int) (string, error) {
	q := luceneAnd(
		field("artist", artist),
		field("recording", title),
		optionalField("release", album),
	)

	u := fmt.Sprintf("%s/recording?query=%s&limit=5&fmt=json", base, url.QueryEscape(q))

	var out struct {
		Recordings []recording `json:"recordings"`
	}

	if err := c.getJSON(ctx, u, &out); err != nil {
		return "", err
	}
	if len(out.Recordings) == 0 {
		return "", nil
	}

	for _, rc := range out.Recordings {
		if rc.Score == 100 {
			return rc.ID, nil
		}
		println("ID:", rc.ID, "score:", rc.Score, "title:", rc.Title)
	}

	return "", nil
}

func optionalField(k, v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return field(k, v)
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func field(k, v string) string {
	return fmt.Sprintf(`%s:%s`, k, escapeQuotes(v))
}

func luceneAnd(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " AND ")
}

func pickGenres(gs []Genre) []string {
	out := make([]string, 0, len(gs))
	seen := map[string]bool{}

	for _, g := range gs {
		name := strings.TrimSpace(g.Name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}

	return out
}

func pickTags(ts []Tag) []string {
	seen := map[string]int{}
	for _, t := range ts {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		seen[strings.ToLower(name)] += t.Count
	}
	out := make([]string, 0, len(seen))
	for name, cnt := range seen {
		if cnt >= 2 {
			out = append(out, name)
		}
	}
	return out
}

func extractFLACComments(filename string) (*flacvorbis.MetaDataBlockVorbisComment, int) {
	f, err := flac.ParseFile(filename)
	if err != nil {
		panic(err)
	}

	var cmt *flacvorbis.MetaDataBlockVorbisComment
	var cmtIdx int

	for idx, meta := range f.Meta {
		if meta.Type == flac.VorbisComment {
			cmt, err = flacvorbis.ParseFromMetaDataBlock(*meta)
			cmtIdx = idx
			if err != nil {
				panic(err)
			}
		}
	}
	return cmt, cmtIdx
}

func main() {
	fmt.Println("Yay! GenreBender")
	// args := os.Args[1:]
	filename := os.Args[1]

	vb, _ := extractFLACComments(filename)
	// vb, _ := extractFLACComments("./goreshit - tomboyish love for daughter - 05 strawberry cheesecake.flac")

	// fmt.Println(vb.Comments, "count:", count)
	title, _ := vb.Get(flacvorbis.FIELD_TITLE)
	album, _ := vb.Get(flacvorbis.FIELD_ALBUM)
	artist, _ := vb.Get(flacvorbis.FIELD_ARTIST)

	fmt.Println("Title:", title[0])
	fmt.Println("Album:", album[0])
	fmt.Println("Artist:", artist[0])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// recMBID, _ := client.SearchRecordingMBID(ctx, "goreshit", "crabs", "tomboyish love for daughter", 0)
	recMBID, _ := client.SearchRecordingMBID(ctx, artist[0], title[0], album[0], 0)

	//genres, tags, err := client.ReleaseGroupGenres(ctx, "ba03bce9-9f91-42ce-9f12-519dae3f734b")
	genres, tags, err := client.RecordingGenres(ctx, recMBID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Genres:", genres)
	fmt.Println("Tags:", tags)
}
