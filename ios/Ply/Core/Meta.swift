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
    let tmdbId: Int64?
    let tvdbId: Int64?
    /// Only titles in the library, newest first.
    let credits: [PersonCredit]
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
