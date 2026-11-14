import Foundation

// The shapes of ply's HTTP API (internal/api, docs/M11-CLIENTS.md). Fields the server leaves out
// (omitempty) are optional; Go's nil slices arrive as null and are read as [].

/// The API version this app is built against (docs/M11-CLIENTS.md, "Versioning").
nonisolated let clientAPIVersion = 1

nonisolated struct ServerInfo: Decodable, Sendable, Equatable {
    let id: String
    let name: String
    let version: String
    let authRequired: Bool
    /// 0 from a server older than M11, which says no version: it is one to update.
    let api: Int
    let apiMin: Int

    private enum CodingKeys: String, CodingKey {
        case id, name, version, authRequired, api, apiMin
    }

    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        version = try c.decode(String.self, forKey: .version)
        authRequired = try c.decode(Bool.self, forKey: .authRequired)
        api = try c.decodeIfPresent(Int.self, forKey: .api) ?? 0
        apiMin = try c.decodeIfPresent(Int.self, forKey: .apiMin) ?? 0
    }
}

/// Whether this app and a server can talk: a client built for version N works with a server whose
/// apiMin ≤ N ≤ api. Otherwise it says which of the two to update.
nonisolated enum Compatibility: Equatable {
    case compatible
    case updateApp
    case updateServer

    init(api: Int, apiMin: Int, client: Int = clientAPIVersion) {
        if client < apiMin {
            self = .updateApp
        } else if client > api {
            self = .updateServer
        } else {
            self = .compatible
        }
    }

    var problem: String? {
        switch self {
        case .compatible: nil
        case .updateApp: "This server is newer than the app: update the ply app."
        case .updateServer: "This server is older than the app: update ply on the server."
        }
    }
}

/// A server that answers but cannot be talked to because its API version and this app's do not
/// overlap: the message says which of the two to update.
nonisolated struct CompatibilityProblem: LocalizedError, Sendable {
    let problem: String
    var errorDescription: String? { problem }
}

nonisolated struct Progress: Decodable, Sendable, Hashable {
    let positionSec: Double
    let watched: Bool
}

nonisolated struct Art: Decodable, Sendable, Hashable {
    let backdrop: String?
    let thumb: String?
}

/// A rating as lists carry it (M10): value on the source's own scale.
nonisolated struct Rating: Decodable, Sendable, Hashable {
    let source: String
    let value: Double
    let scale: Double

    var label: String {
        let name = switch source {
        case "imdb": "IMDb"
        case "tmdb": "TMDB"
        case "rt_critics": "RT"
        case "rt_audience": "RT audience"
        case "metacritic": "Metacritic"
        case "letterboxd": "Letterboxd"
        case "trakt": "Trakt"
        default: source
        }
        let v = value.rounded() == value ? String(Int(value)) : String(format: "%.1f", value)
        return "\(name) \(v)"
    }
}

/// One file of the library: a movie or an episode, as GET /api/items (full rows or
/// fields=card) and GET /api/continue list it.
nonisolated struct Item: Decodable, Sendable, Hashable, Identifiable {
    let id: Int64
    let kind: String
    let title: String
    let year: Int?
    let show: String?
    let season: Int?
    let episode: Int?
    let episodeTitle: String?
    let durationSec: Double
    let addedAt: Int64?
    let titleId: Int64?
    let sortTitle: String?
    let poster: String?
    let backdrop: String?
    let still: String?
    let overview: String?
    let art: Art?
    let rating: Rating?
    let progress: Progress?
    let watched: Bool?
    let genres: [String]?

    var isEpisode: Bool { kind == "episode" }
    var isWatched: Bool { watched ?? progress?.watched ?? false }

    /// Where a play resumes: the position of a file started and not watched to the end.
    var resumeSec: Double {
        guard let p = progress, !p.watched else { return 0 }
        return p.positionSec
    }

    /// How far through it is, 0 to 1, for a started file.
    var fraction: Double? {
        guard resumeSec > 0, durationSec > 0 else { return nil }
        return min(1, resumeSec / durationSec)
    }

    /// "S1 · E2" for an episode.
    var episodeCode: String? {
        guard let season, let episode else { return nil }
        return "S\(season) · E\(episode)"
    }

    /// The picture of a landscape card: an episode's still, a movie's backdrop, else a frame grab.
    var landscapeImage: String? {
        isEpisode ? still ?? art?.thumb ?? backdrop : backdrop ?? art?.backdrop ?? art?.thumb
    }
}

nonisolated struct MediaStream: Decodable, Sendable, Hashable {
    let index: Int
    let type: String
    let codec: String
    let language: String?
    let title: String?
    let channels: Int?
    let `default`: Bool?

    var label: String {
        var parts: [String] = []
        if let l = language, !l.isEmpty { parts.append(Locale.current.localizedString(forLanguageCode: l) ?? l) }
        if let t = title, !t.isEmpty { parts.append(t) } else { parts.append(codec.uppercased()) }
        if let c = channels, c > 0 { parts.append(c == 2 ? "stereo" : c == 1 ? "mono" : "\(c - 1).1") }
        return parts.joined(separator: " · ")
    }
}

nonisolated struct Subtitle: Decodable, Sendable, Hashable {
    /// The ffprobe stream index of an embedded track, or ext<N> for a sidecar file.
    let key: String
    let language: String?
    let title: String?
    let format: String
    /// A bitmap track: it can only be burned into the video (subtitleIndex).
    let image: Bool
    let forced: Bool?
    /// WebVTT, for text tracks.
    let url: String?

    var label: String {
        var parts: [String] = []
        if let l = language, !l.isEmpty { parts.append(Locale.current.localizedString(forLanguageCode: l) ?? l) }
        if let t = title, !t.isEmpty { parts.append(t) }
        if forced == true { parts.append("forced") }
        if parts.isEmpty { parts.append(format.uppercased()) }
        return parts.joined(separator: " · ")
    }
}

nonisolated struct Genre: Decodable, Sendable, Hashable {
    let id: Int64
    let name: String
}

nonisolated struct CastMember: Decodable, Sendable, Hashable {
    let personId: Int64
    let name: String
    let character: String?
    let profile: String?
}

/// A movie's title, or a show's (GET /api/items/{id} meta, GET /api/shows/{name} meta).
nonisolated struct TitleDetail: Decodable, Sendable {
    let id: Int64
    let title: String
    let year: Int?
    let overview: String?
    let tagline: String?
    let runtimeMin: Int?
    let certification: String?
    let poster: String?
    let backdrop: String?
    let rating: Rating?
    /// The collection the title belongs to, when it is in one.
    let collection: CollectionRef?
    /// Every collection it is in: the server sends this instead of `collection` for an older
    /// client, and `collection` too.
    let collections: [CollectionRef]?
    let genres: [Genre]?
    let cast: [CastMember]?
}

nonisolated struct EpisodeInfo: Decodable, Sendable {
    let name: String?
    let overview: String?
    let airDate: String?
    let still: String?
}

/// GET /api/items/{id}: the row, its streams and subtitles, and what is known of its title.
nonisolated struct ItemDetail: Decodable, Sendable {
    let item: Item
    let streams: [MediaStream]
    let subtitles: [Subtitle]
    let meta: TitleDetail?
    let episodeInfo: EpisodeInfo?

    private enum CodingKeys: String, CodingKey {
        case streams, subtitles, meta, episodeInfo
    }

    init(from decoder: any Decoder) throws {
        item = try Item(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        streams = try c.decodeIfPresent([MediaStream].self, forKey: .streams) ?? []
        subtitles = try c.decodeIfPresent([Subtitle].self, forKey: .subtitles) ?? []
        meta = try c.decodeIfPresent(TitleDetail.self, forKey: .meta)
        episodeInfo = try c.decodeIfPresent(EpisodeInfo.self, forKey: .episodeInfo)
    }

    var audioStreams: [MediaStream] { streams.filter { $0.type == "audio" } }
    var overview: String? { episodeInfo?.overview ?? item.overview ?? meta?.overview }
}

/// One entry of GET /api/shows.
nonisolated struct ShowSummary: Decodable, Sendable, Hashable, Identifiable {
    let show: String
    let episodeCount: Int
    let watchedEpisodes: Int?
    let year: Int?
    let sortTitle: String?
    let poster: String?
    let backdrop: String?

    var id: String { show }
    var allWatched: Bool { episodeCount > 0 && (watchedEpisodes ?? 0) >= episodeCount }
}

nonisolated struct SeasonInfo: Decodable, Sendable, Hashable, Identifiable {
    let season: Int
    let name: String
    let episodeCount: Int

    var id: Int { season }
}

/// GET /api/shows/{name}.
nonisolated struct ShowDetail: Decodable, Sendable {
    let show: String
    let year: Int?
    let overview: String?
    let poster: String?
    let backdrop: String?
    let rating: Rating?
    let meta: TitleDetail?
    let seasons: [SeasonInfo]?
}

/// POST /api/items/{id}/playback: how the item is delivered, and the URL to play.
nonisolated struct Playback: Decodable, Sendable {
    let method: String
    /// Server-relative; empty when there is nothing to play (the last reason says why).
    let url: String?
    let sessionId: String?
    let reasons: [String]?
}

/// What a device is asked to do (docs/M11-CLIENTS.md, "Remote control").
nonisolated struct Command: Decodable, Sendable {
    let seq: Int64
    let type: String
    let itemId: Int64?
    let positionSec: Double?
    let from: String?
    let expiresAt: Date
}

/// The payload of a command.* event.
nonisolated struct CommandEvent: Decodable, Sendable {
    let command: Command
}

/// A snapshot of the feed: the sections of the topics asked for. Only what this app follows.
nonisolated struct Snapshot: Decodable, Sendable {
    let api: Int?
    let commands: [Command]?
}
