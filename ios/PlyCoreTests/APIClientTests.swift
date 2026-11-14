import Foundation
import Testing

/// A URLProtocol that answers every request from a handler and records what the client sent, so
/// that the API client can be tested without a server. The suite is serialized: the handler is one
/// process-wide slot.
nonisolated final class StubURL: URLProtocol, @unchecked Sendable {
    struct Reply: Sendable {
        var status: Int
        var body: Data
        var headers: [String: String] = ["Content-Type": "application/json"]

        static func json(_ text: String, status: Int = 200) -> Reply {
            Reply(status: status, body: Data(text.utf8))
        }
    }

    final class Recorder: @unchecked Sendable {
        private let lock = NSLock()
        private var entries: [(request: URLRequest, body: Data?)] = []

        func add(_ request: URLRequest, _ body: Data?) {
            lock.lock()
            entries.append((request, body))
            lock.unlock()
        }

        var requests: [URLRequest] { lock.lock(); defer { lock.unlock() }; return entries.map(\.request) }
        var bodies: [Data?] { lock.lock(); defer { lock.unlock() }; return entries.map(\.body) }
        var count: Int { lock.lock(); defer { lock.unlock() }; return entries.count }
        func clear() { lock.lock(); entries.removeAll(); lock.unlock() }
    }

    nonisolated(unsafe) private static var replyHandler: (@Sendable (URLRequest) -> Reply)?
    static let recorder = Recorder()

    static func reset(_ handler: @escaping @Sendable (URLRequest) -> Reply) {
        replyHandler = handler
        recorder.clear()
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Self.body(of: request)
        Self.recorder.add(request, body)
        let reply = Self.replyHandler?(request) ?? .json(#"{"error":"no stub"}"#, status: 500)
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status,
                                       httpVersion: "HTTP/1.1", headerFields: reply.headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    /// URLSession moves a request's body into a stream before a URLProtocol sees it.
    private static func body(of request: URLRequest) -> Data? {
        if let data = request.httpBody { return data }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 8192)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}

extension DeviceContext {
    /// A fixed device for the tests: a legal id, one registration and the attempt 42.
    static var test: DeviceContext {
        DeviceContext(id: "test-device-0001",
                      registration: DeviceRegistration(name: "Test iPhone", kind: "ios", app: "ply-ios", version: "0.1.0"),
                      nextAttempt: { 42 })
    }
}

private func makeClient(token: String? = "secret", device: DeviceContext = .test) -> APIClient {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [StubURL.self]
    return APIClient(base: URL(string: "http://ply.test:8420")!, token: token, device: device,
                     session: URLSession(configuration: config))
}

nonisolated private func object(_ body: Data?) -> [String: Any]? {
    guard let body, let o = try? JSONSerialization.jsonObject(with: body) as? [String: Any] else { return nil }
    return o
}

@Suite(.serialized)
struct APIClientTests {
    @Test func infoSendsTheDeviceAndReadsTheServer() async throws {
        StubURL.reset { _ in
            .json(#"{"id":"srv-1","name":"Home","version":"1.2.0","authRequired":true,"api":1,"apiMin":1}"#)
        }
        let info = try await makeClient().info()
        #expect(info.id == "srv-1")
        #expect(info.name == "Home")
        #expect(info.api == 1)

        let request = try #require(StubURL.recorder.requests.first)
        #expect(request.httpMethod == "GET")
        #expect(request.url?.path == "/api/info")
        #expect(request.value(forHTTPHeaderField: "X-Ply-Device") == "test-device-0001")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer secret")
    }

    @Test func pairSendsTheRegistrationAndAnswersTheToken() async throws {
        StubURL.reset { _ in .json(#"{"token":"device-token"}"#) }
        let token = try await makeClient(token: nil).pair(code: "123456")
        #expect(token == "device-token")

        let request = try #require(StubURL.recorder.requests.first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path == "/api/pair")
        let body = try #require(object(StubURL.recorder.bodies.first ?? nil))
        #expect(body["code"] as? String == "123456")
        #expect(body["id"] as? String == "test-device-0001")
        #expect(body["name"] as? String == "Test iPhone")
        #expect(body["kind"] as? String == "ios")
        #expect(body["app"] as? String == "ply-ios")
        #expect(body["version"] as? String == "0.1.0")
    }

    @Test func registerDevicePutsTheRegistration() async throws {
        StubURL.reset { _ in StubURL.Reply(status: 204, body: Data()) }
        try await makeClient().registerDevice()
        let request = try #require(StubURL.recorder.requests.first)
        #expect(request.httpMethod == "PUT")
        #expect(request.url?.path == "/api/devices/test-device-0001")
        let body = try #require(object(StubURL.recorder.bodies.first ?? nil))
        #expect(body["name"] as? String == "Test iPhone")
        #expect(body["kind"] as? String == "ios")
    }

    @Test func playbackNamesTheProfileTheReceiverTheSubtitleAndTheAttempt() async throws {
        StubURL.reset { _ in
            .json(#"{"method":"transcode","url":"/api/sessions/s1/index.m3u8","sessionId":"s1","reasons":["burned in"]}"#)
        }
        let playback = try await makeClient().playback(7, startSec: 12.5, audioIndex: 2, subtitleIndex: 3,
                                                      subtitleKey: "ext0", receiver: "AA:BB:CC:DD", profile: "airplay")
        #expect(playback.sessionId == "s1")
        #expect(playback.url == "/api/sessions/s1/index.m3u8")

        let request = try #require(StubURL.recorder.requests.first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path == "/api/items/7/playback")
        let body = try #require(object(StubURL.recorder.bodies.first ?? nil))
        #expect(body["profile"] as? String == "airplay")
        #expect(body["startSec"] as? Double == 12.5)
        #expect(body["audioIndex"] as? Int == 2)
        #expect(body["subtitleIndex"] as? Int == 3)
        #expect(body["subtitleKey"] as? String == "ext0")
        #expect(body["receiver"] as? String == "AA:BB:CC:DD")
        #expect(body["attempt"] as? Int == 42)
    }

    @Test func heartbeatProgressWatchedAndAckGoToTheirPaths() async throws {
        StubURL.reset { _ in StubURL.Reply(status: 204, body: Data()) }
        let client = makeClient()
        try await client.heartbeat("s1", positionSec: 90.5, paused: true, wireless: false)
        try await client.putProgress(7, positionSec: 90.5, durationSec: 6000)
        try await client.setWatched(7, true)
        try await client.ack(9, ok: true, error: nil)

        let requests = StubURL.recorder.requests
        #expect(requests.map(\.httpMethod) == ["POST", "PUT", "PUT", "POST"])
        #expect(requests.map { $0.url?.path } == [
            "/api/sessions/s1/heartbeat", "/api/items/7/progress", "/api/items/7/watched",
            "/api/devices/test-device-0001/commands/9/ack",
        ])
        let heartbeat = try #require(object(StubURL.recorder.bodies[0]))
        #expect(heartbeat["positionSec"] as? Double == 90.5)
        #expect(heartbeat["paused"] as? Bool == true)
        #expect(heartbeat["wireless"] as? Bool == false)
        let watched = try #require(object(StubURL.recorder.bodies[2]))
        #expect(watched["watched"] as? Bool == true)
        let ack = try #require(object(StubURL.recorder.bodies[3]))
        #expect(ack["ok"] as? Bool == true)
        #expect(ack["error"] == nil || ack["error"] is NSNull)
    }

    @Test func peopleAndTitlesBuildTheirQueries() async throws {
        StubURL.reset { _ in .json("[]") }
        let client = makeClient()
        _ = try await client.people("Keanu Reeves")
        _ = try await client.titles(genre: 28, collection: 5, person: 6384)

        let requests = StubURL.recorder.requests
        #expect(requests[0].url?.path == "/api/people")
        #expect(requests[0].url?.query == "q=Keanu%20Reeves")
        #expect(requests[1].url?.path == "/api/titles")
        #expect(requests[1].url?.query == "genre=28&collection=5&person=6384")
    }

    @Test func personFollowsAMergedAnswerOnce() async throws {
        StubURL.reset { request in
            if request.url?.path == "/api/people/9" {
                return .json(#"{"error":"merged","into":7}"#, status: 404)
            }
            return .json(#"""
            {"id":7,"name":"Keanu Reeves","credits":[]}
            """#)
        }
        let person = try await makeClient().person(9)
        #expect(person.id == 7)
        #expect(person.name == "Keanu Reeves")
        #expect(StubURL.recorder.requests.map { $0.url?.path } == ["/api/people/9", "/api/people/7"])
    }

    @Test func aFailureCarriesTheServersMessage() async throws {
        StubURL.reset { _ in .json(#"{"error":"wrong or expired code"}"#, status: 403) }
        do {
            _ = try await makeClient().pair(code: "000000")
            Issue.record("a 403 should throw")
        } catch let e as APIError {
            #expect(e.status == 403)
            #expect(e.message == "wrong or expired code")
            #expect(e.body != nil)
        }
    }

    @Test func a401CallsTheRevocationHandler() async throws {
        StubURL.reset { _ in .json(#"{"error":"revoked"}"#, status: 401) }
        let client = makeClient()
        var revoked = false
        client.onUnauthorized = { revoked = true }
        _ = try? await client.movies()
        #expect(revoked)
    }

    @Test func aFeedStreamIsReadAsItArrives() async throws {
        // The bytes the server writes, through the same parser the feed uses.
        let stream = "retry: 3000\n\nid: 7\nevent: library\ndata: {}\n\n: keep-alive\n\n"
        StubURL.reset { _ in StubURL.Reply(status: 200, body: Data(stream.utf8),
                                           headers: ["Content-Type": "text/event-stream"]) }
        var parser = SSEParser()
        var events: [SSEEvent] = []
        var request = makeClient().urlRequest("GET", "/api/events?topics=library,commands")
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURL.self]
        for try await byte in try await URLSession(configuration: config).bytes(for: request).0 {
            if let event = parser.push(byte) { events.append(event) }
        }
        #expect(events == [SSEEvent(id: "7", type: "library", data: "{}")])
        #expect(parser.retry == 3000)
    }

    @Test func trailersFetchesTitleTrailers() async throws {
        StubURL.reset { req in
            #expect(req.url?.path == "/api/titles/42/trailers")
            return .json(#"[{"site":"youtube","key":"abc12345678","name":"Trailer","type":"trailer"}]"#)
        }
        let trailers = try await makeClient().trailers(titleId: 42)
        #expect(trailers.count == 1)
        #expect(trailers[0].key == "abc12345678")
    }

    @Test func upcomingFetchesUpcomingShows() async throws {
        StubURL.reset { req in
            #expect(req.url?.path == "/api/upcoming")
            return .json(#"[{"titleId":10,"show":"Dark","nextAirDate":"2026-11-20"}]"#)
        }
        let upcoming = try await makeClient().upcoming()
        #expect(upcoming.count == 1)
        #expect(upcoming[0].show == "Dark")
    }

    @Test func personFilmographyFetchesExternalCredits() async throws {
        StubURL.reset { req in
            #expect(req.url?.path == "/api/people/7/filmography")
            return .json(#"[{"kind":"movie","title":"John Wick","year":2014}]"#)
        }
        let filmography = try await makeClient().personFilmography(7)
        #expect(filmography.count == 1)
        #expect(filmography[0].title == "John Wick")
    }

    @Test func markWatchedEndpointsSendExpectedBodies() async throws {
        StubURL.reset { _ in StubURL.Reply(status: 200, body: Data(#"{"watched":true,"items":[]}"#.utf8)) }
        let client = makeClient()

        try await client.setTitleWatched(5, true)
        #expect(StubURL.recorder.requests.last?.url?.path == "/api/titles/5/watched")
        let titleBody = try #require(object(StubURL.recorder.bodies.last ?? nil))
        #expect(titleBody["watched"] as? Bool == true)

        try await client.setShowWatched("The Matrix", true)
        #expect(StubURL.recorder.requests.last?.url?.path == "/api/shows/The Matrix/watched")
        let showBody = try #require(object(StubURL.recorder.bodies.last ?? nil))
        #expect(showBody["watched"] as? Bool == true)

        try await client.setSeasonWatched("The Matrix", season: 2, true)
        #expect(StubURL.recorder.requests.last?.url?.path == "/api/shows/The Matrix/seasons/2/watched")
        let seasonBody = try #require(object(StubURL.recorder.bodies.last ?? nil))
        #expect(seasonBody["watched"] as? Bool == true)
    }
}
