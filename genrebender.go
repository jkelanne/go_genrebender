package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io/fs"

	// "errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-flac/flacvorbis/v2"
	"github.com/go-flac/go-flac/v2"
	"github.com/urfave/cli/v3"
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
	// fmt.Printf("%s/release-group/%s?inc=genres+tags&fmt=json\n", base, rgMBID)
	if err != nil {
		return nil, nil, err
	}

	// fmt.Println("genres:", r.Genres)
	// fmt.Println("Tags:", r.Tags)
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

	// for _, rc := range out.Recordings {
	// 	if rc.Score == 100 {
	// 		return rc.ID, nil
	// 	}
	// 	println("ID:", rc.ID, "score:", rc.Score, "title:", rc.Title)
	// }
	best := pickBestRecording(out.Recordings, artist, title, album, durationMS)
	return best, nil
}

func (c *Client) SearchReleaseGroupMBID(ctx context.Context, artist, album string) (string, error) {
	q := luceneAnd(
		field("artist", artist),
		field("releasegroup", album),
	)
	u := fmt.Sprintf("%s/release-group?query=%s&limit=5&fmt=json", base, url.QueryEscape(q))

	var out struct {
		ReleaseGroups []releaseGroup `json:"release-groups"`
	}

	if err := c.getJSON(ctx, u, &out); err != nil {
		fmt.Println("GetJSON failed")
		return "", err

	}

	if len(out.ReleaseGroups) == 0 {
		fmt.Println("out.ReleaseGroups size is 0")
		return "", nil
	}

	// fmt.Println(out.ReleaseGroups)
	best := pickBestReleaseGroup(out.ReleaseGroups, artist, album)
	return best, nil
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

func escapeLucene(s string) string {
	// escape Lucene special chars
	specials := []string{"+", "-", "&&", "||", "!", "(", ")", "{", "}", "[", "]",
		"^", "\"", "~", "*", "?", ":", "\\", "/"}
	for _, ch := range specials {
		s = strings.ReplaceAll(s, ch, `\`+ch)
	}
	return s
}

func field(k, v string) string {
	// return fmt.Sprintf(`%s:"%s"`, k, escapeQuotes(v))
	return fmt.Sprintf(`%s:"%s"`, k, escapeLucene(v))
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

func pickBestRecording(cands []recording, artist, title, album string, durMS int) string {
	type scored struct {
		id    string
		score int
	}

	artist = strings.ToLower(artist)
	title = strings.ToLower(title)
	album = strings.ToLower(album)

	items := make([]scored, 0, len(cands))
	for _, rc := range cands {
		s := rc.Score
		if ciContains(rc.Title, title) {
			s += 5
		}
		if hasArtist(rc.AC, artist) {
			s += 5
		}
		if album != "" && hasReleaseTitle(rc.Rels, album) {
			s += 4
		}

		if durMS > 0 && rc.Length > 0 {
			diff := abs(rc.Length - durMS)
			switch {
			case diff <= 1500:
				s += 6
			case diff <= 3000:
				s += 3
			case diff <= 7000:
				s += 1
			}
		}
		items = append(items, scored{rc.ID, s})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].score > items[j].score })
	if len(items) == 0 {
		return ""
	}
	return items[0].id
}

func pickBestReleaseGroup(cands []releaseGroup, artist, album string) string {
	type scored struct {
		id    string
		title string
		score int
	}

	artist = strings.ToLower(artist)
	album = strings.ToLower(album)

	items := make([]scored, 0, len(cands))
	for _, rg := range cands {
		// fmt.Println(rg)
		s := rg.Score
		if ciContains(rg.Title, album) {
			s += 5
		}
		if hasArtist(rg.AC, artist) {
			s += 5
		}
		if yr := year(rg.First); yr > 0 && yr >= 1950 && yr <= time.Now().Year()+1 {
			s += 1
		}
		items = append(items, scored{rg.ID, rg.Title, s})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].score > items[j].score })
	if len(items) == 0 {
		return ""
	}
	return items[0].id
}

func ciContains(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func hasArtist(ac []artistCred, want string) bool {
	want = strings.ToLower(want)
	for _, a := range ac {
		if strings.Contains(strings.ToLower(a.Name), want) {
			return true
		}
	}
	return false
}

func hasReleaseTitle(rs []relRelease, want string) bool {
	want = strings.ToLower(want)
	for _, r := range rs {
		if strings.Contains(strings.ToLower(r.Title), want) {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func year(s string) int {
	if len(s) >= 4 {
		if y, err := strconv.Atoi(s[:4]); err == nil {
			return y
		}
	}
	return 0
}

func extractFLACComments(filename string) (*flacvorbis.MetaDataBlockVorbisComment, int) {
	f, err := flac.ParseFile(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

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

func addFLACGenreComment(filename string, genres []string) {
	f, err := flac.ParseFile(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	cmts, idx := extractFLACComments(filename)
	if cmts == nil && idx > 0 {
		cmts = flacvorbis.New()
	}

	// cmts.Add(flacvorbis.FIELD_GENRE, genre)
	for _, g := range genres {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if err := cmts.Add(flacvorbis.FIELD_GENRE, g); err != nil {
			fmt.Printf("Something went wrong: %v", err)
			return
		}
	}
	cmtsmeta := cmts.Marshal()
	if idx > 0 {
		f.Meta[idx] = &cmtsmeta
	} else {
		f.Meta = append(f.Meta, &cmtsmeta)
	}
	f.Save("cached.flac")
}
func getGenres(filename string, verbose bool) (genres, tags []string, err error) {
	// TODO: Move these somewhere else and pass the *Disk* object around.. or reference to it
	cache_path, err := os.UserCacheDir()
	if err != nil {
		log.Fatal("Not sure what happened, %w", err)
	}

	cache_path = fmt.Sprintf("%s/genrebender/", cache_path)
	cache := Disk{
		Dir:        cache_path,
		TTL:        30 * 24 * time.Hour,
		SearchTTL:  7 * 24 * time.Hour,
		APIVersion: 1,
	}

	vb, _ := extractFLACComments(filename)

	title, _ := vb.Get(flacvorbis.FIELD_TITLE)
	album, _ := vb.Get(flacvorbis.FIELD_ALBUM)
	artist, _ := vb.Get(flacvorbis.FIELD_ARTIST)

	if verbose {
		fmt.Println("Title:", title[0])
		fmt.Println("Album:", album[0])
		fmt.Println("Artist:", artist[0])
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// recMBID, _ := client.SearchRecordingMBID(ctx, "goreshit", "crabs", "tomboyish love for daughter", 0)
	// TODO: We should normalise these variables somehow. no need to have arrays here..
	recMBID, _ := client.SearchRecordingMBID(ctx, artist[0], title[0], album[0], 0)
	recKey := "rec:" + recMBID
	if e, stale, _ := cache.Get(ctx, recKey, false); e != nil && !stale {
		fmt.Println("Values read from the cache")
		return e.Genres, e.Tags, nil
	}

	//genres, tags, err := client.ReleaseGroupGenres(ctx, "ba03bce9-9f91-42ce-9f12-519dae3f734b")
	genres, tags, err = client.RecordingGenres(ctx, recMBID)
	if err != nil {
		// log.Fatal(err)
		return nil, nil, err
	}
	// NOTE: If genre is empty try to get them from the ReleaseGroup
	if len(genres) == 0 {
		relGrpMBID, _ := client.SearchReleaseGroupMBID(ctx, artist[0], album[0])
		if verbose {
			fmt.Println("Release-Group MBID:", relGrpMBID)
		}
		genres, tags, err = client.ReleaseGroupGenres(ctx, relGrpMBID)
		if err != nil {
			log.Fatal(err)
		}
	}

	// fmt.Println("Genres:", genres)
	// fmt.Println("Tags:", tags)
	_ = cache.Put(ctx, recKey, Entry{
		Source: "recording",
		MBID:   recMBID,
		Genres: genres,
		Tags:   tags,
		Raw:    json.RawMessage{},
	})

	// NOTE: This should be in the ADD command..
	// if !c.Bool("check-only") != true {
	// 	addFLACGenreComment(filename, genres)
	// }
	return genres, tags, nil
}

func main() {
	// Check this for full example:
	//    https://github.com/urfave/cli/blob/main/docs/v3/examples/full-api-example.md
	var filename string
	cmd := &cli.Command{
		Name:      "GenreBender",
		Version:   "v0.0.1",
		Usage:     "Just testing some things",
		UsageText: "What is this used for?",
		Commands: []*cli.Command{
			{
				Name:    "add",
				Aliases: []string{"a"},
				Usage:   "add genres to file (WIP)",
				Arguments: []cli.Argument{
					&cli.StringArg{
						Name:        "filename",
						Destination: &filename,
					},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					// fmt.Println("Adding genres to file:", filename)
					info, err := os.Stat(filename)
					if err != nil {
						fmt.Println(err)
						return nil
					}

					if info.IsDir() {
						fmt.Printf("[%s] is a directory\n", filename)
					} else {
						fmt.Printf("[%s] is a file\n", filename)
					}
					return nil
				},
			},
			{
				Name:    "check",
				Aliases: []string{"c"},
				Usage:   "check genres (WIP)",
				Arguments: []cli.Argument{
					&cli.StringArg{
						Name:        "filename",
						Destination: &filename,
					},
				},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}},
					// &cli.BoolFlag{Name: "check-only", Aliases: []string{"c"}},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					cache_path, err := os.UserCacheDir()
					if err != nil {
						log.Fatal("Not sure what happened, %w", err)
					}
					cache_path = fmt.Sprintf("%s/genrebender/", cache_path)

					// NOTE: Testing caching
					sum := sha1.Sum([]byte(strings.TrimSpace(filename)))
					fmt.Printf("SHA1: [%s] => %s%s.json\n", filename, cache_path, hex.EncodeToString(sum[:]))
					// cacheDir, _ := os.UserCacheDir()
					// fmt.Println("user cache_dir:", cacheDir)
					if c.Bool("verbose") {
						fmt.Println("Checking genres...", filename)
					}

					if filename == "" {
						// This will do for now, but we should invoke a usage error here, but don't know how.
						return fmt.Errorf("Missing file")
					}

					info, err := os.Stat(filename)
					if err != nil {
						fmt.Println(err)
						return nil
					}

					if info.IsDir() {
						root := os.DirFS(filename)
						flacFiles, err := fs.Glob(root, "*.flac")
						if err != nil {
							fmt.Println(err)
							return nil
						}

						// var files []string
						for _, v := range flacFiles {
							// fmt.Println(v)
							genres, _, err := getGenres(fmt.Sprintf("%s/%s", filename, v), c.Bool("verbose"))
							if err != nil {
								log.Fatal(err)
							}
							fmt.Printf("[%s] :: %s\n", v, strings.Join(genres, ","))
							time.Sleep(2 * time.Second)

						}
						return nil
					} else {
						genres, tags, err := getGenres(filename, c.Bool("verbose"))
						if err != nil {
							log.Fatal(err)
						}
						fmt.Printf("genres: %s\n", strings.Join(genres, ","))
						fmt.Printf("tags: %s\n", strings.Join(tags, ","))
					}

					return nil
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
