import Foundation
import Testing

private func events(_ stream: String) -> [SSEEvent] {
    var parser = SSEParser()
    return Array(stream.utf8).compactMap { parser.push($0) }
}

@Suite struct SSEParserTests {
    @Test func aBlankLineEndsAFrameWhicheverLineEnding() {
        let want = [
            SSEEvent(id: "7", type: "library", data: "{}"),
            SSEEvent(id: "8", type: "job.done", data: #"{"job":1}"#),
        ]
        for eol in ["\n", "\r\n", "\r"] {
            let stream = ["id: 7", "event: library", "data: {}", "", "id: 8", "event: job.done", #"data: {"job":1}"#, "", ""]
                .joined(separator: eol)
            #expect(events(stream) == want, "line ending \(eol.debugDescription)")
        }
    }

    @Test func aFrameIsNotDispatchedBeforeItsBlankLine() {
        #expect(events("event: library\ndata: {}\n").isEmpty)
    }

    @Test func commentsRetryAndDataLessFramesAreNoEvents() {
        var parser = SSEParser()
        let got = Array("retry: 3000\n\n: keep-alive\n\nid: 4\nevent: tier\n\n".utf8).compactMap { parser.push($0) }
        #expect(got.isEmpty)
        #expect(parser.retry == 3000)
        #expect(parser.lastEventID == "4", "an id without data still moves the resume point")
    }

    @Test func dataLinesJoinWithNewlinesAndOneLeadingSpaceIsDropped() {
        #expect(events("data:a\ndata:  b\ndata\n\n") == [SSEEvent(id: nil, type: "message", data: "a\n b\n")])
    }

    @Test func theIdCarriesOverToLaterEventsAndTheTypeDoesNot() {
        let got = events("id: 9\nevent: library\ndata: 1\n\ndata: 2\n\n")
        #expect(got == [SSEEvent(id: "9", type: "library", data: "1"), SSEEvent(id: "9", type: "message", data: "2")])
    }
}

@Suite struct WebVTTTests {
    let track = WebVTT("""
    WEBVTT\r
    \r
    NOTE made by ffmpeg\r
    \r
    1\r
    00:00:01.000 --> 00:00:03.500 line:90%\r
    <i>Hello</i> &amp; welcome\r
    \r
    01:02.250 --> 01:04.000\r
    Two\r
    lines\r
    \r
    00:00:03.000 --> 00:00:05.000\r
    Overlap
    """)

    @Test func cuesAreReadWithTheirTimesAndTextWithoutMarkup() {
        #expect(track.cues == [
            .init(start: 1, end: 3.5, text: "Hello & welcome"),
            .init(start: 3, end: 5, text: "Overlap"),
            .init(start: 62.25, end: 64, text: "Two\nlines"),
        ])
    }

    @Test func theTextAtATimeIsEveryCueThatCoversIt() {
        #expect(track.text(at: 0.5) == nil)
        #expect(track.text(at: 1) == "Hello & welcome")
        #expect(track.text(at: 3.2) == "Hello & welcome\nOverlap")
        #expect(track.text(at: 3.5) == "Overlap", "a cue's end is not part of it")
        #expect(track.text(at: 10) == nil)
        #expect(track.text(at: 63) == "Two\nlines")
    }

    /// "Downloading subtitles" is for a play ahead of the track still arriving, not for every pause
    /// between two lines while it arrives (it flashed between lines through a whole film).
    @Test func aTrackStillArrivingIsPendingOnlyPastItsLastLine() {
        #expect(WebVTT().isPending(at: 0, arriving: true), "nothing read yet: a line may come")
        #expect(track.text(at: 10) == nil)
        #expect(track.isPending(at: 10, arriving: true) == false, "a pause the track has passed is a pause")
        #expect(track.isPending(at: 62.25, arriving: true) == false)
        #expect(track.isPending(at: 70, arriving: true), "past the last line read: the next may not be here yet")
        #expect(track.isPending(at: 70, arriving: false) == false, "a whole track is never pending")
        #expect(track.isPending(at: .nan, arriving: true), "no time yet: still waiting")
    }
}

@Suite struct RFC3339Tests {
    @Test func goTimesWithNanosecondsAndWithout() throws {
        let whole = try #require(RFC3339.date("2026-11-12T11:00:00Z"))
        #expect(whole.timeIntervalSince1970 == 1_794_481_200)
        let nanos = try #require(RFC3339.date("2026-11-12T11:00:00.123456789Z"))
        #expect(abs(nanos.timeIntervalSince(whole) - 0.123456789) < 1e-6)
        let offset = try #require(RFC3339.date("2026-11-12T22:00:00.5+11:00"))
        #expect(offset.timeIntervalSince(whole) == 0.5)
    }

    @Test func notATime() {
        #expect(RFC3339.date("2026-11-12") == nil)
        #expect(RFC3339.date("2026-11-12T11:00:00.Z") == nil)
    }
}

@Suite struct AddressTests {
    @Test func whatIsTypedBecomesABaseURL() {
        #expect(ServerAddress.parse(" 192.168.1.10 ")?.absoluteString == "http://192.168.1.10:8420")
        #expect(ServerAddress.parse("nas.local:9000")?.absoluteString == "http://nas.local:9000")
        #expect(ServerAddress.parse("HTTPS://ply.home.arpa/web?x=1")?.absoluteString == "https://ply.home.arpa")
        #expect(ServerAddress.parse("http://10.0.0.2")?.absoluteString == "http://10.0.0.2", "a URL keeps its scheme's port")
        #expect(ServerAddress.parse("[fe80::1]:8420")?.absoluteString == "http://[fe80::1]:8420")
    }

    @Test func notAnAddress() {
        for typed in ["", "   ", "ftp://nas.local", "two words", "http://"] {
            #expect(ServerAddress.parse(typed) == nil, "\(typed.debugDescription)")
        }
    }

    @Test func pathsKeepAShowNameOneSegmentAndQueriesWhatGoDropsOrReadsOtherwise() {
        #expect(Path.segment("AC/DC?") == "AC%2FDC%3F")
        #expect(Path.query([("show", "Love+Death & Robots; Too"), ("kind", "episode")])
            == "?show=Love%2BDeath%20%26%20Robots%3B%20Too&kind=episode")
    }
}

@Suite struct VersionTests {
    @Test func aClientWorksWithServersFromApiMinToApi() {
        #expect(Compatibility(api: 1, apiMin: 1, client: 1) == .compatible)
        #expect(Compatibility(api: 3, apiMin: 1, client: 2) == .compatible)
        #expect(Compatibility(api: 3, apiMin: 2, client: 1) == .updateApp)
        #expect(Compatibility(api: 1, apiMin: 1, client: 2) == .updateServer)
    }

    @Test func aServerOlderThanVersioningIsOneToUpdate() throws {
        let old = #"{"id":"a","name":"ply","version":"0.9","authRequired":false}"#
        let info: ServerInfo = try APIClient.decode(Data(old.utf8))
        #expect(Compatibility(api: info.api, apiMin: info.apiMin) == .updateServer)
    }

    @Test func theAttemptNumberAlwaysGoesUp() {
        #expect(DeviceContext.nextAttempt(after: 0, now: 1_000) == 1_000)
        #expect(DeviceContext.nextAttempt(after: 5_000, now: 1_000) == 5_001, "the clock went back")
        #expect(DeviceContext.nextAttempt(after: 1 << 53, now: 1_000) == 1_000, "a stored number the server would refuse is forgotten")
    }
}
