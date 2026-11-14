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
