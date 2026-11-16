package trailerdl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// YouTube resolves a YouTube video to its streams.
type YouTube struct {
	HTTP *http.Client
	Base string // "" = https://www.youtube.com (tests: a fake's address)
}

const youtubeOrigin = "https://www.youtube.com"

var reVisitorData = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)

// maxPage is the most of a page or an answer read.
const maxPage = 8 << 20

// Resolve asks YouTube for the streams of the video key, as the visionos client: the watch
// page gives a visitor id (without one YouTube asks to sign in), the player gives the streams.
func (y YouTube) Resolve(ctx context.Context, key string) (Source, error) {
	visitor, err := y.visitorData(ctx, key)
	if err != nil {
		return Source{}, err
	}
	pr, err := y.player(ctx, key, visitor)
	if err != nil {
		return Source{}, err
	}
	// A paid film's page plays its trailer instead: followed once.
	if next := pr.PlayabilityStatus.ErrorScreen.YpcTrailer.TrailerVideoID; pr.PlayabilityStatus.Status != "OK" && next != "" {
		if pr, err = y.player(ctx, next, visitor); err != nil {
			return Source{}, err
		}
		key = next
	}
	if s := pr.PlayabilityStatus; s.Status != "OK" {
		return Source{}, fmt.Errorf("YouTube: %s (%s): %w", s.Reason, s.Status, ErrUnavailable)
	}
	if pr.VideoDetails.VideoID != key {
		return Source{}, fmt.Errorf("YouTube answered for video %q, not %q", pr.VideoDetails.VideoID, key)
	}
	var streams []Stream
	for _, f := range append(pr.StreamingData.AdaptiveFormats, pr.StreamingData.Formats...) {
		if f.URL == "" || len(f.DRMFamilies) > 0 { // a signatureCipher needs YouTube's JavaScript
			continue
		}
		size, _ := strconv.ParseInt(f.ContentLength, 10, 64)
		streams = append(streams, Stream{URL: f.URL, MIME: f.MimeType, Height: f.Height, Bitrate: f.Bitrate, Size: size})
	}
	video, okV := pickVideo(streams, MaxHeight)
	audio, okA := pickAudio(streams)
	if !okV || !okA {
		return Source{}, fmt.Errorf("YouTube gave no stream this client can read: %w", ErrUnavailable)
	}
	return Source{Video: video, Audio: audio, Header: http.Header{"User-Agent": {desktopUA}}, Wait: pr.startAdWait()}, nil
}

func (y YouTube) base() string {
	if y.Base != "" {
		return y.Base
	}
	return youtubeOrigin
}

func (y YouTube) client() *http.Client {
	if y.HTTP != nil {
		return y.HTTP
	}
	return http.DefaultClient
}

// visitorData reads the visitor id of the watch page's ytcfg ("" when it has none).
func (y YouTube) visitorData(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		y.base()+"/watch?v="+url.QueryEscape(key)+"&bpctr=9999999999&has_verified=1", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Cookie", "SOCS=CAI; PREF=hl=en&tz=UTC")
	page, err := y.fetch(req)
	if err != nil {
		return "", fmt.Errorf("YouTube watch page: %w", err)
	}
	if m := reVisitorData.FindSubmatch(page); m != nil {
		return string(m[1]), nil
	}
	return "", nil
}

func (y YouTube) player(ctx context.Context, key, visitor string) (*playerResponse, error) {
	client := map[string]any{
		"clientName": ytClientName, "clientVersion": ytClientVersion, "deviceMake": ytDeviceMake,
		"deviceModel": ytDeviceModel, "userAgent": ytClientUA, "osName": ytOSName, "osVersion": ytOSVersion,
		"hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0,
	}
	if visitor != "" {
		client["visitorData"] = visitor
	}
	body, err := json.Marshal(map[string]any{"context": map[string]any{"client": client}, "videoId": key,
		"contentCheckOk": true, "racyCheckOk": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.base()+"/youtubei/v1/player?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-YouTube-Client-Name", ytClientID)
	req.Header.Set("X-YouTube-Client-Version", ytClientVersion)
	req.Header.Set("Origin", youtubeOrigin)
	req.Header.Set("User-Agent", ytClientUA)
	req.Header.Set("Cookie", "SOCS=CAI; PREF=hl=en&tz=UTC")
	if visitor != "" {
		req.Header.Set("X-Goog-Visitor-Id", visitor)
	}
	data, err := y.fetch(req)
	if err != nil {
		return nil, fmt.Errorf("YouTube player: %w", err)
	}
	var pr playerResponse
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("YouTube player: %w", err)
	}
	return &pr, nil
}

func (y YouTube) fetch(req *http.Request) ([]byte, error) {
	return fetch(y.client(), req)
}

// fetch does req and reads its answer, which must be a 200.
func fetch(c *http.Client, req *http.Request) ([]byte, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return data, &StatusError{Code: resp.StatusCode, Body: data}
	}
	return data, nil
}

// StatusError is an answer that is not a 200.
type StatusError struct {
	Code int
	Body []byte
}

func (e *StatusError) Error() string { return "HTTP " + strconv.Itoa(e.Code) }

// playerResponse is what Resolve reads of /youtubei/v1/player.
type playerResponse struct {
	PlayabilityStatus struct {
		Status      string `json:"status"`
		Reason      string `json:"reason"`
		ErrorScreen struct {
			YpcTrailer struct {
				TrailerVideoID string `json:"trailerVideoId"`
			} `json:"playerLegacyDesktopYpcTrailerRenderer"`
		} `json:"errorScreen"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		VideoID string `json:"videoId"`
	} `json:"videoDetails"`
	StreamingData struct {
		Formats         []ytFormat `json:"formats"`
		AdaptiveFormats []ytFormat `json:"adaptiveFormats"`
	} `json:"streamingData"`
	AdPlacements []struct {
		Renderer struct {
			Config struct {
				Config struct {
					Kind string `json:"kind"`
				} `json:"adPlacementConfig"`
			} `json:"config"`
			Renderer struct {
				Ad *struct {
					SkipOffsetMs float64 `json:"skipOffsetMilliseconds"`
					PlayerVars   string  `json:"playerVars"`
				} `json:"instreamVideoAdRenderer"`
			} `json:"renderer"`
		} `json:"adPlacementRenderer"`
	} `json:"adPlacements"`
}

type ytFormat struct {
	Itag          int      `json:"itag"`
	URL           string   `json:"url"`
	MimeType      string   `json:"mimeType"`
	Bitrate       int64    `json:"bitrate"`
	Height        int      `json:"height"`
	ContentLength string   `json:"contentLength"`
	DRMFamilies   []string `json:"drmFamilies"`
}

// startAdWait is how long the ads before the video last, as yt-dlp counts them
// (_get_available_at_timestamp): an ad that can be skipped counts until it can be.
func (pr *playerResponse) startAdWait() time.Duration {
	var sec float64
	for _, p := range pr.AdPlacements {
		ad := p.Renderer.Renderer.Ad
		if p.Renderer.Config.Config.Kind != "AD_PLACEMENT_KIND_START" || ad == nil {
			continue
		}
		if ad.SkipOffsetMs > 0 {
			sec += ad.SkipOffsetMs / 1000
			continue
		}
		vars, _ := url.ParseQuery(ad.PlayerVars)
		if n, err := strconv.Atoi(vars.Get("length_seconds")); err == nil {
			sec += float64(n)
		}
	}
	return time.Duration(sec * float64(time.Second))
}
