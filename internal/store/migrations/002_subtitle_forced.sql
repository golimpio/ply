-- M5: sidecar subtitles can be forced ("Movie.en.forced.srt").
ALTER TABLE subtitles_external ADD COLUMN forced INTEGER NOT NULL DEFAULT 0;
