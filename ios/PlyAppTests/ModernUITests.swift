import AVKit
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
            XCTAssertTrue(tabVC.tabs.allSatisfy { $0.image?.isSymbolImage == true }, "All tabs should render native SF Symbols")
            XCTAssertEqual(AppTab.home.systemImage, "house")
            XCTAssertEqual(AppTab.films.systemImage, "video")
            XCTAssertEqual(AppTab.series.systemImage, "tv")
            XCTAssertEqual(AppTab.settings.systemImage, "switch.2")
            XCTAssertEqual(AppTab.search.systemImage, "magnifyingglass")
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
                    if trailingCount >= 2 { break }
                }
                try await Task.sleep(for: .milliseconds(20))
            }

            let resolvedNav = try XCTUnwrap(nav, "\(label) should host UINavigationController")
            let topItem = try XCTUnwrap(resolvedNav.topViewController?.navigationItem)
            XCTAssertNil(topItem.searchController, "\(label) must not attach a search bar when Search tab is dedicated")
            XCTAssertEqual(trailingCount, 2, "\(label) should host Collections (star.square.on.square) left of Filter in topBarTrailing")
        }
    }

    @MainActor
    func testLibraryFilterPillsUseUnifiedHeightAndHideUnselectedFilters() throws {
        // Every control pill (Sort, Active filter chip, Reset icon) must share the exact same height.
        let sortSize = UIHostingController(
            rootView: Label(MovieSort.rating.rawValue, systemImage: "arrow.up.arrow.down")
                .plyControlPill(highlighted: true)
        ).sizeThatFits(in: CGSize(width: 400, height: 200))

        let activeChipSize = UIHostingController(
            rootView: ActiveFilterChip(title: "2010s") {}
        ).sizeThatFits(in: CGSize(width: 400, height: 200))

        let resetSize = UIHostingController(
            rootView: Image(systemName: "arrow.counterclockwise")
                .plyControlPill(iconOnly: true)
        ).sizeThatFits(in: CGSize(width: 400, height: 200))

        XCTAssertEqual(sortSize.height, PlyDesign.filterPillHeight, accuracy: 0.5)
        XCTAssertEqual(activeChipSize.height, PlyDesign.filterPillHeight, accuracy: 0.5)
        XCTAssertEqual(resetSize.height, PlyDesign.filterPillHeight, accuracy: 0.5)
        XCTAssertEqual(sortSize.height, activeChipSize.height, accuracy: 0.5)
        XCTAssertEqual(activeChipSize.height, resetSize.height, accuracy: 0.5)

        // With default sort and no active filters, only the Sort pill renders (no unselected "Unwatched" or reset pill).
        let defaultBarWidth = UIHostingController(
            rootView: LibraryFilterBar(sort: .constant(MovieSort.title), activeFilters: [], onReset: {}).pillsContent
        ).sizeThatFits(in: CGSize(width: 1000, height: 200)).width

        // Switching only sort to non-default adds the Reset icon pill (wider bar).
        let sortedBarWidth = UIHostingController(
            rootView: LibraryFilterBar(sort: .constant(MovieSort.rating), activeFilters: [], onReset: {}).pillsContent
        ).sizeThatFits(in: CGSize(width: 1000, height: 200)).width
        XCTAssertGreaterThan(sortedBarWidth, defaultBarWidth, "Non-default sort should reveal the Reset icon pill")

        // Adding an active filter adds both the removable pill and Reset icon.
        let filteredBarWidth = UIHostingController(
            rootView: LibraryFilterBar(
                sort: .constant(MovieSort.title),
                activeFilters: [LibraryActiveFilter(id: "unwatched", title: "Unwatched") {}],
                onReset: {}
            ).pillsContent
        ).sizeThatFits(in: CGSize(width: 1000, height: 200)).width
        XCTAssertGreaterThan(filteredBarWidth, sortedBarWidth)
    }

    @MainActor
    func testPersistedNonDefaultSortActivatesFilterIconTintInMoviesAndShows() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-filter-tint",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let defaults = UserDefaults.standard
        let keys = [
            PlyPreferences.Movies.sortKey, PlyPreferences.Movies.genreKey,
            PlyPreferences.Movies.decadeKey, PlyPreferences.Movies.only4KKey,
            PlyPreferences.Movies.onlyHDRKey, PlyPreferences.Movies.onlyUnwatchedKey,
            PlyPreferences.Shows.sortKey, PlyPreferences.Shows.decadeKey,
            PlyPreferences.Shows.onlyUnwatchedKey
        ]
        let saved = Dictionary(uniqueKeysWithValues: keys.compactMap { k in defaults.object(forKey: k).map { (k, $0) } })
        defer {
            for k in keys {
                if let val = saved[k] { defaults.set(val, forKey: k) } else { defaults.removeObject(forKey: k) }
            }
        }

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        func trailingAccentPixelCount(from view: AnyView) async throws -> Int {
            let host = UIHostingController(
                rootView: view
                    .preferredColorScheme(.dark)
                    .environment(connection)
                    .environment(model)
                    .environment(playbackScene)
            )
            window.rootViewController = host
            window.makeKeyAndVisible()

            func findNav(_ vc: UIViewController) -> UINavigationController? {
                if let nav = vc as? UINavigationController { return nav }
                return vc.children.lazy.compactMap(findNav).first
            }

            var resolvedNav: UINavigationController?
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let nav = findNav(host), let item = nav.topViewController?.navigationItem {
                    let count = (item.rightBarButtonItems?.count ?? 0) + item.trailingItemGroups.flatMap(\.barButtonItems).count
                    if count >= 2 {
                        resolvedNav = nav
                        break
                    }
                }
                try await Task.sleep(for: .milliseconds(20))
            }

            let nav = try XCTUnwrap(resolvedNav)
            try await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            let bar = nav.navigationBar
            let barFrameInWindow = bar.convert(bar.bounds, to: window)
            let snap = UIGraphicsImageRenderer(bounds: barFrameInWindow).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            guard let cg = snap.cgImage else { return 0 }
            let width = cg.width
            let height = cg.height
            var bytes = [UInt8](repeating: 0, count: width * height * 4)
            guard let ctx = CGContext(
                data: &bytes,
                width: width,
                height: height,
                bitsPerComponent: 8,
                bytesPerRow: width * 4,
                space: CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
            ) else { return 0 }
            ctx.draw(cg, in: CGRect(x: 0, y: 0, width: width, height: height))

            let minX = Int(Double(width) * 0.72)
            var count = 0
            for y in 0..<height {
                for x in minX..<width {
                    let o = (y * width + x) * 4
                    let r = bytes[o], g = bytes[o + 1], b = bytes[o + 2]
                    if r > 150 && g > 70 && b < 95 && Int(r) - Int(b) > 75 {
                        count += 1
                    }
                }
            }
            return count
        }

        // Positive control: default sort keeps top-right Filter icon neutral (no amber pixels).
        defaults.removeObject(forKey: PlyPreferences.Shows.sortKey)
        defaults.removeObject(forKey: PlyPreferences.Shows.decadeKey)
        defaults.removeObject(forKey: PlyPreferences.Shows.onlyUnwatchedKey)
        let defaultAccentPixels = try await trailingAccentPixelCount(from: AnyView(ShowsView()))
        XCTAssertEqual(defaultAccentPixels, 0, "Default sort should render Filter icon neutral without amber pixels")

        // Persisted non-default Sort (Rating) must turn the top-right Filter icon amber on fresh mount.
        defaults.set(ShowSort.rating.rawValue, forKey: PlyPreferences.Shows.sortKey)
        defaults.set(MovieSort.rating.rawValue, forKey: PlyPreferences.Movies.sortKey)

        let showsAccentPixels = try await trailingAccentPixelCount(from: AnyView(ShowsView()))
        let moviesAccentPixels = try await trailingAccentPixelCount(from: AnyView(MoviesView()))

        XCTAssertGreaterThan(showsAccentPixels, 15, "Series Filter icon must turn amber when Rating sort is selected")
        XCTAssertGreaterThan(moviesAccentPixels, 15, "Films Filter icon must turn amber when Rating sort is selected")
    }

    @MainActor
    func testPeekingCanvasDeckPlacesPageIndicatorBelowPosterAndSpacesRowsEvenly() async throws {
        XCTAssertEqual(PlyDesign.deckMetadataSpacing, 12)
        XCTAssertEqual(PlyDesign.deckCardsVerticalPadding, 16)

        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-deck-layout",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let items: [Item] = try JSONDecoder().decode(
            [Item].self,
            from: Data(#"""
            [
              {"id":1,"kind":"movie","title":"You Hurt My Feelings","year":2023,"durationSec":5580,"genres":["Comedy"],"height":1080,"container":"mp4","rating":{"source":"metacritic","value":80,"scale":100}},
              {"id":2,"kind":"movie","title":"Past Lives","year":2023,"durationSec":6360,"genres":["Drama"]}
            ]
            """#.utf8)
        )

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        var frames: [String: CGRect] = [:]
        let host = UIHostingController(
            rootView: ScrollView {
                HomeView.feedLayout {
                    PeekingCanvasDeckView(items: items)
                } shelves: {
                    Shelf("Continue Watching", subtitle: "Pick up where you left off") {
                        Text("Item").frame(width: 120, height: 80)
                    }
                    .background(GeometryReader { geo in
                        Color.clear.preference(key: DeckRowFramesKey.self, value: ["firstShelf": geo.frame(in: .global)])
                    })
                }
            }
            .onPreferenceChange(DeckRowFramesKey.self) { frames = $0 }
            .environment(connection)
            .environment(model)
            .environment(playbackScene)
        )
        window.rootViewController = host
        window.makeKeyAndVisible()

        for _ in 0..<50 {
            window.layoutIfNeeded()
            if frames.count == 5 {
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }

        let indicator = try XCTUnwrap(frames["indicator"], "Expected page indicator row")
        let specs = try XCTUnwrap(frames["specs"], "Expected specs row")
        let title = try XCTUnwrap(frames["title"], "Expected title row")
        let pills = try XCTUnwrap(frames["pills"], "Expected telemetry pills row")
        let firstShelf = try XCTUnwrap(frames["firstShelf"], "Expected Continue Watching shelf row")

        XCTAssertLessThan(indicator.maxY, specs.minY, "Page indicator must sit above specs (right below poster)")
        XCTAssertLessThan(specs.maxY, title.minY, "Specs must sit above title")
        XCTAssertLessThan(title.maxY, pills.minY, "Telemetry pills must sit below title")
        XCTAssertLessThan(pills.maxY, firstShelf.minY, "First shelf must sit below telemetry pills")

        let indicatorToSpecsGap = specs.minY - indicator.maxY
        let specsToTitleGap = title.minY - specs.maxY
        let titleToPillsGap = pills.minY - title.maxY
        let pillsToShelfGap = firstShelf.minY - pills.maxY
        XCTAssertEqual(indicatorToSpecsGap, PlyDesign.deckMetadataSpacing, accuracy: 2.0)
        XCTAssertEqual(specsToTitleGap, PlyDesign.deckMetadataSpacing, accuracy: 2.0)
        XCTAssertGreaterThanOrEqual(titleToPillsGap, specsToTitleGap, "Title-to-pills gap should not be tighter than specs-to-title")
        XCTAssertEqual(pillsToShelfGap, indicatorToSpecsGap, accuracy: 2.0, "Gap between deck pills and Continue Watching must match indicator-to-specs gap")
    }

    @MainActor
    func testPlayerTrackPillMatchesGlassOverlayHeightAndClearsTrailingControls() throws {
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-player-pill",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let api = APIClient(base: base, token: nil, device: device)
        let playback = PlaybackController(api: api, request: PlayRequest(itemId: 1, startSec: 0))
        let presenter = PlayerPresenter()

        let vc = AVPlayerViewController()
        vc.loadViewIfNeeded()
        presenter.installTrackBar(playback: playback, on: vc)

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        window.rootViewController = vc
        window.makeKeyAndVisible()
        window.layoutIfNeeded()

        func findTrackPill(in view: UIView) -> UIView? {
            if view.accessibilityIdentifier == "ply.player.trackPill" { return view }
            for sub in view.subviews {
                if let hit = findTrackPill(in: sub) { return hit }
            }
            return nil
        }

        let pillView = try XCTUnwrap(findTrackPill(in: vc.view), "Expected installed track pill view")
        let pillFrame = pillView.convert(pillView.bounds, to: vc.view)
        let safeFrame = vc.view.safeAreaLayoutGuide.layoutFrame

        // Height matches standard 44pt overlay controls (GlassCloseButton / AVKit bar buttons).
        let closeHeight = UIHostingController(rootView: GlassCloseButton {}).sizeThatFits(in: CGSize(width: 200, height: 200)).height
        XCTAssertEqual(pillFrame.height, closeHeight, accuracy: 0.5)
        XCTAssertEqual(pillFrame.height, PlyDesign.iconButtonSize, accuracy: 0.5)

        // Trailing clearance gives proper separation beside AVKit's 44pt top-right Mute circle.
        let trailingInset = safeFrame.maxX - pillFrame.maxX
        XCTAssertEqual(trailingInset, PlyDesign.playerTrackTrailingOffset, accuracy: 0.5)
        XCTAssertGreaterThanOrEqual(
            trailingInset - (PlyDesign.overlayTrailingPadding + PlyDesign.iconButtonSize),
            PlyDesign.overlayButtonSpacing - 0.5
        )
    }

    @MainActor
    func testFastScrollScrubberThumbHandleIsCompactIconOnlyWithoutText() throws {
        let scrubber = FastScrollScrubber(
            itemCount: 100,
            currentIndex: 12,
            currentLabel: "2023",
            currentTitle: "Asteroid City",
            onScrub: { _ in },
            isVisible: .constant(true),
            isDragging: .constant(false),
            progress: .constant(0.25)
        )

        let handleHost = UIHostingController(rootView: scrubber.thumbHandle)
        let size = handleHost.sizeThatFits(in: CGSize(width: 200, height: 200))
        XCTAssertEqual(size.width, 34, accuracy: 0.5)
        XCTAssertEqual(size.height, 38, accuracy: 0.5)

        // Thumb handle must never contain any rendered label text ("2023") — only arrow icons.
        let labels = handleHost.view.accessibilityElements?.compactMap { ($0 as AnyObject).accessibilityLabel ?? nil } ?? []
        XCTAssertFalse(labels.contains { $0.contains("2023") })
        func findAnyLabelText(in view: UIView) -> [String] {
            var texts: [String] = []
            if let label = view as? UILabel, let t = label.text, !t.isEmpty { texts.append(t) }
            if let acc = view.accessibilityLabel, !acc.isEmpty { texts.append(acc) }
            for sub in view.subviews { texts.append(contentsOf: findAnyLabelText(in: sub)) }
            return texts
        }
        let found = findAnyLabelText(in: handleHost.view)
        XCTAssertFalse(found.contains { $0.contains("2023") })
    }
    #endif
}
