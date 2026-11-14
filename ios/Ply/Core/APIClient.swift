import Foundation

/// An answer that was not a success: status is the HTTP status, 0 when the server did not answer.
nonisolated struct APIError: LocalizedError, Sendable {
    let status: Int
    let message: String
    /// The answer's body, when the server sent one (a person's 404 names the survivor there).
    let body: Data?

    init(status: Int, message: String, body: Data? = nil) {
        self.status = status
        self.message = message
        self.body = body
    }

    var errorDescription: String? { message }
}

/// The ply HTTP API, as one device (docs/M11-CLIENTS.md): every request names the device in
/// X-Ply-Device and, once paired, carries its token as Bearer. The URLs the server builds (media,
/// art, subtitles) carry their own URL tokens and are followed as they are.
final class APIClient {
    let base: URL
    let token: String?
    /// The device every request is made as: its id, its registration and its attempt number.
    let device: DeviceContext
    /// Called when the server answers 401: the device's token was revoked.
    var onUnauthorized: (() -> Void)?

    private let session: URLSession

    init(base: URL, token: String?, device: DeviceContext, session: URLSession = .shared) {
        self.base = base
        self.token = token
        self.device = device
        self.session = session
    }

    /// A URL the server gave (server-relative or absolute) as an absolute one; nil for none.
    func url(_ path: String?) -> URL? {
        guard let path, !path.isEmpty else { return nil }
        return URL(string: path, relativeTo: base)?.absoluteURL
    }

    /// A request with the device's headers, for path (server-relative, with its query).
    func urlRequest(_ method: String, _ path: String) -> URLRequest {
        var req = URLRequest(url: url(path) ?? base)
        req.httpMethod = method
        req.setValue(device.id, forHTTPHeaderField: "X-Ply-Device")
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        return req
    }

    // MARK: transport

    private func data(_ method: String, _ path: String, body: (any Encodable & Sendable)? = nil) async throws -> Data {
        var req = urlRequest(method, path)
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONEncoder().encode(body)
        }
        return try await perform(req)
    }

    private func perform(_ req: URLRequest) async throws -> Data {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: req)
        } catch let e as URLError where e.code == .cancelled {
            throw CancellationError()
        } catch {
            throw APIError(status: 0, message: "Cannot reach the ply server (\(error.localizedDescription))")
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        try check(status: status, body: data)
        return data
    }

    /// An answer read as it arrives (the feed, a subtitle track being extracted), once its status
    /// says it is one.
    func bytes(_ req: URLRequest, session: URLSession? = nil) async throws -> URLSession.AsyncBytes {
        let (bytes, response) = try await (session ?? self.session).bytes(for: req)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        if !(200..<300).contains(status) {
            var body = Data()
            for try await b in bytes.prefix(4096) { body.append(b) }
            try check(status: status, body: body)
        }
        return bytes
    }

    /// Throws the APIError of a failed answer, with the server's {"error": ...} as its message.
    private func check(status: Int, body: Data) throws {
        guard !(200..<300).contains(status) else { return }
        if status == 401 { onUnauthorized?() }
        struct Failure: Decodable { let error: String? }
        let message = (try? JSONDecoder().decode(Failure.self, from: body))?.error
        throw APIError(status: status, message: message ?? HTTPURLResponse.localizedString(forStatusCode: status), body: body)
    }

    private func get<T: Decodable>(_ path: String, as: T.Type = T.self) async throws -> T {
        try Self.decode(try await data("GET", path))
    }

    /// A list: Go's null slice is [].
    private func list<T: Decodable>(_ path: String, of: T.Type = T.self) async throws -> [T] {
        try Self.decode(try await data("GET", path)) as [T]? ?? []
    }

    /// JSON as the server writes it, its times RFC 3339.
    nonisolated static func decode<T: Decodable>(_ data: Data) throws -> T {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { d in
            let s = try d.singleValueContainer().decode(String.self)
            guard let date = RFC3339.date(s) else {
                throw DecodingError.dataCorrupted(.init(codingPath: d.codingPath, debugDescription: "not RFC 3339: \(s)"))
            }
            return date
        }
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw APIError(status: 0, message: "The server's answer could not be read (\(error))")
        }
    }

    // MARK: endpoints

    func info() async throws -> ServerInfo { try await get("/api/info") }

    /// Pairs this device with the code the owner was shown (Settings › Devices › Pair a device):
    /// registers it and answers its token, once. 403 for a wrong or expired code.
    func pair(code: String) async throws -> String {
        struct Body: Encodable { let code, id, name, kind, app, version: String }
        struct Answer: Decodable { let token: String }
        let r = device.registration
        let body = Body(code: code, id: device.id, name: r.name, kind: r.kind, app: r.app, version: r.version)
        let answer: Answer = try Self.decode(try await data("POST", "/api/pair", body: body))
        return answer.token
    }

    /// PUT /api/devices/{id}: at start, and whenever the name, kind, app or version change.
    func registerDevice() async throws {
        _ = try await data("PUT", "/api/devices/\(Path.segment(device.id))", body: device.registration)
    }

    func movies() async throws -> [Item] { try await list("/api/items?kind=movie&fields=card") }

    func episodes(show: String) async throws -> [Item] {
        try await list("/api/items" + Path.query([("kind", "episode"), ("fields", "card"), ("show", show)]))
    }

    func shows() async throws -> [ShowSummary] { try await list("/api/shows") }

    func show(_ name: String) async throws -> ShowDetail { try await get("/api/shows/\(Path.segment(name))") }

    func continueWatching() async throws -> [Item] { try await list("/api/continue") }

    func item(_ id: Int64) async throws -> ItemDetail { try await get("/api/items/\(id)") }

    /// Starts a play. Each ask carries the device's next attempt number: of two that cross,
    /// the server keeps the newer. An AirPlay play names the receiver its URLs must be on, and
    /// the `airplay` profile (MPEG-TS HLS, which an older Apple TV plays); a text subtitle is then
    /// a rendition of that HLS (`subtitleKey`), not drawn by the app.
    func playback(_ id: Int64, startSec: Double, audioIndex: Int?, subtitleIndex: Int?,
                  subtitleKey: String? = nil, receiver: String? = nil,
                  profile: String = "avplayer") async throws -> Playback {
        struct Body: Encodable {
            let profile: String
            let startSec: Double
            let audioIndex: Int?
            let subtitleIndex: Int?
            let subtitleKey: String?
            let receiver: String?
            let attempt: Int64
        }
        let body = Body(profile: profile, startSec: startSec, audioIndex: audioIndex, subtitleIndex: subtitleIndex,
                        subtitleKey: subtitleKey, receiver: receiver, attempt: device.nextAttempt())
        return try Self.decode(try await data("POST", "/api/items/\(id)/playback", body: body))
    }

    /// Keeps a play alive. APIError 404: the session is gone; 410: someone stopped it.
    func heartbeat(_ sid: String, positionSec: Double, paused: Bool, wireless: Bool) async throws {
        struct Body: Encodable { let positionSec: Double; let paused: Bool; let wireless: Bool }
        _ = try await data("POST", "/api/sessions/\(Path.segment(sid))/heartbeat",
                           body: Body(positionSec: positionSec, paused: paused, wireless: wireless))
    }

    /// Ends a play; best effort, the server also reaps idle ones.
    func deleteSession(_ sid: String) async {
        _ = try? await data("DELETE", "/api/sessions/\(Path.segment(sid))")
    }

    /// Where playback is, for the resume point.
    func putProgress(_ id: Int64, positionSec: Double, durationSec: Double) async throws {
        struct Body: Encodable { let positionSec: Double; let durationSec: Double }
        _ = try await data("PUT", "/api/items/\(id)/progress", body: Body(positionSec: positionSec, durationSec: durationSec))
    }

    /// An episode, or every version of a film.
    func setWatched(_ id: Int64, _ watched: Bool) async throws {
        struct Body: Encodable { let watched: Bool }
        _ = try await data("PUT", "/api/items/\(id)/watched", body: Body(watched: watched))
    }

    /// Every version of a film, or every episode of a series by its title id.
    func setTitleWatched(_ id: Int64, _ watched: Bool) async throws {
        struct Body: Encodable { let watched: Bool }
        _ = try await data("PUT", "/api/titles/\(id)/watched", body: Body(watched: watched))
    }

    /// Every episode of a show, matched or not.
    func setShowWatched(_ name: String, _ watched: Bool) async throws {
        struct Body: Encodable { let watched: Bool }
        _ = try await data("PUT", "/api/shows/\(Path.segment(name))/watched", body: Body(watched: watched))
    }

    /// Every episode of a season of a show.
    func setSeasonWatched(_ name: String, season: Int, _ watched: Bool) async throws {
        struct Body: Encodable { let watched: Bool }
        _ = try await data("PUT", "/api/shows/\(Path.segment(name))/seasons/\(season)/watched", body: Body(watched: watched))
    }

    /// Answers a command this device was sent.
    func ack(_ seq: Int64, ok: Bool, error: String?) async throws {
        struct Body: Encodable { let ok: Bool; let error: String? }
        _ = try await data("POST", "/api/devices/\(Path.segment(device.id))/commands/\(seq)/ack", body: Body(ok: ok, error: error))
    }

    /// The body at a URL the server built, as it arrives (a WebVTT track). Cues of an embedded track
    /// come as ffmpeg reads the file, with long gaps over a slow share: the wait between bytes is long.
    func stream(at path: String) async throws -> URLSession.AsyncBytes {
        guard let url = url(path) else { throw APIError(status: 0, message: "No URL") }
        var req = urlRequest("GET", "/")
        req.url = url
        req.timeoutInterval = 600
        return try await bytes(req)
    }

    // MARK: browsing metadata

    /// The AirPlay targets the server found on the LAN.
    func receivers() async throws -> [Receiver] { try await list("/api/receivers") }

    /// People the library knows, whose name matches `q`.
    func people(_ q: String) async throws -> [PersonCard] {
        try await list("/api/people" + Path.query([("q", q)]))
    }

    /// A person and their credits. A person TVDB and TMDB both know is merged on first open;
    /// the old id's 404 names the survivor, which is followed once.
    func person(_ id: Int64) async throws -> Person {
        do {
            return try await get("/api/people/\(id)")
        } catch let e as APIError where e.status == 404 {
            if let body = e.body, let merged: MergedPerson = try? Self.decode(body) {
                return try await get("/api/people/\(merged.into)")
            }
            throw e
        }
    }

    func genres() async throws -> [GenreCount] { try await list("/api/genres") }

    /// The library's titles, by genre, collection or person (a person's credits).
    func titles(genre: Int64? = nil, collection: Int64? = nil, person: Int64? = nil) async throws -> [TitleCard] {
        var query: [(String, String)] = []
        if let genre { query.append(("genre", String(genre))) }
        if let collection { query.append(("collection", String(collection))) }
        if let person { query.append(("person", String(person))) }
        return try await list("/api/titles" + Path.query(query))
    }

    func collections() async throws -> [CollectionCard] { try await list("/api/collections") }

    func collection(_ id: Int64) async throws -> CollectionDetail { try await get("/api/collections/\(id)") }

    /// External filmography for a person; [] when none or online lookups are off.
    func personFilmography(_ id: Int64) async throws -> [PersonFilmographyEntry] {
        try await list("/api/people/\(id)/filmography")
    }

    /// A title's trailers, best first; [] when it has none or online lookups are off.
    func trailers(titleId: Int64) async throws -> [Trailer] {
        try await list("/api/titles/\(titleId)/trailers")
    }

    /// Shows with upcoming episodes airing today or later (GET /api/upcoming).
    func upcoming() async throws -> [UpcomingShow] {
        try await list("/api/upcoming")
    }

    /// Every episode, for search. The server answers the whole list when no limit is given.
    func allEpisodes() async throws -> [Item] {
        try await list("/api/items?kind=episode&fields=card")
    }
}

/// Escaping for the parts of an API path.
nonisolated enum Path {
    /// One path segment: a show named "AC/DC" stays one segment (%2F), as the server reads it.
    static func segment(_ s: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove("/")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s
    }

    /// "?k=v&...": "+", "&", "=" and ";" are escaped too: Go reads "+" in a query as a space, and
    /// drops a pair with a bare ";".
    static func query(_ items: [(String, String)]) -> String {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "+&=;")
        let pairs = items.map { k, v in
            "\(k)=\(v.addingPercentEncoding(withAllowedCharacters: allowed) ?? v)"
        }
        return pairs.isEmpty ? "" : "?" + pairs.joined(separator: "&")
    }
}
