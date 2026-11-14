import Foundation
import Testing

@Suite struct ReconnectTests {
    @Test func theWaitIsBoundedBelowAndAbove() {
        #expect(EventFeed.reconnectDelay(retryMs: 0, failures: 0) == 1, "a retry of 0 must not spin")
        #expect(EventFeed.reconnectDelay(retryMs: 100, failures: 0) == 1)
        #expect(EventFeed.reconnectDelay(retryMs: 3000, failures: 0) == 3)
        #expect(EventFeed.reconnectDelay(retryMs: 3000, failures: 1) == 6)
        #expect(EventFeed.reconnectDelay(retryMs: 3000, failures: 10) == 30)
    }

    @Test func sseParserTracksTheLastEventIDAcrossChunks() {
        var parser = SSEParser()
        let chunk1 = "id: evt-1\nevent: library\ndata: {}\n\n"
        var events: [SSEEvent] = []
        for byte in chunk1.utf8 {
            if let ev = parser.push(byte) { events.append(ev) }
        }
        #expect(events == [SSEEvent(id: "evt-1", type: "library", data: "{}")])
        #expect(parser.lastEventID == "evt-1")

        let chunk2 = "id: evt-2\nevent: snapshot\ndata: {\"api\":1}\n\n"
        for byte in chunk2.utf8 {
            if let ev = parser.push(byte) { events.append(ev) }
        }
        #expect(events.count == 2)
        #expect(events.last == SSEEvent(id: "evt-2", type: "snapshot", data: "{\"api\":1}"))
        #expect(parser.lastEventID == "evt-2")
    }
}

@Suite struct DiscoveryDedupeTests {
    private struct Service: Identifiable {
        let id: String
        let name: String
    }

    @Test func keepsTheFirstOfEachIDInOrder() {
        let services = [Service(id: "a", name: "First"), Service(id: "b", name: "Other"),
                        Service(id: "a", name: "Seen again on another interface")]
        #expect(uniqueByID(services).map(\.name) == ["First", "Other"])
    }
}

/// A URLProtocol for the feed: it answers one event and ends the stream, so the feed reconnects.
nonisolated final class FeedStubURL: URLProtocol, @unchecked Sendable {
    final class Recorder: @unchecked Sendable {
        private let lock = NSLock()
        private var headers: [String?] = []

        func add(_ id: String?) {
            lock.lock()
            headers.append(id)
            lock.unlock()
        }

        var lastEventIDs: [String?] {
            lock.lock()
            defer { lock.unlock() }
            return headers
        }
    }

    static let recorder = Recorder()

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.recorder.add(request.value(forHTTPHeaderField: "Last-Event-ID"))
        let body = Data("retry: 1000\n\nid: evt-1\nevent: library\ndata: {}\n\n".utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": "text/event-stream"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

@Suite(.serialized) struct FeedStreamTests {
    @Test(.timeLimit(.minutes(1)))
    func theFeedReconnectsWithTheLastEventID() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FeedStubURL.self]
        let api = APIClient(base: URL(string: "http://ply.test:8420")!, token: nil, device: .test)
        let feed = EventFeed(api: api, topics: ["library"], session: URLSession(configuration: config)) { _ in }
        feed.start()
        try await Task.sleep(for: .seconds(2.5))
        feed.stop()
        #expect(FeedStubURL.recorder.lastEventIDs.contains("evt-1"))
    }
}
