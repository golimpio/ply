import Foundation

/// One event of a Server-Sent Events stream.
nonisolated struct SSEEvent: Equatable, Sendable {
    /// The last event id the stream set, as of this event (what Last-Event-ID resumes from).
    var id: String?
    var type: String
    var data: String
}

/// Reads an SSE stream byte by byte (the HTML spec's event-stream interpretation). Lines end in
/// CRLF, LF or CR, and a blank line dispatches the event, which is why AsyncBytes.lines, which
/// drops blank lines, cannot be used (docs/M11-CLIENTS.md, "Platforms").
nonisolated struct SSEParser {
    private(set) var lastEventID: String?
    /// The reconnection time the server asked for, in milliseconds.
    private(set) var retry: Int?

    private var line: [UInt8] = []
    private var afterCR = false
    private var type = ""
    private var data = ""
    private var hasData = false

    /// Feeds one byte; answers the event a blank line dispatched.
    mutating func push(_ byte: UInt8) -> SSEEvent? {
        switch byte {
        case 0x0A where afterCR:
            afterCR = false
            return nil
        case 0x0A, 0x0D:
            afterCR = byte == 0x0D
            let text = String(decoding: line, as: UTF8.self)
            line.removeAll(keepingCapacity: true)
            return process(text)
        default:
            afterCR = false
            line.append(byte)
            return nil
        }
    }

    private mutating func process(_ text: String) -> SSEEvent? {
        if text.isEmpty { return dispatch() }
        if text.hasPrefix(":") { return nil } // a comment: the keep-alive
        let field: Substring
        var value: Substring
        if let colon = text.firstIndex(of: ":") {
            field = text[..<colon]
            value = text[text.index(after: colon)...]
            if value.hasPrefix(" ") { value = value.dropFirst() }
        } else {
            field = text[...]
            value = ""
        }
        switch field {
        case "event":
            type = String(value)
        case "data":
            if hasData { data += "\n" }
            data += value
            hasData = true
        case "id" where !value.contains("\0"):
            lastEventID = String(value)
        case "retry":
            if let ms = Int(value), ms >= 0 { retry = ms }
        default:
            break
        }
        return nil
    }

    private mutating func dispatch() -> SSEEvent? {
        defer {
            type = ""
            data = ""
            hasData = false
        }
        guard hasData else { return nil }
        return SSEEvent(id: lastEventID, type: type.isEmpty ? "message" : type, data: data)
    }
}

/// The live feed, GET /api/events (docs/M11-CLIENTS.md, "The live feed"): reconnects on its own,
/// resuming with Last-Event-ID, until stopped. Stop it when the app goes to the background and start
/// it again on return: the server replays what was missed, or sends a snapshot.
final class EventFeed {
    private let api: APIClient
    private let topics: String
    private let onEvent: (SSEEvent) -> Void
    private let session: URLSession
    private var task: Task<Void, Never>?
    private var lastEventID: String?

    /// A session of its own, so the stream holds none of the API session's connections; its idle
    /// timeout is well over the server's 15 s keep-alive.
    private static let session: URLSession = {
        let config = URLSessionConfiguration.default
        config.timeoutIntervalForRequest = 60
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: config)
    }()

    init(api: APIClient, topics: [String], session: URLSession? = nil, onEvent: @escaping (SSEEvent) -> Void) {
        self.api = api
        self.topics = topics.joined(separator: ",")
        self.session = session ?? Self.session
        self.onEvent = onEvent
    }

    func start() {
        guard task == nil else { return }
        task = Task { [weak self] in await self?.run() }
    }

    func stop() {
        task?.cancel()
        task = nil
    }

    private func run() async {
        var failures = 0
        while !Task.isCancelled {
            var retryMs = 3000
            do {
                retryMs = try await follow()
                failures = 0
            } catch let e as APIError where e.status == 401 {
                return // revoked: the client's onUnauthorized has been told
            } catch {
                if Task.isCancelled { return }
                failures += 1
            }
            // The server's retry, then longer after repeated failures, at most 30 s. A retry of 0
            // (or a proxy that strips the value) must not turn into a tight loop.
            let delay = Self.reconnectDelay(retryMs: retryMs, failures: failures)
            try? await Task.sleep(for: .seconds(delay))
        }
    }

    /// The wait before reconnecting: the server's retry, doubled after each failure, at most 30 s
    /// and at least 1 s (a server, or a proxy, that sends `retry: 0` must not make a tight loop).
    nonisolated static func reconnectDelay(retryMs: Int, failures: Int) -> Double {
        max(1, min(30, Double(retryMs) / 1000 * Double(1 << min(failures, 4))))
    }

    /// Reads the stream until it ends; answers the retry the server asked for.
    private func follow() async throws -> Int {
        var req = api.urlRequest("GET", "/api/events" + Path.query([("topics", topics)]))
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        if let lastEventID { req.setValue(lastEventID, forHTTPHeaderField: "Last-Event-ID") }
        let parser = try await Self.read(try await api.bytes(req, session: session)) { [weak self] event in
            guard let self else { return }
            self.lastEventID = event.id ?? self.lastEventID
            self.onEvent(event)
        }
        return parser.retry ?? 3000
    }

    /// Parses a feed's bytes off the main actor (the stream is read for hours); each event is handed
    /// to it in order. The parser is the one the tests run.
    private nonisolated static func read(_ bytes: URLSession.AsyncBytes,
                                         onEvent: @escaping @MainActor @Sendable (SSEEvent) -> Void) async throws -> SSEParser {
        var parser = SSEParser()
        for try await byte in bytes {
            if let event = parser.push(byte) { await onEvent(event) }
        }
        return parser
    }
}
