package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ply/internal/library"
	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/subs"
)

func extKey(n int) string { return "ext" + strconv.Itoa(n) }

func (s *Server) subtitleURL(itemID int64, key string) string {
	return s.withToken(fmt.Sprintf("/api/items/%d/subtitles/%s.vtt", itemID, key))
}

// canonicalIndex parses a non-negative decimal without sign or leading zeros,
// so every track has exactly one URL.
func canonicalIndex(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && strconv.Itoa(n) == s
}

// subtitleSource resolves a key to something the converter can read. Image
// tracks, unknown streams and unknown sidecars have none.
func subtitleSource(e store.Entry, key string) (subs.Source, bool) {
	if rest, ok := strings.CutPrefix(key, "ext"); ok {
		n, ok := canonicalIndex(rest)
		if !ok || n >= len(e.External) {
			return subs.Source{}, false
		}
		fi, err := os.Stat(e.External[n].Path)
		if err != nil || !fi.Mode().IsRegular() {
			return subs.Source{}, false
		}
		return subs.Source{ItemID: e.ID, Key: key, Input: e.External[n].Path, Stream: -1, MTime: fi.ModTime().UnixNano()}, true
	}
	idx, ok := canonicalIndex(key)
	if !ok {
		return subs.Source{}, false
	}
	for _, st := range e.Streams {
		if st.Index == idx {
			return embeddedSource(e, st)
		}
	}
	return subs.Source{}, false
}

// embeddedSource is the converter's view of the text subtitle stream st of e.
func embeddedSource(e store.Entry, st probe.Stream) (subs.Source, bool) {
	if st.Type != "subtitle" || !subs.IsText(st.Codec) || playback.IsImageSubtitle(st.Codec) {
		return subs.Source{}, false
	}
	return subs.Source{ItemID: e.ID, Key: strconv.Itoa(st.Index), Input: e.Path, Stream: st.Index, MTime: e.MTime}, true
}

// EmbeddedSubtitles lists the text subtitle streams of e: what the converter can
// make of its container. It reads nothing from disk.
func EmbeddedSubtitles(e store.Entry) []subs.Source {
	var out []subs.Source
	for _, st := range e.Streams {
		if src, ok := embeddedSource(e, st); ok {
			out = append(out, src)
		}
	}
	return out
}

// SubtitleSources lists every subtitle of e the converter can make: its sidecar
// files that are there, then its text streams. (Sidecars cost a stat each.)
func SubtitleSources(e store.Entry) []subs.Source {
	var out []subs.Source
	for n := range e.External {
		if src, ok := subtitleSource(e, extKey(n)); ok {
			out = append(out, src)
		}
	}
	return append(out, EmbeddedSubtitles(e)...)
}

// handleSubtitle serves one text subtitle track as WebVTT. A bitmap track
// (PGS, VobSub, DVB) has no text: it can only be burned in, so it is a 404 here.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	key, isVTT := strings.CutSuffix(r.PathValue("file"), ".vtt")
	if !isVTT || s.Subs == nil {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return
	}
	src, ok := subtitleSource(e, key)
	if !ok {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return
	}
	var path string
	var err error
	switch {
	case src.Stream >= 0 && r.Method != http.MethodGet && !s.Subs.Cached(src):
		// A HEAD asks about the track, it does not start the read of a whole film for it.
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	case src.Stream < 0 || r.Method != http.MethodGet:
		// A sidecar is a small file; a HEAD of a track that is cached is the file.
		path, err = s.Subs.Path(r.Context(), src)
	default:
		// An embedded track means reading the container end to end, which over a slow share
		// takes many minutes: the WebVTT goes out as ffmpeg writes it.
		fo, ferr := s.Subs.Follow(r.Context(), src, EmbeddedSubtitles(e))
		if ferr != nil {
			err = ferr
			break
		}
		defer fo.Close()
		grace := time.NewTimer(s.subsGrace)
		defer grace.Stop()
		select {
		case <-fo.Done(): // cached, or quick, or failed at once: answered as any file is
			path, err = fo.Result()
		case <-grace.C:
			s.streamSubtitle(w, r, e.ID, key, fo)
			return
		case <-r.Context().Done():
			return
		}
	}
	switch {
	case r.Context().Err() != nil:
		return // the client went away
	case errors.Is(err, subs.ErrConvert):
		s.Logger.Warn("subtitle conversion failed", "item", e.ID, "key", key, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "subtitle could not be converted")
		return
	case errors.Is(err, subs.ErrUnavailable):
		// The file cannot be read now (a share that is not mounted yet or asleep): not the
		// subtitle's fault, and not for good.
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "the file is not available right now")
		return
	case errors.Is(err, context.Canceled), errors.Is(err, subs.ErrClosed):
		// Not the client (it is here): the extraction this request waited for was stopped by
		// someone else (a job cancelled in Activity) or by ply stopping.
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "the subtitle extraction was stopped; try again")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// streamSubtitle answers while the track is still being extracted: 200 at once, then
// the cues as they are written, each flushed, until the file is complete. The browser
// parses a <track> as it arrives, so the first minutes of a film show their subtitles
// long before the last has been read. The answer is not cached (it may be cut
// short), and does not honour Range.
//
// A failure after the first byte cannot change the status: the cues so far stay,
// the answer ends, and the next request meets the remembered failure (422).
func (s *Server) streamSubtitle(w http.ResponseWriter, r *http.Request, id int64, key string, fo io.Reader) {
	h := w.Header()
	h.Set("Content-Type", "text/vtt; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // a proxy in front must not hold the cues back
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	buf := make([]byte, 32<<10)
	for {
		n, err := fo.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // the client went away; the extraction goes on
			}
			_ = rc.Flush()
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF), r.Context().Err() != nil:
			return
		default:
			s.Logger.Warn("subtitle conversion failed while it was served", "item", id, "key", key, "err", err)
			return
		}
	}
}

// SubtitleSearchResult represents a candidate subtitle from an online provider.
type SubtitleSearchResult struct {
	ID              string  `json:"id"`
	FileID          int     `json:"fileId"`
	FileName        string  `json:"fileName"`
	Language        string  `json:"language"`
	Format          string  `json:"format"`
	DownloadCount   int     `json:"downloadCount"`
	HearingImpaired bool    `json:"hearingImpaired"`
	Release         string  `json:"release"`
	Ratings         float64 `json:"ratings"`
}

// SubtitleSearchResponse is the reply for GET /api/items/{id}/subtitles/search.
type SubtitleSearchResponse struct {
	Configured bool                   `json:"configured"`
	Message    string                 `json:"message,omitempty"`
	Results    []SubtitleSearchResult `json:"results"`
}

// handleDeleteSubtitle is DELETE /api/items/{id}/subtitles/{key}.
// It deletes an external sidecar subtitle from disk and updates the item's subtitles.
func (s *Server) handleDeleteSubtitle(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	key := strings.TrimSuffix(r.PathValue("key"), ".vtt")
	rest, ok := strings.CutPrefix(key, "ext")
	if !ok {
		writeError(w, http.StatusBadRequest, "cannot delete embedded subtitle stream")
		return
	}
	n, ok := canonicalIndex(rest)
	if !ok || n >= len(e.External) {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return
	}
	sub := e.External[n]
	if err := os.Remove(sub.Path); err != nil && !os.IsNotExist(err) {
		s.Logger.Warn("remove subtitle file", "path", sub.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete subtitle file")
		return
	}
	newSubs, err := library.ScanSidecars(e.Path)
	if err != nil {
		s.Logger.Warn("scan sidecars after delete", "path", e.Path, "err", err)
	}
	if err := s.Store.SetExternalSubtitles(r.Context(), e.ID, newSubs); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAddSubtitle is POST /api/items/{id}/subtitles.
// It accepts a multipart file upload (.srt, .vtt, .ass), writes it as a sidecar,
// rescans sidecars, and returns the updated subtitles.
func (s *Server) handleAddSubtitle(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form data")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing subtitle file")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".srt" && ext != ".vtt" && ext != ".ass" {
		writeError(w, http.StatusBadRequest, "unsupported subtitle format (must be .srt, .vtt, or .ass)")
		return
	}

	lang := strings.TrimSpace(strings.ToLower(r.FormValue("language")))
	forced := r.FormValue("forced") == "true" || r.FormValue("forced") == "1"

	dir := filepath.Dir(e.Path)
	base := strings.TrimSuffix(filepath.Base(e.Path), filepath.Ext(e.Path))

	var subName string
	switch {
	case lang != "" && forced:
		subName = fmt.Sprintf("%s.%s.forced%s", base, lang, ext)
	case lang != "":
		subName = fmt.Sprintf("%s.%s%s", base, lang, ext)
	case forced:
		subName = fmt.Sprintf("%s.forced%s", base, ext)
	default:
		subName = fmt.Sprintf("%s%s", base, ext)
	}

	destPath := filepath.Join(dir, subName)
	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		s.internalError(w, r, err)
		return
	}

	newSubs, err := library.ScanSidecars(e.Path)
	if err != nil {
		s.Logger.Warn("scan sidecars after add", "path", e.Path, "err", err)
	}
	if err := s.Store.SetExternalSubtitles(r.Context(), e.ID, newSubs); err != nil {
		s.internalError(w, r, err)
		return
	}

	updated, err := s.Store.GetEntry(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.subtitlesOf(updated))
}

// handleSearchSubtitles is GET /api/items/{id}/subtitles/search.
// It searches OpenSubtitles for subtitles matching the item.
func (s *Server) handleSearchSubtitles(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	key, ok, _ := s.Store.GetSetting(r.Context(), "opensubtitles_key")
	if !ok || strings.TrimSpace(key) == "" {
		writeJSON(w, http.StatusOK, SubtitleSearchResponse{
			Configured: false,
			Message:    "OpenSubtitles API key is not configured in Settings > API Keys.",
			Results:    []SubtitleSearchResult{},
		})
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		if e.Kind == store.KindEpisode && e.Show != "" {
			query = fmt.Sprintf("%s S%02dE%02d", e.Show, e.Season, e.Episode)
		} else {
			query = e.Title
		}
	}

	lang := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("lang")))
	if lang == "" {
		lang = "en"
	}

	reqURL, err := url.Parse("https://api.opensubtitles.com/api/v1/subtitles")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	q := reqURL.Query()
	q.Set("query", query)
	if lang != "all" {
		q.Set("languages", lang)
	}
	if e.Kind == store.KindEpisode && e.Season > 0 && e.Episode > 0 {
		q.Set("season_number", strconv.Itoa(e.Season))
		q.Set("episode_number", strconv.Itoa(e.Episode))
	} else if e.Year > 0 {
		q.Set("year", strconv.Itoa(e.Year))
	}
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, reqURL.String(), nil)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	req.Header.Set("Api-Key", strings.TrimSpace(key))
	req.Header.Set("User-Agent", "ply v1.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.Logger.Warn("opensubtitles search failed", "err", err)
		writeJSON(w, http.StatusOK, SubtitleSearchResponse{
			Configured: true,
			Message:    "Failed to reach OpenSubtitles: " + err.Error(),
			Results:    []SubtitleSearchResult{},
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.Logger.Warn("opensubtitles search bad status", "status", resp.StatusCode)
		writeJSON(w, http.StatusOK, SubtitleSearchResponse{
			Configured: true,
			Message:    fmt.Sprintf("OpenSubtitles answered %d", resp.StatusCode),
			Results:    []SubtitleSearchResult{},
		})
		return
	}

	var openResp struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Language        string  `json:"language"`
				DownloadCount   int     `json:"download_count"`
				HearingImpaired bool    `json:"hearing_impaired"`
				Release         string  `json:"release"`
				Ratings         float64 `json:"ratings"`
				Files           []struct {
					FileID   int    `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openResp); err != nil {
		s.internalError(w, r, err)
		return
	}

	results := make([]SubtitleSearchResult, 0, len(openResp.Data))
	for _, item := range openResp.Data {
		fileID := 0
		fileName := ""
		format := "srt"
		if len(item.Attributes.Files) > 0 {
			fileID = item.Attributes.Files[0].FileID
			fileName = item.Attributes.Files[0].FileName
			if fExt := filepath.Ext(fileName); fExt != "" {
				format = strings.TrimPrefix(strings.ToLower(fExt), ".")
			}
		}
		results = append(results, SubtitleSearchResult{
			ID:              item.ID,
			FileID:          fileID,
			FileName:        fileName,
			Language:        item.Attributes.Language,
			Format:          format,
			DownloadCount:   item.Attributes.DownloadCount,
			HearingImpaired: item.Attributes.HearingImpaired,
			Release:         item.Attributes.Release,
			Ratings:         item.Attributes.Ratings,
		})
	}

	writeJSON(w, http.StatusOK, SubtitleSearchResponse{
		Configured: true,
		Results:    results,
	})
}

type downloadSubtitleRequest struct {
	FileID   int    `json:"fileId"`
	FileName string `json:"fileName"`
	Language string `json:"language"`
	Forced   bool   `json:"forced"`
	URL      string `json:"url,omitempty"`
}

// handleDownloadSubtitle is POST /api/items/{id}/subtitles/download.
// It downloads a subtitle from OpenSubtitles or a URL and attaches it as a sidecar.
func (s *Server) handleDownloadSubtitle(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var req downloadSubtitleRequest
	if !decodeBody(w, r, &req) {
		return
	}

	targetURL := req.URL
	if targetURL == "" && req.FileID != 0 {
		key, ok, _ := s.Store.GetSetting(r.Context(), "opensubtitles_key")
		if !ok || strings.TrimSpace(key) == "" {
			writeError(w, http.StatusBadRequest, "OpenSubtitles API key is not configured")
			return
		}
		bodyBytes, _ := json.Marshal(map[string]int{"file_id": req.FileID})
		dReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://api.opensubtitles.com/api/v1/download", bytes.NewReader(bodyBytes))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		dReq.Header.Set("Api-Key", strings.TrimSpace(key))
		dReq.Header.Set("User-Agent", "ply v1.0")
		dReq.Header.Set("Content-Type", "application/json")
		dReq.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(dReq)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to contact OpenSubtitles: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("OpenSubtitles download answered %d", resp.StatusCode))
			return
		}
		var dlResp struct {
			Link string `json:"link"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&dlResp); err != nil {
			s.internalError(w, r, err)
			return
		}
		targetURL = dlResp.Link
	}

	if targetURL == "" {
		writeError(w, http.StatusBadRequest, "no download URL or fileId provided")
		return
	}

	fileReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	fileReq.Header.Set("User-Agent", "ply v1.0")
	fClient := &http.Client{Timeout: 30 * time.Second}
	fResp, err := fClient.Do(fileReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to download subtitle file: "+err.Error())
		return
	}
	defer fResp.Body.Close()

	if fResp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("subtitle download answered %d", fResp.StatusCode))
		return
	}

	rawBytes, err := io.ReadAll(io.LimitReader(fResp.Body, 10<<20))
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	if len(rawBytes) > 2 && rawBytes[0] == 0x1f && rawBytes[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(rawBytes))
		if err == nil {
			decompressed, err := io.ReadAll(gz)
			_ = gz.Close()
			if err == nil {
				rawBytes = decompressed
			}
		}
	}

	ext := strings.ToLower(filepath.Ext(req.FileName))
	if ext != ".srt" && ext != ".vtt" && ext != ".ass" {
		ext = ".srt"
	}

	lang := strings.TrimSpace(strings.ToLower(req.Language))
	dir := filepath.Dir(e.Path)
	base := strings.TrimSuffix(filepath.Base(e.Path), filepath.Ext(e.Path))

	var subName string
	switch {
	case lang != "" && req.Forced:
		subName = fmt.Sprintf("%s.%s.forced%s", base, lang, ext)
	case lang != "":
		subName = fmt.Sprintf("%s.%s%s", base, lang, ext)
	case req.Forced:
		subName = fmt.Sprintf("%s.forced%s", base, ext)
	default:
		subName = fmt.Sprintf("%s%s", base, ext)
	}

	destPath := filepath.Join(dir, subName)
	if err := os.WriteFile(destPath, rawBytes, 0644); err != nil {
		s.internalError(w, r, err)
		return
	}

	newSubs, err := library.ScanSidecars(e.Path)
	if err != nil {
		s.Logger.Warn("scan sidecars after download", "path", e.Path, "err", err)
	}
	if err := s.Store.SetExternalSubtitles(r.Context(), e.ID, newSubs); err != nil {
		s.internalError(w, r, err)
		return
	}

	updated, err := s.Store.GetEntry(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.subtitlesOf(updated))
}
