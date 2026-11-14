import Foundation
import Testing

// The library's fetch of the episodes is lazy and shared: Home must not ask for them, two pages that
// need them at once must cost one request, and a reload during the fetch must not lose them.

/// Its own URLProtocol: this suite needs the request log and a reply that is slow on purpose, and
/// Swift Testing runs suites side by side, so it must not share `StubURL` with the API client's suite.
nonisolated final class LibraryStub: URLProtocol, @unchecked Sendable {
    struct Reply: Sendable {
        var status: Int
        var body: Data

        static func json(_ text: String, status: Int = 200) -> Reply {
            Reply(status: status, body: Data(text.utf8))
        }
    }

    final class Recorder: @unchecked Sendable {
        private let lock = NSLock()
        private var paths: [String] = []

        func add(_ path: String) {
            lock.lock()
            paths.append(path)
            lock.unlock()
        }

        var all: [String] { lock.lock(); defer { lock.unlock() }; return paths }

        func clear() {
            lock.lock()
            paths.removeAll()
            lock.unlock()
        }
    }

    nonisolated(unsafe) private static var replyHandler: (@Sendable (URLRequest) -> Reply)?
    /// Holds back the next episode's answer, so that a test can act while it is on its way.
    nonisolated(unsafe) static var holdNextEpisode = false
    nonisolated(unsafe) private static var held: [LibraryStub] = []
    static let recorder = Recorder()

    static var heldCount: Int { held.count }

    /// Sends the answers that were held back, as the server would once they are ready.
    static func deliverHeld() {
        let answers = held
        held = []
        for stub in answers { stub.deliver() }
    }

    private var pending: Reply?

    static func reset(_ handler: @escaping @Sendable (URLRequest) -> Reply) {
        replyHandler = handler
        holdNextEpisode = false
        held = []
        recorder.clear()
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let request = self.request
        let path = (request.url?.path ?? "") + (request.url?.query.map { "?\($0)" } ?? "")
        Self.recorder.add(path)
        pending = Self.replyHandler?(request) ?? .json(#"{"error":"unstubbed"}"#, status: 500)
        if Self.holdNextEpisode, path.contains("kind=episode") {
            Self.holdNextEpisode = false
            Self.held.append(self)
        } else {
            deliver()
        }
    }

    private func deliver() {
        guard let reply = pending else { return }
        pending = nil
        let response = HTTPURLResponse(
            url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private func libraryClient() -> APIClient {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [LibraryStub.self]
    return APIClient(base: URL(string: "http://ply.test:8420")!, token: "secret", device: .test,
                     session: URLSession(configuration: config))
}

/// The server's answers, by the request it is asked: the episodes are the list this suite counts.
nonisolated private func reply(for request: URLRequest) -> LibraryStub.Reply {
    let path = request.url?.path
    let query = request.url?.query ?? ""
    switch (path, query) {
    case ("/api/items", let q) where q.contains("kind=episode"):
        return .json(#"""
        [{"id":1,"kind":"episode","title":"Pilot","show":"Lost","season":1,"episode":1,"durationSec":1,"addedAt":10},
         {"id":2,"kind":"episode","title":"Two","show":"Lost","season":1,"episode":2,"durationSec":1,"addedAt":30}]
        """#)
    case ("/api/items", _):
        return .json(#"[{"id":11,"kind":"movie","title":"Heat","durationSec":1}]"#)
    case ("/api/shows", _):
        return .json(#"[{"show":"Lost","episodeCount":2,"year":2004,"rating":{"source":"imdb","value":8.3,"scale":10,"score100":83}}]"#)
    case ("/api/continue", _), ("/api/upcoming", _):
        return .json("[]")
    default:
        return .json(#"{"error":"unmocked"}"#, status: 404)
    }
}

/// How many times the episodes were asked for, across the suite.
private func episodeRequests() -> Int {
    LibraryStub.recorder.all.filter { $0.hasPrefix("/api/items?") && $0.contains("kind=episode") }.count
}

@Suite(.serialized)
struct LibraryModelTests {
    @Test @MainActor func homeLoadsOnlyItsOwnListsAndTheEpisodesComeWhenAPageAsks() async {
        LibraryStub.reset { reply(for: $0) }
        let library = LibraryModel(api: libraryClient())

        await library.reload()
        #expect(episodeRequests() == 0, "the episodes are not fetched with Home")
        #expect(library.episodesLoaded == false)

        #expect(library.movieSearch.search("heat").map(\.id) == [11], "the movies are ready to search as they load")
        #expect(library.showSearch.search("lost").count == 1)
        #expect(library.shows.first?.rating?.score == 83, "a show's rating is read for its sort")

        await library.ensureEpisodes()
        #expect(episodeRequests() == 1)
        #expect(library.episodesLoaded)
        #expect(library.episodes.map(\.id) == [1, 2])
        #expect(library.latestAddedAt == ["Lost": 30], "a show is as new as its newest file")
        #expect(library.episodeSearch.search("two").map(\.id) == [2])
    }

    @Test @MainActor func aSecondAskIsAnsweredFromWhatIsLoaded() async {
        LibraryStub.reset { reply(for: $0) }
        let library = LibraryModel(api: libraryClient())

        await library.ensureEpisodes()
        await library.ensureEpisodes()
        #expect(episodeRequests() == 1)
    }

    @Test @MainActor func twoPagesAskingAtOnceShareOneFetch() async {
        LibraryStub.reset { reply(for: $0) }
        let library = LibraryModel(api: libraryClient())

        // Browse opening and Shows sorting by "Recently updated" at the same moment.
        async let browse: Void = library.ensureEpisodes()
        async let shows: Void = library.ensureEpisodes()
        _ = await (browse, shows)

        #expect(episodeRequests() == 1, "one request, not one per page")
        #expect(library.episodesLoaded)
        #expect(library.episodes.count == 2)
    }

    @Test @MainActor func aReloadForgetsTheEpisodesAndTheNextAskFetchesAgain() async {
        LibraryStub.reset { reply(for: $0) }
        let library = LibraryModel(api: libraryClient())

        await library.reload()
        await library.ensureEpisodes()
        #expect(episodeRequests() == 1)

        await library.reload()
        #expect(library.episodesLoaded == false, "a new library version drops the episodes read for the old one")
        #expect(library.episodes.isEmpty)

        await library.ensureEpisodes()
        #expect(episodeRequests() == 2)
    }

    @Test @MainActor func aReloadWhileTheFetchIsOnItsWayDoesNotLoseTheEpisodes() async {
        LibraryStub.reset { request in reply(for: request) }
        LibraryStub.holdNextEpisode = true
        defer { LibraryStub.reset { request in reply(for: request) } }
        let library = LibraryModel(api: libraryClient())

        async let ask: Void = library.ensureEpisodes()
        for _ in 0..<1_000 where LibraryStub.heldCount == 0 { await Task.yield() }
        #expect(LibraryStub.heldCount == 1, "the episodes are on their way")
        await library.reload() // the library changes while the answer is on its way
        LibraryStub.deliverHeld() // and the answer lands, for the version the reload replaced
        await ask

        #expect(episodeRequests() == 2, "the answer the reload invalidated is fetched again for it")
        #expect(library.episodesLoaded, "the pages that asked are left with the episodes, not with nothing")
        #expect(library.episodes.count == 2)
    }

    @Test @MainActor func aFailedFetchIsReportedAndCanBeAskedAgain() async {
        LibraryStub.reset { _ in .json(#"{"error":"the episodes are gone"}"#, status: 500) }
        let library = LibraryModel(api: libraryClient())

        await library.ensureEpisodes()
        #expect(library.episodesLoaded == false)
        #expect(library.episodesError?.contains("the episodes are gone") == true, "\(library.episodesError ?? "nil")")

        LibraryStub.reset { reply(for: $0) }
        await library.ensureEpisodes()
        #expect(library.episodesLoaded, "a second ask tries again")
        #expect(library.episodesError == nil)
        #expect(library.episodes.count == 2)
    }
}
