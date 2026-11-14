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
    /// The same rating on a 0-100 scale, as the lists sort by (the server's `score100`).
    let score100: Int?

    /// The rating on a 0-100 scale: the server's, or the value on the source's own scale.
    var score: Int {
        if let score100 { return score100 }
        guard scale > 0 else { return 0 }
        return Int((value / scale * 100).rounded())
    }

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
    let container: String?
    let width: Int?
    let height: Int?
    let hdr: String?

    var isEpisode: Bool { kind == "episode" }
    var isWatched: Bool { watched ?? progress?.watched ?? false }

    var is4K: Bool { (height ?? 0) >= 2000 || (width ?? 0) >= 3800 }
    var isHDR: Bool { !(hdr ?? "").isEmpty }

    /// A short label telling versions apart: "4K HDR, MKV", "1080p, MP4".
    var versionLabel: String {
        let h = height ?? 0
        let res = h >= 2000 ? "4K" : h >= 1400 ? "1440p" : h >= 1000 ? "1080p" : h >= 700 ? "720p" : h > 0 ? "\(h)p" : ""
        let hdrText = (hdr ?? "").isEmpty ? "" : (hdr?.uppercased() ?? "HDR")
        let picture = [res, hdrText].filter { !$0.isEmpty }.joined(separator: " ")
        return [picture, container?.uppercased()].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: ", ")
    }

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

nonisolated struct CrewMember: Decodable, Sendable, Hashable {
    let personId: Int64
    let name: String
    let job: String
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
    let crew: [CrewMember]?
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

/// A trailer or teaser for a title (GET /api/titles/{id}/trailers).
nonisolated struct Trailer: Decodable, Sendable, Hashable, Identifiable {
    let site: String
    let key: String
    let name: String?
    let type: String?
    let official: Bool?

    var id: String { "\(site):\(key)" }

    var embedURL: URL? {
        if site == "youtube" {
            return URL(string: "https://www.youtube-nocookie.com/embed/\(key)?autoplay=1&rel=0&playsinline=1")
        }
        if site == "vimeo" {
            return URL(string: "https://player.vimeo.com/video/\(key)?autoplay=1&dnt=1")
        }
        return nil
    }

    var webURL: URL? {
        if site == "youtube" {
            return URL(string: "https://www.youtube.com/watch?v=\(key)")
        }
        if site == "vimeo" {
            return URL(string: "https://vimeo.com/\(key)")
        }
        return nil
    }

    func label(in all: [Trailer]) -> String {
        let cleanName = (name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !cleanName.isEmpty { return cleanName }
        let kind = type == "teaser" ? "Teaser" : "Trailer"
        let same = all.filter { ($0.name ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && ($0.type == "teaser") == (type == "teaser") }
        if same.count > 1, let idx = same.firstIndex(where: { $0.id == id }) {
            return "\(kind) \(idx + 1)"
        }
        return kind
    }
}

/// The next episode of a show that airs (GET /api/upcoming).
nonisolated struct NextEpisode: Decodable, Sendable, Hashable {
    let season: Int
    let episode: Int
    let name: String?
}

/// A show in the library with an upcoming episode (GET /api/upcoming).
nonisolated struct UpcomingShow: Decodable, Sendable, Hashable, Identifiable {
    let titleId: Int64
    let show: String
    let poster: String?
    let backdrop: String?
    let nextAirDate: String
    let nextEpisode: NextEpisode?

    var id: Int64 { titleId }
}
