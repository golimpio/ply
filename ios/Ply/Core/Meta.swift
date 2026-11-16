import Foundation

// The metadata the library's browse pages read (GET /api/titles, /api/people,
// /api/genres, /api/collections, /api/receivers). The shapes are docs/M11-CLIENTS.md's
// and internal/api's; a field the server omits (omitempty) is optional.

/// A film or a series as a tile shows it (GET /api/titles and the lists of a person,
/// a genre or a collection). `year` is 0 when unknown.
nonisolated struct TitleCard: Decodable, Sendable, Hashable, Identifiable {
    let id: Int64
    let kind: String
    let title: String
    let sortTitle: String?
    let year: Int
    let poster: String?
    let backdrop: String?
    let rating: Rating?
    /// Movies: the first file.
    let itemId: Int64?
    /// Shows: the show's name in the library.
    let show: String?
    let nextAirDate: String?
    /// A film: watched in any version; a series: in all its episodes.
    let watched: Bool?
    /// A series: how far through its episodes.
    let episodeCount: Int?
    let watchedEpisodes: Int?
    let inProgress: Int?

    var isSeries: Bool { kind == "series" }
    var yearText: String? { year > 0 ? String(year) : nil }
    /// A film watched in any version, or a series whose episodes are all watched.
    var isWatched: Bool {
        if isSeries { return (watched ?? false) && (episodeCount ?? 0) > 0 }
        return watched ?? false
    }
}

/// One entry of GET /api/people?q=.
nonisolated struct PersonCard: Decodable, Sendable, Hashable, Identifiable {
    let id: Int64
    let name: String
    let profile: String?
    let knownFor: String?
}

/// One role of a person in a title of the library.
nonisolated struct PersonCredit: Decodable, Sendable, Hashable {
    let titleId: Int64
    /// cast | crew
    let kind: String
    let job: String?
    let character: String?
    let title: TitleCard
}

/// An entry of GET /api/people/{id}/filmography: a title from TMDB/TVDB.
nonisolated struct PersonFilmographyEntry: Decodable, Sendable, Hashable, Identifiable {
    let kind: String
    let tmdbId: Int64?
    let tvdbId: Int64?
    let imdbId: String?
    let title: String
    let originalTitle: String?
    let year: Int?
    let releaseDate: String?
    let character: String?
    let job: String?
    let department: String?
    let creditKind: String?
    let poster: String?
    let backdrop: String?
    let rating: Double?
    let voteCount: Int?
    let popularity: Double?
    let episodeCount: Int?
    /// Whether the title is in the library (the server says; the id fields are external ids).
    let inLibrary: Bool?

    var id: String { "\(kind):\(tmdbId ?? 0):\(title):\(job ?? ""):\(character ?? "")" }
    var isSeries: Bool { kind == "series" }
    /// A title the library does not have, so there is no page to open.
    var isExternal: Bool { inLibrary != true }
    var yearText: String? { (year ?? 0) > 0 ? String(year!) : nil }
    var roleDescription: String? {
        let cleanChar = (character ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !cleanChar.isEmpty { return cleanChar }
        let cleanJob = (job ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !cleanJob.isEmpty { return cleanJob }
        return nil
    }
    var ratingLabel: String? {
        guard let rating, rating > 0 else { return nil }
        return String(format: "%.1f", (rating * 10).rounded() / 10)
    }
    var imdbURL: URL? { ExternalLinks.imdbTitleURL(imdbId) }
    var tmdbURL: URL? { ExternalLinks.tmdbTitleURL(kind: kind, tmdbId: tmdbId) }
}

/// GET /api/metadata/preview/{kind}/{tmdbId}: details for an off-library credit preview.
nonisolated struct TitlePreview: Decodable, Sendable, Equatable {
    let kind: String
    let tmdbId: Int64
    let imdbId: String?
    let title: String
    let originalTitle: String?
    let year: Int
    let overview: String?
    let tagline: String?
    let releaseDate: String?
    let endDate: String?
    let status: String?
    let runtimeMin: Int?
    let certification: String?
    let poster: String?
    let backdrop: String?
    let genres: [String]
    let cast: [PreviewCredit]
    let crew: [PreviewCredit]

    var creditLines: [(label: String, names: [String])] {
        let specs: [(label: String, plural: String, jobs: Set<String>)] = [
            ("Director", "Directors", ["Director"]),
            ("Creator", "Creators", ["Creator"]),
            ("Writer", "Writers", ["Screenplay", "Writer", "Story", "Novel"]),
            ("Music", "Music", ["Original Music Composer"]),
            ("Cinematography", "Cinematography", ["Director of Photography"])
        ]
        return specs.compactMap { spec in
            var seen: Set<String> = []
            var names: [String] = []
            for member in crew where member.role.map({ spec.jobs.contains($0) }) ?? false {
                if seen.insert(member.name).inserted {
                    names.append(member.name)
                }
            }
            guard !names.isEmpty else { return nil }
            return (label: names.count > 1 ? spec.plural : spec.label, names: names)
        }
    }
}

nonisolated struct PreviewCredit: Decodable, Sendable, Hashable {
    let tmdbId: Int64?
    let name: String
    let role: String?
    let profile: String?
}

/// GET /api/people/{id}.
nonisolated struct Person: Decodable, Sendable {
    let id: Int64
    let name: String
    let profile: String?
    let knownFor: String?
    let biography: String?
    let birthday: String?
    let deathday: String?
    let placeOfBirth: String?
    let imdbId: String?
    let wikidataId: String?
    let tmdbId: Int64?
    let tvdbId: Int64?
    /// Only titles in the library, newest first.
    let credits: [PersonCredit]

    var wikipediaURL: URL? { ExternalLinks.wikipediaURL(name: name, wikidataId: wikidataId) }
    var imdbURL: URL? { ExternalLinks.imdbPersonURL(imdbId) }
    var lifeSpan: String? { PersonDates.lifeSpan(birthday: birthday, deathday: deathday) }

    /// Library credits that open a detail page, split into films and series.
    var openableMediaCredits: (films: [PersonCredit], series: [PersonCredit]) {
        let openable = credits.filter { c in
            c.title.isSeries ? !(c.title.show ?? "").isEmpty : c.title.itemId != nil
        }
        return (
            films: openable.filter { !$0.title.isSeries },
            series: openable.filter { $0.title.isSeries }
        )
    }

    /// External filmography entries excluding anything already in the library.
    func externalFilmography(from entries: [PersonFilmographyEntry]) -> (films: [PersonFilmographyEntry], series: [PersonFilmographyEntry]) {
        let external = entries.filter(\.isExternal)
        return (
            films: external.filter { !$0.isSeries },
            series: external.filter { $0.isSeries }
        )
    }
}

nonisolated enum CreditGroup: String, CaseIterable, Sendable {
    case acting = "Acting"
    case directing = "Directing"
    case writing = "Writing"
    case other = "Other"
}

nonisolated struct CreditSection: Identifiable, Sendable {
    struct Entry: Identifiable, Sendable {
        let card: TitleCard
        let detail: String
        var id: Int64 { card.id }
    }

    let group: CreditGroup
    let entries: [Entry]
    var id: String { group.rawValue }
    var title: String { group.rawValue }

    static func uniqueTitleCount(_ credits: [PersonCredit]) -> Int {
        Set(credits.map(\.title.id)).count
    }

    static func sections(_ credits: [PersonCredit]) -> [CreditSection] {
        var byGroup: [CreditGroup: [Int64: (card: TitleCard, details: [String])]] = [:]
        for credit in credits {
            let group = group(of: credit)
            var titles = byGroup[group] ?? [:]
            var entry = titles[credit.title.id] ?? (card: credit.title, details: [])
            let detail = ((credit.kind == "cast" ? credit.character : credit.job) ?? "")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            if !detail.isEmpty, !entry.details.contains(detail) { entry.details.append(detail) }
            titles[credit.title.id] = entry
            byGroup[group] = titles
        }

        return CreditGroup.allCases.compactMap { group in
            guard let titles = byGroup[group] else { return nil }
            let entries = titles.values
                .map { Entry(card: $0.card, detail: $0.details.joined(separator: ", ")) }
                .sorted { max($0.card.year, 0) > max($1.card.year, 0) }
            return CreditSection(group: group, entries: entries)
        }
    }

    private static func group(of credit: PersonCredit) -> CreditGroup {
        if credit.kind == "cast" { return .acting }
        switch (credit.job ?? "").trimmingCharacters(in: .whitespaces).lowercased() {
        case "director": return .directing
        case "writer", "screenplay", "story", "novel", "creator": return .writing
        default: return .other
        }
    }
}

nonisolated enum ExternalLinks {
    static func wikipediaURL(name: String, wikidataId: String?) -> URL? {
        if let qid = wikidataId?.trimmingCharacters(in: .whitespacesAndNewlines),
           qid.range(of: #"^Q\d+$"#, options: [.regularExpression, .caseInsensitive]) != nil,
           let encoded = qid.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) {
            return URL(string: "https://www.wikidata.org/wiki/Special:GoToLinkedPage/enwiki/\(encoded)")
        }
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty,
              let encoded = cleanName.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) else {
            return nil
        }
        return URL(string: "https://en.wikipedia.org/wiki/Special:Search?search=\(encoded)")
    }

    static func imdbPersonURL(_ imdbId: String?) -> URL? {
        guard let clean = imdbId?.trimmingCharacters(in: .whitespacesAndNewlines), !clean.isEmpty,
              let encoded = clean.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) else {
            return nil
        }
        return URL(string: "https://www.imdb.com/name/\(encoded)/")
    }

    static func imdbTitleURL(_ imdbId: String?) -> URL? {
        guard let clean = imdbId?.trimmingCharacters(in: .whitespacesAndNewlines), !clean.isEmpty,
              let encoded = clean.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) else {
            return nil
        }
        return URL(string: "https://www.imdb.com/title/\(encoded)/")
    }

    static func tmdbTitleURL(kind: String, tmdbId: Int64?) -> URL? {
        guard let tmdbId, tmdbId > 0 else { return nil }
        let media = kind == "series" ? "tv" : "movie"
        return URL(string: "https://www.themoviedb.org/\(media)/\(tmdbId)")
    }
}

nonisolated enum PersonDates {
    private static let months = [
        "January", "February", "March", "April", "May", "June",
        "July", "August", "September", "October", "November", "December"
    ]

    private struct Parts {
        let year: Int
        let month: Int
        let day: Int
    }

    private static func daysIn(year: Int, month: Int) -> Int {
        if month == 2 {
            return (year % 4 == 0 && (year % 100 != 0 || year % 400 == 0)) ? 29 : 28
        }
        return [4, 6, 9, 11].contains(month) ? 30 : 31
    }

    private static func parse(_ raw: String?) -> Parts? {
        let s = (raw ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return nil }
        let datePart = s.split(whereSeparator: { $0 == "T" || $0 == " " }).first.map(String.init) ?? s
        let comps = datePart.split(separator: "-", omittingEmptySubsequences: false)
        guard (1...3).contains(comps.count),
              comps[0].count == 4,
              let year = Int(comps[0]), year >= 1 else {
            return nil
        }
        let month: Int
        if comps.count >= 2 {
            guard comps[1].count == 2, let m = Int(comps[1]), (0...12).contains(m) else { return nil }
            month = m
        } else {
            month = 0
        }
        let day: Int
        if comps.count == 3 {
            guard comps[2].count == 2, let d = Int(comps[2]) else { return nil }
            let maxDay = month > 0 ? daysIn(year: year, month: month) : 0
            guard (0...maxDay).contains(d) else { return nil }
            day = month == 0 ? 0 : d
        } else {
            day = 0
        }
        return Parts(year: year, month: month, day: day)
    }

    static func formatDate(_ raw: String?) -> String? {
        guard let d = parse(raw) else { return nil }
        guard d.month > 0 else { return String(d.year) }
        let monthName = months[d.month - 1]
        return d.day > 0 ? "\(d.day) \(monthName) \(d.year)" : "\(monthName) \(d.year)"
    }

    static func lifeSpan(birthday: String?, deathday: String?) -> String? {
        let born = parse(birthday)
        let died = parse(deathday)
        if let born, let died { return "\(born.year)–\(died.year)" }
        if died != nil, let diedFormatted = formatDate(deathday) { return "Died \(diedFormatted)" }
        if born != nil, let bornFormatted = formatDate(birthday) { return "Born \(bornFormatted)" }
        return nil
    }
}

/// The 404 of GET /api/people/{id} for a person merged into another:
/// `{"error": "merged", "into": <id to ask for>}`.
nonisolated struct MergedPerson: Decodable, Sendable {
    let into: Int64
}

/// An entry of GET /api/genres.
nonisolated struct GenreCount: Decodable, Sendable, Hashable, Identifiable {
    let id: Int64
    let name: String
    let count: Int
}

/// A collection with a title in the library (GET /api/collections).
nonisolated struct CollectionCard: Decodable, Sendable, Hashable, Identifiable {
    let id: Int64
    let name: String
    let poster: String?
    let backdrop: String?
    let count: Int
    let movieCount: Int?
    let seriesCount: Int?

    init(
        id: Int64,
        name: String,
        poster: String? = nil,
        backdrop: String? = nil,
        count: Int,
        movieCount: Int? = nil,
        seriesCount: Int? = nil
    ) {
        self.id = id
        self.name = name
        self.poster = poster
        self.backdrop = backdrop
        self.count = count
        self.movieCount = movieCount
        self.seriesCount = seriesCount
    }
}
/// GET /api/collections/{id}.
nonisolated struct CollectionDetail: Decodable, Sendable {
    let id: Int64
    let name: String
    let overview: String?
    let poster: String?
    let backdrop: String?
    /// Release order.
    let titles: [TitleCard]
}

/// The part of a title's metadata that names the collection it belongs to.
nonisolated struct CollectionRef: Decodable, Sendable, Hashable {
    let id: Int64
    let name: String
}

/// An AirPlay target the server found on the LAN (GET /api/receivers).
nonisolated struct Receiver: Decodable, Sendable, Hashable, Identifiable {
    let id: String
    let name: String
    let model: String?
    let kind: String?
    let address: String?
    let port: Int?
    /// The base URL of the ply server this target can fetch. Empty when the server is on
    /// no network of the target's, and then a play cannot name it.
    let base: String?

    var canRoute: Bool { !(base ?? "").isEmpty }
}
