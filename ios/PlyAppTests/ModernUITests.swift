import SwiftUI
import UIKit
import XCTest
@testable import Ply

private nonisolated final class StubImageURLProtocol: URLProtocol {
    nonisolated(unsafe) static var imageData: Data = Data()
    nonisolated(unsafe) static var hitCount = 0

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.hitCount += 1
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.imageData)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

nonisolated final class ModernUITests: XCTestCase {
    private static func makeTestPNGData() throws -> Data {
        let renderer = UIGraphicsImageRenderer(size: CGSize(width: 24, height: 24))
        let img = renderer.image { ctx in
            UIColor.orange.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 24, height: 24))
        }
        return try XCTUnwrap(img.pngData())
    }

    @MainActor
    func testImagePipelineCachesDecodedImagesAndCoalescesRequests() async throws {
        StubImageURLProtocol.imageData = try Self.makeTestPNGData()
        StubImageURLProtocol.hitCount = 0

        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubImageURLProtocol.self]
        let pipeline = ImagePipeline(session: URLSession(configuration: config))
        let url = try XCTUnwrap(URL(string: "http://ply.test/art/1.png"))

        // Positive control: uncached URL is nil synchronously and triggers one network fetch.
        XCTAssertNil(pipeline.cachedImage(for: url))
        let first = await pipeline.image(for: url)
        XCTAssertNotNil(first)
        XCTAssertEqual(StubImageURLProtocol.hitCount, 1)

        // Subsequent reads and CGImage extraction hit the memory cache with zero network requests.
        XCTAssertNotNil(pipeline.cachedImage(for: url))
        let second = await pipeline.image(for: url)
        XCTAssertNotNil(second)
        let cg = await pipeline.cgImage(for: url)
        XCTAssertNotNil(cg)
        XCTAssertEqual(StubImageURLProtocol.hitCount, 1)
    }

    func testFastScrollMetricsHandlesAnyGridHeightAndClampsProgress() {
        let nonScrollable = FastScrollMetrics(offsetY: 0, contentHeight: 600, containerHeight: 600)
        XCTAssertFalse(nonScrollable.isScrollable)
        XCTAssertEqual(nonScrollable.progress, 0)

        let mid = FastScrollMetrics(offsetY: 500, contentHeight: 1600, containerHeight: 600)
        XCTAssertTrue(mid.isScrollable)
        XCTAssertEqual(mid.progress, 0.5, accuracy: 0.001)
        XCTAssertEqual(mid.itemIndex(count: 101), 50)

        let overscrolledTop = FastScrollMetrics(offsetY: -80, contentHeight: 1600, containerHeight: 600)
        XCTAssertEqual(overscrolledTop.progress, 0)

        let overscrolledBottom = FastScrollMetrics(offsetY: 1400, contentHeight: 1600, containerHeight: 600)
        XCTAssertEqual(overscrolledBottom.progress, 1)
    }

    func testFastScrollMetricsTrackMathAndRevealOnlyOnScroll() {
        XCTAssertGreaterThanOrEqual(FastScrollMetrics.thumbHitSize.width, 44)
        XCTAssertGreaterThanOrEqual(FastScrollMetrics.thumbHitSize.height, 44)

        // Track math maps progress [0, 1] across [topInset, containerHeight - bottomInset] and clamps outside bounds.
        let containerHeight: CGFloat = 790
        let track = FastScrollMetrics.trackHeight(containerHeight: containerHeight)
        XCTAssertEqual(track, 600, accuracy: 0.001)
        XCTAssertEqual(FastScrollMetrics.thumbCenterY(progress: 0, containerHeight: containerHeight), 80, accuracy: 0.001)
        XCTAssertEqual(FastScrollMetrics.thumbCenterY(progress: 0.5, containerHeight: containerHeight), 380, accuracy: 0.001)
        XCTAssertEqual(FastScrollMetrics.thumbCenterY(progress: 1, containerHeight: containerHeight), 680, accuracy: 0.001)

        XCTAssertEqual(FastScrollMetrics.progress(forLocationY: 20, containerHeight: containerHeight), 0, accuracy: 0.001)
        XCTAssertEqual(FastScrollMetrics.progress(forLocationY: 380, containerHeight: containerHeight), 0.5, accuracy: 0.001)
        XCTAssertEqual(FastScrollMetrics.progress(forLocationY: 900, containerHeight: containerHeight), 1, accuracy: 0.001)

        XCTAssertEqual(FastScrollMetrics.clampedIndex(progress: 0.999, count: 100), 99)
        XCTAssertEqual(FastScrollMetrics.positionText(index: 0, count: 1840), "1 / 1,840")
        XCTAssertEqual(FastScrollMetrics.positionText(index: 641, count: 1840), "642 / 1,840")
        XCTAssertEqual(FastScrollMetrics.positionText(index: 0, count: 0), "0 / 0")

        // The thumb pill must remain hidden before scrolling, on short lists, and on sub-pixel jitter.
        let scrollable = FastScrollMetrics(offsetY: 24, contentHeight: 2000, containerHeight: 700)
        XCTAssertFalse(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 24, newMetrics: scrollable, itemCount: 50, isDragging: false))
        XCTAssertFalse(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 23.7, newMetrics: scrollable, itemCount: 50, isDragging: false))
        XCTAssertFalse(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 0, newMetrics: scrollable, itemCount: 7, isDragging: false))
        XCTAssertFalse(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 0, newMetrics: scrollable, itemCount: 50, isDragging: true))

        let shortList = FastScrollMetrics(offsetY: 12, contentHeight: 720, containerHeight: 700)
        XCTAssertFalse(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 0, newMetrics: shortList, itemCount: 50, isDragging: false))

        // Reveals only once actual scrolling occurs on a scrollable catalog.
        XCTAssertTrue(FastScrollMetrics.shouldRevealOnScroll(oldOffsetY: 0, newMetrics: scrollable, itemCount: 50, isDragging: false))
    }

    #if os(iOS)
    @MainActor
    func testMainTabsHostsNativeUITabBarControllerInDarkMode() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-2",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let defaults = UserDefaults.standard
        let priorShowLabels = defaults.object(forKey: PlyPreferences.showTabLabelsKey)
        defaults.set(false, forKey: PlyPreferences.showTabLabelsKey)
        defer {
            if let priorShowLabels {
                defaults.set(priorShowLabels, forKey: PlyPreferences.showTabLabelsKey)
            } else {
                defaults.removeObject(forKey: PlyPreferences.showTabLabelsKey)
            }
        }

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        let host = UIHostingController(
            rootView: MainTabs()
                .preferredColorScheme(.dark)
                .environment(connection)
                .environment(model)
                .environment(playbackScene)
        )
        window.rootViewController = host
        window.makeKeyAndVisible()

        func findTabBarController(from vc: UIViewController) -> UITabBarController? {
            if let tab = vc as? UITabBarController { return tab }
            for child in vc.children {
                if let tab = findTabBarController(from: child) { return tab }
            }
            return nil
        }

        var tabBarController: UITabBarController?
        for _ in 0..<100 {
            window.layoutIfNeeded()
            if let found = findTabBarController(from: host),
               !found.tabs.isEmpty || !(found.viewControllers ?? []).isEmpty {
                tabBarController = found
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }

        let tabVC = try XCTUnwrap(tabBarController, "iOS MainTabs should host native UITabBarController")
        let tabCount = !tabVC.tabs.isEmpty ? tabVC.tabs.count : (tabVC.viewControllers?.count ?? 0)
        XCTAssertEqual(tabCount, 5, "Expected Home, Films, Series, Settings, and Search tabs")
        if !tabVC.tabs.isEmpty {
            XCTAssertTrue(tabVC.tabs.contains { $0 is UISearchTab }, "Expected native UISearchTab for Tab(role: .search)")
            XCTAssertTrue(tabVC.tabs.allSatisfy { $0.image != nil }, "All tabs should carry rendered custom PlyTabIcon images")
            XCTAssertTrue(tabVC.tabs.allSatisfy { $0.title.isEmpty }, "Tab bar labels should be hidden when ply.showTabLabels is false")

            defaults.set(true, forKey: PlyPreferences.showTabLabelsKey)
            var showedTitles = false
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if tabVC.tabs.allSatisfy({ !$0.title.isEmpty }) {
                    showedTitles = true
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }
            XCTAssertTrue(showedTitles, "Toggling ply.showTabLabels to true should display visible tab labels")
        }
        XCTAssertEqual(host.traitCollection.userInterfaceStyle, .dark)
    }

    @MainActor
    func testMoviesAndShowsDoNotAttachSearchControllerOnIOS() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-no-search",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        func findNavController(from vc: UIViewController) -> UINavigationController? {
            if let nav = vc as? UINavigationController { return nav }
            for child in vc.children {
                if let nav = findNavController(from: child) { return nav }
            }
            return nil
        }

        for (label, view) in [("Movies", AnyView(MoviesView())), ("Shows", AnyView(ShowsView()))] {
            let host = UIHostingController(
                rootView: view
                    .environment(connection)
                    .environment(model)
                    .environment(playbackScene)
            )
            window.rootViewController = host
            window.makeKeyAndVisible()

            var nav: UINavigationController?
            var trailingCount = 0
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let found = findNavController(from: host), let item = found.topViewController?.navigationItem {
                    nav = found
                    trailingCount = (item.rightBarButtonItems?.count ?? 0) + item.trailingItemGroups.flatMap(\.barButtonItems).count
                    if trailingCount > 0 { break }
                }
                try await Task.sleep(for: .milliseconds(20))
            }

            let resolvedNav = try XCTUnwrap(nav, "\(label) should host UINavigationController")
            let topItem = try XCTUnwrap(resolvedNav.topViewController?.navigationItem)
            XCTAssertNil(topItem.searchController, "\(label) must not attach a search bar when Search tab is dedicated")
            XCTAssertGreaterThan(trailingCount, 0, "\(label) should keep Filter in topBarTrailing")
        }
    }
    #endif
}
