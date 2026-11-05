-- A person who was merged into another (a person of TheTVDB whom TMDB knows too): the old id
-- still answers, with the new one, to a page or a bookmark that has it. When the person it was
-- merged into goes (the library no longer has them), so does the alias.
CREATE TABLE people_alias (
    old_id INTEGER PRIMARY KEY,
    new_id INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE
);
CREATE INDEX people_alias_new ON people_alias (new_id);

-- SQLite hands out the highest id again after it is deleted: a new person with an id that was
-- an alias is that person, not the one that was merged.
CREATE TRIGGER people_alias_reused AFTER INSERT ON people
BEGIN
    DELETE FROM people_alias WHERE old_id = NEW.id;
END;
