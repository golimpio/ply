import Foundation
import Testing

// The decisions the app makes before it asks the server to play, kept out of the player so that
// they can be tested without one.

private let livingRoom = Receiver(id: "AA:BB:CC:DD", name: "Living Room", model: "AppleTV3,2",
                                  kind: "airplay", address: "192.168.4.50", port: 7000,
                                  base: "http://192.168.4.196:8420")

@Suite struct PlaybackPlanTests {
    @Test func aLocalTextSubtitleIsDrawnByTheApp() {
        let plan = PlaybackPlanning.plan(subtitle: .text(key: "ext0", url: "/api/items/1/subtitles/ext0.vtt"),
                                         receiver: nil, externalPlayback: false)
        #expect(plan.profile == "avplayer")
        #expect(plan.receiver == nil)
        #expect(plan.subtitleIndex == nil)
        #expect(plan.subtitleKey == nil)
        #expect(plan.drawsSubtitles)
    }

    @Test func aTextSubtitleOnAirPlayIsARenditionTheTVShows() {
        let plan = PlaybackPlanning.plan(subtitle: .text(key: "ext0", url: "/vtt"),
                                         receiver: livingRoom, externalPlayback: false)
        #expect(plan.profile == "airplay")
        #expect(plan.receiver == "AA:BB:CC:DD")
        #expect(plan.subtitleKey == "ext0")
        #expect(plan.subtitleIndex == nil)
        #expect(plan.drawsSubtitles == false)
    }

    @Test func startingAirPlayWithoutAReceiverStillUsesTheAirPlayProfile() {
        let plan = PlaybackPlanning.plan(subtitle: nil, receiver: nil, externalPlayback: true)
        #expect(plan.profile == "airplay")
        #expect(plan.receiver == nil)
        #expect(plan.drawsSubtitles == false)
    }

    @Test func aBitmapSubtitleIsBurnedInOnBothProfiles() {
        let local = PlaybackPlanning.plan(subtitle: .burnIn(index: 3), receiver: nil, externalPlayback: false)
        #expect(local.profile == "avplayer")
        #expect(local.subtitleIndex == 3)
        #expect(local.subtitleKey == nil)

        let air = PlaybackPlanning.plan(subtitle: .burnIn(index: 3), receiver: livingRoom, externalPlayback: false)
        #expect(air.profile == "airplay")
        #expect(air.subtitleIndex == 3)
        #expect(air.subtitleKey == nil)
    }

    @Test func noSubtitleAsksForNothing() {
        let plan = PlaybackPlanning.plan(subtitle: nil, receiver: nil, externalPlayback: false)
        #expect(plan == PlaybackPlan(profile: "avplayer", receiver: nil, subtitleIndex: nil,
                                     subtitleKey: nil, drawsSubtitles: false))
    }
}

@Suite struct ConnectFailureTests {
    @Test func anIncompatibleVersionIsNotUnreachable() {
        let failure = ConnectFailure.classify(CompatibilityProblem(problem: "This server is newer than the app: update the ply app."))
        #expect(failure == .incompatible("This server is newer than the app: update the ply app."))
    }

    @Test func anythingElseIsUnreachableWithItsMessage() {
        let failure = ConnectFailure.classify(APIError(status: 0, message: "Cannot reach the ply server (timed out)"))
        #expect(failure == .unreachable("Cannot reach the ply server (timed out)"))
    }
}

@Suite struct CommandHistoryTests {
    @Test func remembersTheNewestSeqPerServer() {
        let suite = "ply.command-history-tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }

        #expect(CommandHistory.lastHandled(for: "server-a", defaults: defaults) == 0)
        CommandHistory.setLastHandled(41, for: "server-a", defaults: defaults)
        CommandHistory.setLastHandled(7, for: "server-b", defaults: defaults)
        #expect(CommandHistory.lastHandled(for: "server-a", defaults: defaults) == 41)
        #expect(CommandHistory.lastHandled(for: "server-b", defaults: defaults) == 7)
        #expect(CommandHistory.lastHandled(for: "server-c", defaults: defaults) == 0)
    }
}
