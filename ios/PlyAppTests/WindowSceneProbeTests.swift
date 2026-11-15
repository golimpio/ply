import SwiftUI
import UIKit
import XCTest
@testable import Ply

/// A window says it is gone only when its scene disconnects. A full-screen player covering the window's
/// root takes the root's views out of the window, and taking that for the window closing ended the play
/// the player had just started: an empty player whose play button did nothing.
nonisolated final class WindowSceneProbeTests: XCTestCase {
    @MainActor func testAFullScreenPlayerOverTheWindowIsNotTheWindowClosing() async throws {
        let probe = WindowSceneProbe()
        let center = NotificationCenter()
        probe.notifications = center
        var seen: [UIWindowScene] = []
        var gone = 0
        probe.onScene = { seen.append($0) }
        probe.onGone = { gone += 1 }
        let root = UIViewController()
        root.view.addSubview(probe)
        let window = try Self.show(root)
        defer { window.isHidden = true }
        let scene = try XCTUnwrap(window.windowScene)
        XCTAssertTrue(seen.last === scene, "the probe did not report its window's scene")

        await Self.coverWithPlayer(root)
        // Positive control: the player really took the root's views out of the window.
        XCTAssertNil(probe.window, "the full-screen player left the root in the window: this test proves nothing")
        XCTAssertEqual(gone, 0, "a full-screen player over the window was taken for the window closing")

        // The window closing while the player covers it still ends the play.
        center.post(name: UIScene.didDisconnectNotification, object: scene)
        XCTAssertEqual(gone, 1, "the window closed under the player and the probe missed it")
        await Self.uncover(root)
    }

    /// The app's own root: its window stays one a play can be shown in while the player covers it.
    @MainActor func testTheRootStaysAWindowUnderAFullScreenPlayer() async throws {
        let model = AppModel()
        let root = UIHostingController(rootView: RootView().environment(model))
        let window = try Self.show(root)
        defer { window.isHidden = true }
        try await Self.wait("the root did not register its window") { model.windowCount == 1 }

        await Self.coverWithPlayer(root)
        XCTAssertNil(root.view.window, "the full-screen player left the root in the window: this test proves nothing")
        try await Task.sleep(for: .milliseconds(300)) // SwiftUI's appearance callbacks are not synchronous
        XCTAssertEqual(model.windowCount, 1, "a full-screen player over the root was taken for its window closing")
        await Self.uncover(root)
    }

    // MARK: helpers

    @MainActor private static func show(_ root: UIViewController) throws -> UIWindow {
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.rootViewController = root
        window.makeKeyAndVisible()
        return window
    }

    /// Presents a stand-in for the player the way `PlayerPresenter` does: full screen, animated.
    @MainActor private static func coverWithPlayer(_ root: UIViewController) async {
        let player = UIViewController()
        player.modalPresentationStyle = .fullScreen
        await withCheckedContinuation { done in root.present(player, animated: true) { done.resume() } }
        await Task.yield()
    }

    @MainActor private static func uncover(_ root: UIViewController) async {
        await withCheckedContinuation { done in root.dismiss(animated: false) { done.resume() } }
    }

    @MainActor private static func wait(_ failure: String, until ok: () -> Bool) async throws {
        for _ in 0..<100 where !ok() {
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(ok(), failure)
    }
}
