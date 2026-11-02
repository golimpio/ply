-- M6: a series' next episode to air (TMDB next_episode_to_air), next to
-- next_air_date, for the "Upcoming" rail. 0 and '' mean unknown, as in 003.

ALTER TABLE titles ADD COLUMN next_episode_season INTEGER NOT NULL DEFAULT 0;
ALTER TABLE titles ADD COLUMN next_episode_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE titles ADD COLUMN next_episode_name   TEXT    NOT NULL DEFAULT '';
