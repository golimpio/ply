import Foundation
import Testing

// The web's own fixtures, decoded here: a search must rank a list the same way in both clients
// (web/src/lib/fuzzy.test.ts).

private func item(
    _ id: Int64, _ title: String, show: String? = nil, episodeTitle: String? = nil,
    year: Int? = nil, addedAt: Int64? = nil
) throws -> Item {
    var json: [String: Any] = ["id": id, "kind": show == nil ? "movie" : "episode", "title": title, "durationSec": 1]
    if let show { json["show"] = show }
    if let episodeTitle { json["episodeTitle"] = episodeTitle }
    if let year { json["year"] = year }
    if let addedAt { json["addedAt"] = addedAt }
    return try APIClient.decode(JSONSerialization.data(withJSONObject: json))
}

private func show(_ name: String, year: Int? = nil) throws -> ShowSummary {
    var json: [String: Any] = ["show": name, "episodeCount": 1]
    if let year { json["year"] = year }
    return try APIClient.decode(JSONSerialization.data(withJSONObject: json))
}

private func person(_ name: String, knownFor: String? = nil) throws -> PersonCard {
    var json: [String: Any] = ["id": 1, "name": name]
    if let knownFor { json["knownFor"] = knownFor }
    return try APIClient.decode(JSONSerialization.data(withJSONObject: json))
}

private func genre(_ name: String) throws -> GenreCount {
    try APIClient.decode(JSONSerialization.data(withJSONObject: ["id": 1, "name": name, "count": 1]))
}

private func collection(_ name: String) throws -> CollectionCard {
    try APIClient.decode(JSONSerialization.data(withJSONObject: ["id": 1, "name": name, "count": 1]))
}

private func titles(_ rows: [Item]) -> [String] { rows.map(\.title) }
private func ids(_ rows: [Item]) -> [Int64] { rows.map(\.id) }

@Suite struct FuzzyTests {
    @Test func foldStripsAccentsAndLowerCase() {
        #expect(Fuzzy.fold("Pokémon Ça Ñandú") == "pokemon ca nandu")
        #expect(Fuzzy.fold("Amélie") == "amelie")
        #expect(Fuzzy.fold("POKEMON") == "pokemon")
        // The web folds only a canonical decomposition's marks away: ICU's diacritic folding would
        // also give "strasse oresund" here, matching where the web finds nothing.
        #expect(Fuzzy.fold("Straße Øresund") == "straße øresund")
    }

    /// The web's arithmetic, written out: an exact piece scores 1000 - where it starts, +200 at a
    /// word's start; a scattered match scores 1 a character, +2 for a character that follows the one
    /// before it, +5 at a word's start. Without the +200, the +2 or the +5 these fail.
    @Test func theScoreRewardsAWordStartAndARunOfCharacters() {
        #expect(Fuzzy.score(Array("abc"), in: Array("abcx")) == 1200, "exact, at the start")
        #expect(Fuzzy.score(Array("abc"), in: Array("xabc")) == 999, "exact, at 1, inside a word")
        #expect(Fuzzy.score(Array("abc"), in: Array("x abc")) == 1198, "exact, at 2, at a word start")
        #expect(Fuzzy.score(Array("abc"), in: Array("xabxc")) == 5, "b follows a: that run is worth 2 more")
        #expect(Fuzzy.score(Array("abc"), in: Array("a b c")) == 20, "each character at a word start; a at 0 counts in a run")
        #expect(Fuzzy.score(Array("abc"), in: Array("xyz")) == 0, "not a match at all")
    }

    @Test func aWordStartWinsAtSearchLevelToo() throws {
        // Without the exact match's +200 this reads Carrie, The Arrival: 999 against 996.
        let rows = [try item(1, "Carrie"), try item(2, "The Arrival")]
        #expect(titles(Fuzzy.search(rows, query: "ar")) == ["The Arrival", "Carrie"])
    }

    @Test func anEmptyOrBlankQueryMatchesNothing() throws {
        let library = [try item(1, "Blade Runner"), try item(2, "Arrival")]
        #expect(Fuzzy.search(library, query: "").isEmpty)
        #expect(Fuzzy.search(library, query: "   ").isEmpty)
    }

    @Test func aWordStartBeatsTheMiddleOfAWordAndBothBeatAScatteredMatch() throws {
        let library = [
            try item(1, "Blade Runner"), try item(2, "Carrie"), try item(3, "Arrival"), try item(4, "Paterson"),
        ]
        let found = titles(Fuzzy.search(library, query: "ar"))
        #expect(found == ["Arrival", "Carrie", "Blade Runner", "Paterson"], "\(found)")
    }

    @Test func everyCharacterMustAppearInOrder() throws {
        let library = [
            try item(1, "Blade Runner"), try item(2, "Carrie"), try item(3, "Arrival"), try item(4, "Paterson"),
        ]
        #expect(Fuzzy.search(library, query: "xyz").isEmpty)
        #expect(Fuzzy.search(library, query: "lvr").isEmpty, "l..v..r is not in order in Arrival")
    }

    @Test func accentsAndCaseAreIgnoredInBothDirections() throws {
        let accented = [try item(1, "Amélie"), try item(2, "Pokémon")]
        #expect(titles(Fuzzy.search(accented, query: "amelie")) == ["Amélie"])
        #expect(titles(Fuzzy.search(accented, query: "POKEMON")) == ["Pokémon"])
        #expect(titles(Fuzzy.search([try item(3, "Amelie")], query: "Amélie")) == ["Amelie"])
    }

    @Test func itAlsoMatchesTheShowTheEpisodeTitleAndTheYear() throws {
        let episode = try item(1, "S01E01", show: "Night Shift", episodeTitle: "Pilot")
        let film = try item(2, "Paterson", year: 2016)
        #expect(ids(Fuzzy.search([episode, film], query: "pilot")) == [1])
        #expect(ids(Fuzzy.search([episode, film], query: "night")) == [1])
        #expect(ids(Fuzzy.search([episode, film], query: "2016")) == [2])
    }

    @Test func theLimitIsRespected() throws {
        let many = try (0..<50).map { try item(Int64($0), "Movie \($0)") }
        #expect(Fuzzy.search(many, query: "movie", limit: 5).count == 5)
    }

    @Test func tiesKeepTheListsOwnOrder() throws {
        // Four rows scoring the same: the web's sort is stable, so they keep the list's order.
        let rows = try (1...4).map { try item(Int64($0), "Heat") }
        #expect(ids(Fuzzy.search(rows, query: "heat")) == [1, 2, 3, 4])
        #expect(ids(Fuzzy.Prepared(rows).search("heat")) == [1, 2, 3, 4])
    }

    @Test func aPreparedListScoresAsTheListItselfDoes() throws {
        let library = [
            try item(1, "Blade Runner"), try item(2, "Carrie"), try item(3, "Arrival"), try item(4, "Paterson"),
            try item(5, "Amélie"), try item(6, "Pokémon", year: 2019),
        ]
        let prepared = Fuzzy.Prepared(library)
        for query in ["ar", "amelie", "xyz", "lvr", "pat", "POKEMON", "2019"] {
            #expect(ids(prepared.search(query)) == ids(Fuzzy.search(library, query: query)), "\(query)")
        }
    }

    @Test func aPreparedListIsMadeAgainToSeeNewRows() throws {
        let old = try item(1, "Heat")
        #expect(Fuzzy.Prepared([old]).search("matrix").isEmpty)
        let replacement = try item(1, "The Matrix")
        #expect(Fuzzy.Prepared([replacement]).search("matrix").count == 1)
    }

    @Test func anEmptyListMatchesNothing() {
        #expect(Fuzzy.search([Item](), query: "heat").isEmpty)
        #expect(Fuzzy.Prepared([Item]()).search("heat").isEmpty)
    }

    @Test func eachKindOfRowSaysWhatAPersonMayType() throws {
        let episode = try item(1, "Pilot", show: "Night Shift", episodeTitle: "Pilot", year: 2016)
        #expect(episode.fuzzyText == "Pilot Night Shift Pilot 2016")
        #expect(try show("Lost", year: 2004).fuzzyText == "Lost 2004")
        #expect(try person("Keanu Reeves", knownFor: "The Matrix").fuzzyText == "Keanu Reeves The Matrix")
        #expect(try genre("Drama").fuzzyText == "Drama")
        #expect(try collection("The Mann Collection").fuzzyText == "The Mann Collection")
    }

    @Test func aShowSearchesByItsNameAndYear() throws {
        let shows = [try show("Lost", year: 2004), try show("Dark", year: 2017)]
        #expect(Fuzzy.search(shows, query: "lost").map(\.show) == ["Lost"])
        #expect(Fuzzy.search(shows, query: "2017").map(\.show) == ["Dark"])
    }
}

@Suite struct ShowOrderTests {
    @Test func aShowIsAsNewAsItsNewestEpisode() throws {
        let episodes = [
            try item(1, "S01E01", show: "Lost", addedAt: 10),
            try item(2, "S01E02", show: "Lost", addedAt: 30),
            try item(3, "S01E03", show: "Lost", addedAt: 20),
            try item(4, "S02E01", show: "Dark", addedAt: 7),
        ]
        let latest = ShowOrder.latestAddedAt(episodes)
        #expect(latest == ["Lost": 30, "Dark": 7])
    }

    @Test func anEpisodeWithoutAFileTimeLeavesTheShowOut() throws {
        let episodes = [try item(1, "S01E01", show: "Lost"), try item(2, "S01E02", show: "Dark", addedAt: 5)]
        #expect(ShowOrder.latestAddedAt(episodes) == ["Dark": 5])
    }

    @Test func aFilmIsNotAShow() throws {
        #expect(ShowOrder.latestAddedAt([try item(1, "Heat", addedAt: 99)]).isEmpty)
    }
}
