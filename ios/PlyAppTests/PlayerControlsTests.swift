import AVKit
import SwiftUI
import UIKit
import XCTest
@testable import Ply

#if os(iOS)
/// ply's own player on iPhone (docs/M11-CLIENTS.md, "The player"): one size of control on a shared grid, the
/// speed and tracks at the foot beside the timeline, the remote in place of the picture, and a close that
/// ends the play.
nonisolated final class PlayerControlsTests: XCTestCase {
    @MainActor
    private func playback(_ id: String) -> PlaybackController {
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: id,
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        return PlaybackController(api: APIClient(base: base, token: nil, device: device),
                                  request: PlayRequest(itemId: 1, startSec: 0))
    }

    @MainActor
    private func scene() throws -> UIWindowScene {
        try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
    }

    /// Hosts `view` full screen and returns the frames its `playerFrame` calls publish, with the window.
    @MainActor
    private func frames<V: View>(of view: V, needs: [String]) throws -> (frames: [String: CGRect], window: UIWindow) {
        var collected: [String: CGRect] = [:]
        let window = UIWindow(windowScene: try scene())
        window.rootViewController = UIHostingController(
            rootView: view.preferredColorScheme(.dark).onPreferenceChange(PlayerLayoutFramesKey.self) { collected = $0 }
        )
        window.makeKeyAndVisible()
        let deadline = Date().addingTimeInterval(3)
        while Date() < deadline, !needs.allSatisfy({ collected[$0] != nil }) {
            window.layoutIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.02))
        }
        RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        return (collected, window)
    }

    @MainActor
    func testControlsShareOneSizeAndLineUpOnTheirRows() throws {
        let playback = playback("test-device-player-grid")
        let transport = PlayerTransport(playback: playback)
        let chrome = PlayerChrome(player: playback.player)
        defer { transport.detach(); chrome.detach() }
        let circles = ["close", "pip", "airplay", "speed", "tracks", "mute"]
        let (f, window) = try frames(
            of: PlayerControls(transport: transport, chrome: chrome, pictureInPicture: PlayerPictureInPicture(),
                               onClose: {}, onFill: { _ in }),
            needs: circles + ["title", "timeline", "back", "playPause", "forward"]
        )
        defer { window.isHidden = true }
        let safe = window.bounds.inset(by: window.safeAreaInsets)
        let leading = safe.minX + PlyDesign.contentHorizontalPadding
        let trailing = safe.maxX - PlyDesign.contentHorizontalPadding

        // Every single control is the same glass circle: the mismatched sizes of AVKit's top bar are gone.
        for id in circles {
            let frame = try XCTUnwrap(f[id], id)
            XCTAssertEqual(frame.width, PlyDesign.iconButtonSize, accuracy: 0.5, id)
            XCTAssertEqual(frame.height, PlyDesign.iconButtonSize, accuracy: 0.5, id)
        }

        // Top row: close on the leading edge, Picture in Picture then AirPlay on the trailing one, one line.
        let close = f["close"]!, pip = f["pip"]!, airplay = f["airplay"]!
        XCTAssertEqual(close.minX, leading, accuracy: 0.5)
        XCTAssertEqual(airplay.maxX, trailing, accuracy: 0.5)
        XCTAssertEqual(airplay.minX - pip.maxX, PlyDesign.glassCircleGroupSpacing, accuracy: 0.5)
        XCTAssertEqual(pip.midY, close.midY, accuracy: 0.5)
        XCTAssertEqual(airplay.midY, close.midY, accuracy: 0.5)

        // Foot: the title, then speed, tracks and mute on the trailing edge, all on one line over the timeline.
        let title = f["title"]!, speed = f["speed"]!, tracks = f["tracks"]!, mute = f["mute"]!, timeline = f["timeline"]!
        XCTAssertEqual(title.minX, leading, accuracy: 0.5)
        XCTAssertEqual(mute.maxX, trailing, accuracy: 0.5)
        XCTAssertLessThan(title.maxX, speed.minX)
        XCTAssertEqual(tracks.minX - speed.maxX, PlyDesign.glassCircleGroupSpacing, accuracy: 0.5)
        XCTAssertEqual(mute.minX - tracks.maxX, PlyDesign.glassCircleGroupSpacing, accuracy: 0.5)
        for frame in [title, tracks, mute] { XCTAssertEqual(frame.midY, speed.midY, accuracy: 0.5) }
        XCTAssertEqual(timeline.minY - speed.maxY, PlyDesign.sectionHeaderSpacing, accuracy: 0.5)
        XCTAssertEqual(timeline.minX, leading, accuracy: 0.5)
        XCTAssertEqual(timeline.maxX, trailing, accuracy: 0.5)
        XCTAssertEqual(timeline.height, PlyDesign.iconButtonSize, accuracy: 0.5)

        // Middle: back, play/pause and forward centred between the rows, the outer two the same size.
        let back = f["back"]!, play = f["playPause"]!, forward = f["forward"]!
        XCTAssertEqual(play.midX, safe.midX, accuracy: 0.5)
        XCTAssertEqual(play.width, PlyDesign.heroPlaySize, accuracy: 0.5)
        XCTAssertEqual(back.width, PlyDesign.playerSkipSize, accuracy: 0.5)
        XCTAssertEqual(forward.width, PlyDesign.playerSkipSize, accuracy: 0.5)
        XCTAssertEqual(play.minX - back.maxX, forward.minX - play.maxX, accuracy: 0.5)
        XCTAssertEqual(back.midY, play.midY, accuracy: 0.5)
        XCTAssertGreaterThan(play.minY, close.maxY)
        XCTAssertLessThan(play.maxY, speed.minY)
    }

    @MainActor
    func testRemoteShowsWhereItPlaysThenTheTransportThenStop() throws {
        let playback = playback("test-device-player-remote")
        let transport = PlayerTransport(playback: playback)
        defer { transport.detach() }
        let (f, window) = try frames(
            of: PlayerRemoteView(transport: transport, playback: playback, onStop: {}),
            needs: ["remoteRoute", "remotePoster", "remoteTitle", "remoteTimeline", "playPause", "remoteStop"]
        )
        defer { window.isHidden = true }
        let order = ["remoteRoute", "remotePoster", "remoteTitle", "remoteTimeline", "playPause", "remoteStop"]
        for (upper, lower) in zip(order, order.dropFirst()) {
            XCTAssertLessThanOrEqual(try XCTUnwrap(f[upper], upper).maxY, try XCTUnwrap(f[lower], lower).minY,
                                     "\(upper) above \(lower)")
        }
        XCTAssertEqual(f["remoteStop"]!.width, PlyDesign.iconButtonSize, accuracy: 0.5)
        XCTAssertEqual(f["playPause"]!.midX, window.bounds.midX, accuracy: 0.5)
    }

    @MainActor
    func testSpeedReadsAsAMultiplierAndHoldsForTheRestOfThePlay() {
        XCTAssertEqual(PlayerTransport.speedLabel(1), "1×")
        XCTAssertEqual(PlayerTransport.speedLabel(1.5), "1.5×")
        XCTAssertEqual(PlayerTransport.speedLabel(0.75), "0.75×")
        XCTAssertEqual(PlayerTransport.speeds.first, 0.5)
        XCTAssertEqual(PlayerTransport.speeds.last, 2)

        let playback = playback("test-device-player-speed")
        let transport = PlayerTransport(playback: playback)
        defer { transport.detach() }
        XCTAssertEqual(playback.player.defaultRate, 1)
        transport.setSpeed(1.5)
        XCTAssertEqual(transport.speed, 1.5)
        // `play()` starts at `defaultRate`: a resume, or a stream reopened for another audio track, keeps it.
        XCTAssertEqual(playback.player.defaultRate, 1.5)
    }

    @MainActor
    func testScrubbingShowsWhereTheFingerIsAndASeekNeverGoesBeforeTheStart() {
        let playback = playback("test-device-player-scrub")
        let transport = PlayerTransport(playback: playback)
        defer { transport.detach() }
        XCTAssertEqual(transport.shownPosition, 0)
        transport.scrub(to: 42)
        XCTAssertEqual(transport.shownPosition, 42)
        transport.scrub(to: nil)
        XCTAssertEqual(transport.shownPosition, 0)
        transport.scrub(to: 42)
        transport.seek(to: -5)
        XCTAssertNil(transport.scrubPosition)
        XCTAssertEqual(transport.shownPosition, 0)
    }

    @MainActor
    func testATapHidesEveryControlTogetherAndAnotherBringsThemBack() {
        let playback = playback("test-device-player-chrome")
        let chrome = PlayerChrome(player: playback.player)
        defer { chrome.detach() }
        XCTAssertTrue(chrome.visible)
        chrome.toggle()
        XCTAssertFalse(chrome.visible)
        chrome.toggle()
        XCTAssertTrue(chrome.visible)
    }

    /// A player that is playing (an empty one asked to play waits to, which is not paused).
    @MainActor
    private func playingChrome(_ id: String, assistive: Bool = false) -> (PlayerChrome, PlaybackController) {
        let playback = playback(id)
        playback.player.play()
        XCTAssertNotEqual(playback.player.timeControlStatus, .paused)
        let chrome = PlayerChrome(player: playback.player, hideDelay: .milliseconds(50),
                                  assistiveTechnologyRunning: { assistive })
        return (chrome, playback)
    }

    @MainActor
    private func wait(_ seconds: TimeInterval) {
        RunLoop.current.run(until: Date().addingTimeInterval(seconds))
    }

    @MainActor
    func testControlsHideOnTheirOwnWhilePlayingButNotUnderAFingerOrForVoiceOver() {
        let (chrome, playback) = playingChrome("test-device-player-autohide")
        defer { chrome.detach(); playback.player.pause() }
        chrome.wake()
        XCTAssertTrue(chrome.visible)
        wait(0.3)
        XCTAssertFalse(chrome.visible, "playing, untouched: the controls go")

        chrome.wake()
        chrome.holding = true
        wait(0.3)
        XCTAssertTrue(chrome.visible, "a finger on the timeline keeps them up")
        chrome.holding = false
        wait(0.3)
        XCTAssertFalse(chrome.visible, "and they go once it lets go")

        // VoiceOver or Switch Control: hidden controls would leave nothing to reach, so they stay.
        let (assisted, assistedPlayback) = playingChrome("test-device-player-voiceover", assistive: true)
        defer { assisted.detach(); assistedPlayback.player.pause() }
        assisted.wake()
        wait(0.3)
        XCTAssertTrue(assisted.visible)
        assisted.toggle()
        XCTAssertTrue(assisted.visible)
    }

    /// The close button ends the play (the presenter's `onDismiss`, which `AppModel` ends it on) and takes
    /// the player off the screen: there is no AVKit button left to do it.
    @MainActor
    func testCloseEndsThePlayAndTakesThePlayerDown() throws {
        let window = UIWindow(windowScene: try scene())
        window.rootViewController = UIViewController()
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        let presenter = PlayerPresenter()
        var ended = 0
        presenter.present(playback("test-device-player-close"), in: window.windowScene) { ended += 1 }

        func spin(until done: () -> Bool) {
            let deadline = Date().addingTimeInterval(3)
            while Date() < deadline, !done() { RunLoop.current.run(until: Date().addingTimeInterval(0.02)) }
        }
        spin { window.rootViewController?.presentedViewController is PlayerViewController }
        let player = try XCTUnwrap(window.rootViewController?.presentedViewController as? PlayerViewController)
        XCTAssertEqual(ended, 0)

        player.onClose?()
        spin { window.rootViewController?.presentedViewController == nil }
        XCTAssertEqual(ended, 1)
        XCTAssertNil(window.rootViewController?.presentedViewController)
    }

    /// Nothing the player sets up (its controls' closures, observers, Picture in Picture, the hide timer)
    /// keeps it alive once it is torn down. (Measured off the screen: in a hosted test UIKit keeps any
    /// controller it presented, even a plain one, for the rest of the test.)
    @MainActor
    func testATornDownPlayerHoldsNothingThatKeepsItAlive() {
        let playback = playback("test-device-player-release")
        final class Delegate: NSObject, AVPictureInPictureControllerDelegate {}
        let delegate = Delegate()
        weak var released: PlayerViewController?
        autoreleasepool {
            let player = PlayerViewController(playback: playback, pictureInPictureDelegate: delegate)
            player.loadViewIfNeeded()
            XCTAssertFalse(player.children.isEmpty, "the controls are hosted")
            player.tearDown()
            released = player
        }
        RunLoop.current.run(until: Date().addingTimeInterval(0.3))
        XCTAssertNil(released)
    }
}
#endif
