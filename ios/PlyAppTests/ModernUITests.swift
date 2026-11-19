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

private nonisolated final class StubDetailAPIURLProtocol: URLProtocol {
    nonisolated(unsafe) static var routes: [String: Data] = [:]

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url?.path ?? ""
        let data = Self.routes[path] ?? Data("{}".utf8)
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: Self.routes[path] == nil ? 404 : 200,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
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

    /// The detail action label's one height holds the subheadline's line on both platforms: Apple TV's
    /// subheadline is far larger than iPhone's, and a height sized for the phone would leave the text
    /// spilling out of the button there.
    func testDetailActionLabelHoldsTheSubheadlineLine() {
        let large = UITraitCollection(preferredContentSizeCategory: .large)
        let line = UIFont.preferredFont(forTextStyle: .subheadline, compatibleWith: large).lineHeight
        XCTAssertGreaterThan(line, 0, "Precondition: the subheadline has a line")
        XCTAssertGreaterThanOrEqual(PlyDesign.detailActionLabelHeight, line,
                                    "The label (\(PlyDesign.detailActionLabelHeight)) holds the subheadline's line (\(line))")
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
        XCTAssertFalse(tabVC.tabs.isEmpty, "tabVC.tabs should not be empty; vcCount=\(tabVC.viewControllers?.count ?? 0)")
        func assertUnfilledTabSymbols(in tabVC: UITabBarController, labeled: Bool, context: String) throws {
            let expectedTabs: [AppTab] = [.home, .films, .series, .settings]
            XCTAssertEqual(tabVC.tabs.count, expectedTabs.count + 1, "\(context): expected ply's 4 tabs and Search")

            func collectImages(in view: UIView) -> [(UIImageView, UIImage)] {
                var images: [(UIImageView, UIImage)] = []
                if let iv = view as? UIImageView, let img = iv.image {
                    images.append((iv, img))
                }
                for sub in view.subviews {
                    images.append(contentsOf: collectImages(in: sub))
                }
                return images
            }
            let renderedTabBarImages = collectImages(in: tabVC.tabBar)

            for (tab, appTab) in zip(tabVC.tabs, expectedTabs) {
                let actualImage = try XCTUnwrap(tab.image, "\(context): Missing image for tab \(appTab.title)")
                XCTAssertEqual(actualImage.renderingMode, .alwaysTemplate, "\(context): Tab \(appTab.title) must use template rendering")
                let expectedOutline = appTab.templateIcon(labeled: labeled)
                XCTAssertEqual(
                    actualImage.pngData(),
                    expectedOutline.pngData(),
                    "\(context): Tab \(appTab.title) should render exact outline template of \(appTab.systemImage)"
                )
                let filledRef = UIImage(systemName: "\(appTab.systemImage).fill")
                let unfilledRef = UIImage(systemName: appTab.systemImage)
                if let filledRef, let unfilledRef, filledRef.pngData() != unfilledRef.pngData() {
                    let filledViews = renderedTabBarImages.filter { _, rendered in
                        guard rendered.isSymbolImage else { return false }
                        let scaledFilled = UIImage(systemName: "\(appTab.systemImage).fill")?
                            .applyingSymbolConfiguration(rendered.symbolConfiguration ?? .unspecified)
                        let scaledUnfilled = UIImage(systemName: appTab.systemImage)?
                            .applyingSymbolConfiguration(rendered.symbolConfiguration ?? .unspecified)
                        guard let sf = scaledFilled, let su = scaledUnfilled, sf.pngData() != su.pngData() else {
                            return false
                        }
                        return rendered.pngData() == sf.pngData()
                    }
                    XCTAssertTrue(
                        filledViews.isEmpty,
                        "\(context): tabBar rendered filled UIImageView for \(appTab.title)"
                    )
                }
            }
        }

        if !tabVC.tabs.isEmpty {
            // Search is the system's search tab: iOS draws it (detached or in the bar, by version) and keeps
            // it on screen when the bar minimizes. ply's four tabs keep their own outline icons.
            XCTAssertTrue(tabVC.tabs.last is UISearchTab, "Search must be the system search tab, Tab(role: .search)")
            XCTAssertEqual(tabVC.tabs.filter { $0 is UISearchTab }.count, 1, "Exactly one search tab")
            try assertUnfilledTabSymbols(in: tabVC, labeled: false, context: "Initial")
            XCTAssertEqual(AppTab.home.systemImage, "house")
            XCTAssertEqual(AppTab.films.systemImage, "video")
            XCTAssertEqual(AppTab.series.systemImage, "tv")
            XCTAssertEqual(AppTab.settings.systemImage, "switch.2")
            XCTAssertEqual(AppTab.search.systemImage, "magnifyingglass")
            XCTAssertTrue(tabVC.tabs.allSatisfy { $0.title.isEmpty }, "Tab bar labels should be hidden when ply.showTabLabels is false")

            // Select Settings before toggling labels (matching user repro).
            let settingsTab = tabVC.tabs[3]
            tabVC.selectedTab = settingsTab
            tabVC.delegate?.tabBarController?(tabVC, didSelectTab: settingsTab, previousTab: tabVC.tabs[0])
            try await Task.sleep(for: .milliseconds(80))
            window.layoutIfNeeded()

            defaults.set(true, forKey: PlyPreferences.showTabLabelsKey)
            var showedTabVC: UITabBarController?
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let refreshedTabVC = findTabBarController(from: host),
                   refreshedTabVC.tabs.count == 5,
                   refreshedTabVC.tabs.allSatisfy({ !$0.title.isEmpty }) {
                    showedTabVC = refreshedTabVC
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }
            let shownTabVC = try XCTUnwrap(showedTabVC, "Toggling ply.showTabLabels to true should display visible tab labels")
            try assertUnfilledTabSymbols(in: shownTabVC, labeled: true, context: "Labels shown")

            defaults.set(false, forKey: PlyPreferences.showTabLabelsKey)
            var hidTabVC: UITabBarController?
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let refreshedTabVC = findTabBarController(from: host),
                   refreshedTabVC.tabs.count == 5,
                   refreshedTabVC.tabs.allSatisfy({ $0.title.isEmpty }) {
                    hidTabVC = refreshedTabVC
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }
            let hiddenTabVC = try XCTUnwrap(hidTabVC, "Toggling ply.showTabLabels back to false should hide visible tab labels")
            try await Task.sleep(for: .milliseconds(80))
            window.layoutIfNeeded()
            try assertUnfilledTabSymbols(in: hiddenTabVC, labeled: false, context: "Labels hidden again while on Settings")
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

        for (label, view) in [("Films", AnyView(MoviesView())), ("Series", AnyView(ShowsView()))] {
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
            XCTAssertEqual(trailingCount, 2, "\(label) must host Collections left of Filter in topBarTrailing")
        }

        // Verify plyTrackVerticalScroll + plyLibraryFilterToolbar hides Collections when scrolled while keeping Filter pinned.
        struct ScrolledToolbarProbe: View {
            @State private var scrollOffset: CGFloat = 0
            @State private var sort: MovieSort = .title
            @State private var decade: DecadeFilter = .all

            var body: some View {
                NavigationStack {
                    ScrollView {
                        Color.clear.frame(height: 1600)
                    }
                    .plyTrackVerticalScroll($scrollOffset)
                    .plyTabBarBottomClearance()
                    .plyLibraryFilterToolbar(
                        collectionsKind: .films,
                        scrollOffset: scrollOffset,
                        sort: $sort,
                        selectedDecade: $decade,
                        attributes: [],
                        hasActiveSelection: false,
                        onReset: {}
                    )
                    .plyMainPageLogo()
                }
            }
        }
        func findScroll(in view: UIView) -> UIScrollView? {
            if let s = view as? UIScrollView { return s }
            for sub in view.subviews {
                if let hit = findScroll(in: sub) { return hit }
            }
            return nil
        }
        func barBrightPixelCount(in bar: UINavigationBar, xFractionRange: ClosedRange<Double>) -> Int {
            let barRect = bar.convert(bar.bounds, to: window)
            let snap = UIGraphicsImageRenderer(bounds: barRect).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            guard let cg = snap.cgImage else { return 0 }
            let width = cg.width, height = cg.height
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
            let minX = Int(Double(width) * xFractionRange.lowerBound)
            let maxX = min(width, Int(Double(width) * xFractionRange.upperBound))
            var count = 0
            for y in 0..<height {
                for x in minX..<maxX {
                    let o = (y * width + x) * 4
                    if bytes[o] > 180 && bytes[o + 1] > 180 && bytes[o + 2] > 180 {
                        count += 1
                    }
                }
            }
            return count
        }

        let probeHost = UIHostingController(rootView: ScrolledToolbarProbe().preferredColorScheme(.dark))
        window.rootViewController = probeHost
        window.makeKeyAndVisible()
        var probeNav: UINavigationController?
        var probeScroll: UIScrollView?
        for _ in 0..<50 {
            window.layoutIfNeeded()
            if let nav = findNavController(from: probeHost),
               (nav.topViewController?.navigationItem.trailingItemGroups.flatMap(\.barButtonItems).count ?? 0) >= 2,
               let s = findScroll(in: nav.view) {
                probeNav = nav
                probeScroll = s
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        let activeNav = try XCTUnwrap(probeNav)
        let activeScroll = try XCTUnwrap(probeScroll)
        try await Task.sleep(for: .milliseconds(60))
        window.layoutIfNeeded()

        let collectionsBefore = barBrightPixelCount(in: activeNav.navigationBar, xFractionRange: 0.50...0.80)
        let filterBefore = barBrightPixelCount(in: activeNav.navigationBar, xFractionRange: 0.82...1.0)
        XCTAssertGreaterThan(collectionsBefore, 40, "Collections text pill must render left of Filter at rest")
        XCTAssertGreaterThan(filterBefore, 20, "Filter icon must render at top-right at rest")

        activeScroll.setContentOffset(CGPoint(x: 0, y: 80), animated: false)
        try await Task.sleep(for: .milliseconds(100))
        window.layoutIfNeeded()

        let collectionsAfter = barBrightPixelCount(in: activeNav.navigationBar, xFractionRange: 0.50...0.80)
        let filterAfter = barBrightPixelCount(in: activeNav.navigationBar, xFractionRange: 0.82...1.0)
        XCTAssertEqual(collectionsAfter, 0, "Collections button must disappear when scrolling up")
        XCTAssertGreaterThan(filterAfter, 20, "Filter button must stay pinned when scrolling up")
    }

    @MainActor
    func testLibraryFilterPillsUseUnifiedHeightAndHideUnselectedFilters() throws {
        // Filter button width must be at least a 44×44 circle, matching Collections button height.
        let filterSize = UIHostingController(
            rootView: FilterButtonLabel(hasActive: false)
        ).sizeThatFits(in: CGSize(width: 200, height: 200))
        XCTAssertGreaterThanOrEqual(filterSize.width, filterSize.height - 0.5, "Filter button width must be at least a circle")
        XCTAssertEqual(filterSize.width, PlyDesign.iconButtonSize, accuracy: 0.5)
        XCTAssertEqual(filterSize.height, PlyDesign.iconButtonSize, accuracy: 0.5)

        let collectionsTopSize = UIHostingController(
            rootView: LibraryCollectionsToolbarButton(kind: .films, scrollOffset: 0)
        ).sizeThatFits(in: CGSize(width: 400, height: 200))
        XCTAssertEqual(collectionsTopSize.height, filterSize.height, accuracy: 0.5, "Collections button height must match Filter button height")

        // Every filter bar control pill (Sort, Active filter chip, Reset icon) must share the exact same height.
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
            rootView: LibraryFilterBar(collectionsKind: .films, sort: .constant(MovieSort.title), activeFilters: [], onReset: {}).pillsContent
        ).sizeThatFits(in: CGSize(width: 1000, height: 200)).width

        // Switching only sort to non-default adds the Reset icon pill (wider bar).
        let sortedBarWidth = UIHostingController(
            rootView: LibraryFilterBar(collectionsKind: .films, sort: .constant(MovieSort.rating), activeFilters: [], onReset: {}).pillsContent
        ).sizeThatFits(in: CGSize(width: 1000, height: 200)).width
        XCTAssertGreaterThan(sortedBarWidth, defaultBarWidth, "Non-default sort should reveal the Reset icon pill")

        // Adding an active filter adds both the removable pill and Reset icon.
        let filteredBarWidth = UIHostingController(
            rootView: LibraryFilterBar(
                collectionsKind: .films,
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
                    if count >= 1 {
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

    /// Home's centre poster (`HeroItemRoute`) opens the same film page every other film link does: a push of
    /// `ItemDetailView` onto Home's own navigation stack, with the back button and the tab bar, not a separate
    /// screen laid over Home.
    @MainActor
    func testHomeCentrePosterPushesTheSharedFilmPage() async throws {
        // The film's details never arrive (no `/api/items/7`): only the item the route carries can fill the page.
        StubDetailAPIURLProtocol.routes = ["/api/items": Data("[]".utf8)]
        defer { StubDetailAPIURLProtocol.routes = [:] }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubDetailAPIURLProtocol.self]
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-hero-push",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(
            base: base,
            info: serverInfo,
            api: APIClient(base: base, token: nil, device: device, session: URLSession(configuration: config))
        )

        /// Home's stack as the poster leaves it: its root, with the poster's route pushed on top.
        struct HomeStackProbe: View {
            static let willow = try! JSONDecoder().decode(
                Item.self,
                from: Data(#"{"id":7,"kind":"movie","title":"Willow","year":1988,"durationSec":7560}"#.utf8)
            )
            @Namespace private var zoom
            @State var path = NavigationPath([HeroItemRoute(item: willow)])
            var body: some View {
                NavigationStack(path: $path) {
                    Color.black
                        .plyDestinations()
                        .plyHeroDestination(zoomingFrom: zoom)
                }
            }
        }

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        var frames: [String: CGRect] = [:]
        let host = UIHostingController(
            rootView: HomeStackProbe()
                .onPreferenceChange(DetailLayoutFramesKey.self) { frames = $0 }
                .preferredColorScheme(.dark)
                .environment(connection)
                .environment(AppModel())
                .environment(PlaybackScene(presenter: PlayerPresenter()))
        )
        window.rootViewController = host
        window.makeKeyAndVisible()

        func findNav(_ vc: UIViewController) -> UINavigationController? {
            if let nav = vc as? UINavigationController { return nav }
            for child in vc.children {
                if let hit = findNav(child) { return hit }
            }
            return nil
        }
        var pushed: UINavigationController?
        for _ in 0..<150 {
            window.layoutIfNeeded()
            if let nav = findNav(host), nav.viewControllers.count == 2, frames["heroFacts"] != nil {
                pushed = nav
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        let nav = try XCTUnwrap(pushed, "The poster's route must push the shared film page onto Home's stack")
        XCTAssertNotNil(frames["heroFacts"], "The page shows the poster's film at once, before its details load")
        XCTAssertNotNil(nav.navigationBar.backItem, "The film page has a back button to Home, not a close button")
    }

    @MainActor
    func testPeekingCanvasDeckPlacesPageIndicatorBelowPosterAndSpacesRowsEvenly() async throws {
        XCTAssertEqual(PlyDesign.deckMetadataSpacing, 12)
        XCTAssertEqual(PlyDesign.deckSectionGap, 34)
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
        XCTAssertEqual(indicatorToSpecsGap, PlyDesign.deckSectionGap, accuracy: 2.0)
        XCTAssertEqual(specsToTitleGap, PlyDesign.deckMetadataSpacing, accuracy: 2.0)
        XCTAssertGreaterThanOrEqual(titleToPillsGap, specsToTitleGap, "Title-to-pills gap should not be tighter than specs-to-title")
        XCTAssertEqual(pillsToShelfGap, indicatorToSpecsGap, accuracy: 2.0, "Gap between deck pills and Continue Watching must match the larger indicator-to-specs gap")
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

    @MainActor
    func testCollectionsScrollsTitleAndDisablesTopEdgeBlurAcrossViews() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-collections-blur",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        func find<T: UIView>(_ type: T.Type, in view: UIView) -> T? {
            if let match = view as? T { return match }
            for sub in view.subviews {
                if let hit = find(type, in: sub) { return hit }
            }
            return nil
        }

        func findNav(from vc: UIViewController) -> UINavigationController? {
            if let nav = vc as? UINavigationController { return nav }
            for child in vc.children {
                if let hit = findNav(from: child) { return hit }
            }
            return nil
        }

        func centerBarBrightPixels(in bar: UINavigationBar, xFractionRange: ClosedRange<Double>) -> Int {
            let barRect = bar.convert(bar.bounds, to: window)
            let snap = UIGraphicsImageRenderer(bounds: barRect).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            guard let cg = snap.cgImage else { return 0 }
            let width = cg.width, height = cg.height
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
            let minX = Int(Double(width) * xFractionRange.lowerBound)
            let maxX = min(width, Int(Double(width) * xFractionRange.upperBound))
            var count = 0
            for y in 0..<height {
                for x in minX..<maxX {
                    let o = (y * width + x) * 4
                    if bytes[o] > 180 && bytes[o + 1] > 180 && bytes[o + 2] > 180 {
                        count += 1
                    }
                }
            }
            return count
        }

        // 1. Both Films and Series Collections push AllCollectionsView with centered Collections on the Back row and disabled top blur.
        for kind in [CollectionKindFilter.films, .series] {
            let pathHost = UIHostingController(
                rootView: NavigationStack(path: .constant([Route.collections(kind)])) {
                    Text(kind.rawValue)
                        .plyDestinations()
                }
                .preferredColorScheme(.dark)
                .environment(connection)
                .environment(model)
                .environment(playbackScene)
            )
            window.rootViewController = pathHost
            window.makeKeyAndVisible()

            var collectionsNav: UINavigationController?
            var collectionsScroll: UIScrollView?
            for _ in 0..<60 {
                window.layoutIfNeeded()
                if let nav = findNav(from: pathHost),
                   nav.viewControllers.count >= 2,
                   let scroll = nav.topViewController.flatMap({ find(UIScrollView.self, in: $0.view) }) {
                    collectionsNav = nav
                    collectionsScroll = scroll
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }

            let nav = try XCTUnwrap(collectionsNav, "Expected pushed AllCollectionsView (\(kind.rawValue))")
            let topScroll = try XCTUnwrap(collectionsScroll, "Expected vertical UIScrollView inside AllCollectionsView (\(kind.rawValue))")
            let topNavItem = try XCTUnwrap(nav.topViewController?.navigationItem)
            XCTAssertTrue((topNavItem.title ?? "").isEmpty, "Collections title must use scroll-away principal item instead of static UIKit title")
            XCTAssertNotNil(nav.navigationBar.backItem, "Back button must stay static in top-left navigation bar")
            XCTAssertTrue(topScroll.topEdgeEffect.isHidden, "AllCollectionsView must disable top blur edge effect")
            try await Task.sleep(for: .milliseconds(60))
            window.layoutIfNeeded()
            XCTAssertGreaterThan(
                centerBarBrightPixels(in: nav.navigationBar, xFractionRange: 0.30...0.70),
                20,
                "\(kind.rawValue) Collections title must render centered in the Back button row at rest"
            )
        }

        // 2. Verify plyScrollAwayInlineTitle scrolls Collections out while keeping Back pinned.
        struct ScrollAwayTitleProbe: View {
            @State private var scrollOffset: CGFloat = 0

            var body: some View {
                NavigationStack(path: .constant(["collections"])) {
                    Text("Root")
                        .navigationDestination(for: String.self) { _ in
                            ScrollView {
                                Color.clear.frame(height: 1600)
                            }
                            .plyTrackVerticalScroll($scrollOffset)
                            .plyTabBarBottomClearance()
                            .plyScrollAwayInlineTitle("Collections", scrollOffset: scrollOffset)
                        }
                }
            }
        }
        let titleProbeHost = UIHostingController(rootView: ScrollAwayTitleProbe().preferredColorScheme(.dark))
        window.rootViewController = titleProbeHost
        window.makeKeyAndVisible()
        var probeNav: UINavigationController?
        var probeScroll: UIScrollView?
        for _ in 0..<60 {
            window.layoutIfNeeded()
            if let nav = findNav(from: titleProbeHost),
               nav.viewControllers.count >= 2,
               let s = nav.topViewController.flatMap({ find(UIScrollView.self, in: $0.view) }) {
                probeNav = nav
                probeScroll = s
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        let activeNav = try XCTUnwrap(probeNav)
        let activeScroll = try XCTUnwrap(probeScroll)
        try await Task.sleep(for: .milliseconds(60))
        window.layoutIfNeeded()
        XCTAssertGreaterThan(centerBarBrightPixels(in: activeNav.navigationBar, xFractionRange: 0.30...0.70), 20, "Title must appear centered at rest")
        let backBefore = centerBarBrightPixels(in: activeNav.navigationBar, xFractionRange: 0.0...0.25)
        XCTAssertGreaterThan(backBefore, 10, "Back button must appear at top-left")

        activeScroll.setContentOffset(CGPoint(x: 0, y: 80), animated: false)
        try await Task.sleep(for: .milliseconds(100))
        window.layoutIfNeeded()
        XCTAssertEqual(centerBarBrightPixels(in: activeNav.navigationBar, xFractionRange: 0.30...0.70), 0, "Collections title must disappear when scrolling up")
        XCTAssertGreaterThan(centerBarBrightPixels(in: activeNav.navigationBar, xFractionRange: 0.0...0.25), 10, "Back button must remain pinned after scrolling")

        // 2. Other scrollable views (Genre, Settings) also disable the top blur header consistently.
        let screens: [(String, AnyView)] = [
            ("Genre", AnyView(NavigationStack { GenreView(id: 1, name: "Drama") })),
            ("Settings", AnyView(SettingsView()))
        ]
        for (name, view) in screens {
            let host = UIHostingController(
                rootView: view
                    .preferredColorScheme(.dark)
                    .environment(connection)
                    .environment(model)
                    .environment(playbackScene)
            )
            window.rootViewController = host
            window.makeKeyAndVisible()

            var foundScroll: UIScrollView?
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let scroll = find(UIScrollView.self, in: host.view), scroll.topEdgeEffect.isHidden {
                    foundScroll = scroll
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }
            let scroll = try XCTUnwrap(foundScroll, "\(name) should disable top blur edge effect")
            XCTAssertTrue(scroll.topEdgeEffect.isHidden, "\(name) topEdgeEffect must be hidden")
        }
    }

    @MainActor
    func testMainTabsSearchTabFocusesSubmitsAndReturnsToThePreviousTab() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-search-overlay",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        var frames: [String: CGRect] = [:]
        let host = UIHostingController(
            rootView: MainTabs()
                .onPreferenceChange(SearchLayoutFramesKey.self) { frames = $0 }
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

        func collectTextAndLabels(in view: UIView) -> [String] {
            var values: [String] = []
            if let label = view as? UILabel, let text = label.text, !text.isEmpty { values.append(text) }
            if let tf = view as? UITextField, let placeholder = tf.placeholder, !placeholder.isEmpty { values.append(placeholder) }
            if let acc = view.accessibilityLabel, !acc.isEmpty { values.append(acc) }
            for element in view.accessibilityElements ?? [] {
                if let label = (element as AnyObject).accessibilityLabel, let text = label, !text.isEmpty {
                    values.append(text)
                }
            }
            for sub in view.subviews { values.append(contentsOf: collectTextAndLabels(in: sub)) }
            return values
        }

        var tabBarController: UITabBarController?
        for _ in 0..<80 {
            window.layoutIfNeeded()
            if let found = findTabBarController(from: host), found.tabs.count == 5 {
                tabBarController = found
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }

        let tabVC = try XCTUnwrap(tabBarController, "Expected 5 standard tabs")
        XCTAssertTrue(tabVC.tabs[4] is UISearchTab, "Search must be the system search tab, Tab(role: .search)")
        let homeTab = tabVC.tabs[0]
        let filmsTab = tabVC.tabs[1]
        let seriesTab = tabVC.tabs[2]
        let searchTab = tabVC.tabs[4]

        func findSearchTextField(in view: UIView) -> UITextField? {
            if let tf = view as? UITextField, tf.placeholder == "Titles, series, episodes, people" {
                return tf
            }
            for sub in view.subviews {
                if let hit = findSearchTextField(in: sub) { return hit }
            }
            return nil
        }

        // Home, Films, and Series must never show a persistent search bar.
        tabVC.selectedTab = homeTab
        tabVC.delegate?.tabBarController?(tabVC, didSelectTab: homeTab, previousTab: nil)
        try await Task.sleep(for: .milliseconds(100))
        window.layoutIfNeeded()
        XCTAssertNil(findSearchTextField(in: host.view), "Home tab must not render a search bar")

        tabVC.selectedTab = seriesTab
        tabVC.delegate?.tabBarController?(tabVC, didSelectTab: seriesTab, previousTab: homeTab)
        try await Task.sleep(for: .milliseconds(100))
        window.layoutIfNeeded()
        XCTAssertNil(findSearchTextField(in: host.view), "Series tab must not render a search bar")

        // Start on Films tab before opening search; Films must also have no search bar.
        tabVC.selectedTab = filmsTab
        tabVC.delegate?.tabBarController?(tabVC, didSelectTab: filmsTab, previousTab: seriesTab)
        try await Task.sleep(for: .milliseconds(150))
        window.layoutIfNeeded()
        XCTAssertTrue(tabVC.selectedTab === filmsTab, "Precondition: Films tab should be selected")
        XCTAssertNil(findSearchTextField(in: host.view), "Films tab must not render a search bar")
        let logoOnFilms = try XCTUnwrap(frames["navLogo"], "Expected the ply logo on Films")

        // Tapping/selecting the search tab focuses the search field automatically and keeps empty state clean.
        tabVC.selectedTab = searchTab
        tabVC.delegate?.tabBarController?(tabVC, didSelectTab: searchTab, previousTab: filmsTab)

        var searchField: UITextField?
        for _ in 0..<60 {
            window.layoutIfNeeded()
            if let tf = findSearchTextField(in: host.view), tf.isFirstResponder {
                searchField = tf
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }

        let focusedField = try XCTUnwrap(searchField, "Selecting Search tab must focus the search field automatically")
        XCTAssertTrue(focusedField.isFirstResponder, "Search field must immediately become first responder to show the keyboard")

        let strings = collectTextAndLabels(in: host.view)
        XCTAssertFalse(strings.contains("Genres"), "Genres must not appear in redesigned search")
        XCTAssertFalse(strings.contains("No genres yet"), "Genres empty state must not appear in redesigned search")
        XCTAssertFalse(strings.contains("People"), "People button must not appear when search is empty")
        XCTAssertFalse(strings.contains("Collections"), "Collections button must not appear when search is empty")

        // Typing shows the scope picker beside the logo, which stays where it is on every page; submitting
        // records the search.
        let defaults = UserDefaults.standard
        let priorHistory = defaults.object(forKey: SearchHistory.defaultKey)
        defaults.removeObject(forKey: SearchHistory.defaultKey)
        defer {
            if let priorHistory {
                defaults.set(priorHistory, forKey: SearchHistory.defaultKey)
            } else {
                defaults.removeObject(forKey: SearchHistory.defaultKey)
            }
        }
        XCTAssertNil(frames["scopePicker"], "Only the logo while the field is empty")
        focusedField.insertText("dune")
        for _ in 0..<60 where frames["scopePicker"] == nil || frames["scope:\(SearchScope.collections.rawValue)"] == nil {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(20))
        }
        let picker = try XCTUnwrap(frames["scopePicker"], "Typing must show the scope buttons")
        let logo = try XCTUnwrap(frames["navLogo"], "The ply logo must stay on screen while searching")
        XCTAssertEqual(logo, logoOnFilms, "The logo must stay in the main pages' slot while searching")
        XCTAssertGreaterThan(picker.minX, logo.maxX, "The scope buttons sit to the right of the logo")
        XCTAssertLessThanOrEqual(picker.maxX, window.bounds.maxX - PlyDesign.contentHorizontalPadding + 1,
                                 "The scope buttons fit inside the screen's content edge")

        // One 44pt glass circle per scope, in order, evenly spaced, on the logo's row.
        let circles = try SearchScope.allCases.map { scope in
            try XCTUnwrap(frames["scope:\(scope.rawValue)"], "Expected a circle for \(scope.rawValue)")
        }
        for (scope, circle) in zip(SearchScope.allCases, circles) {
            XCTAssertEqual(circle.width, PlyDesign.iconButtonSize, accuracy: 0.5, "\(scope.rawValue) is a 44pt circle")
            XCTAssertEqual(circle.height, PlyDesign.iconButtonSize, accuracy: 0.5, "\(scope.rawValue) is a 44pt circle")
            XCTAssertEqual(circle.midY, logo.midY, accuracy: 4, "\(scope.rawValue) shares the logo's row")
        }
        for (left, right) in zip(circles, circles.dropFirst()) {
            XCTAssertEqual(right.minX - left.maxX, PlyDesign.glassCircleGroupSpacing, accuracy: 0.5,
                           "The circles are evenly spaced")
        }

        // Each circle is its own glass: the bar's shared capsule must not wrap the row a second time.
        func navigationBars(in view: UIView) -> [UINavigationBar] {
            (view as? UINavigationBar).map { [$0] } ?? view.subviews.flatMap(navigationBars)
        }
        let pickerCentre = CGPoint(x: picker.midX, y: picker.midY)
        let scopeItem = navigationBars(in: window)
            .compactMap(\.topItem)
            .flatMap { ($0.rightBarButtonItems ?? []) + $0.trailingItemGroups.flatMap(\.barButtonItems) }
            .first { item in
                guard let custom = item.customView, custom.window != nil else { return false }
                return custom.convert(custom.bounds, to: nil).contains(pickerCentre)
            }
        XCTAssertNotNil(scopeItem, "Positive control: the scope buttons are a bar item")
        XCTAssertEqual(scopeItem?.hidesSharedBackground, true, "The scope buttons must not sit in a second capsule")

        XCTAssertTrue(SearchHistory.load().isEmpty, "Precondition: no recent searches before submitting")
        // A typed return goes through the keyboard's own path to `.onSubmit(of: .search)`.
        focusedField.insertText("\n")
        for _ in 0..<40 {
            if SearchHistory.load().first == "dune" { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertEqual(SearchHistory.load().first, "dune", "Submitting the search must record it as a recent search")

        // Dismissing search (the system's close button beside the field) returns to the tab it was opened
        // from, with the keyboard gone.
        let searchContainer = try XCTUnwrap(focusedField.superview?.superview, "Expected the system search container")
        func closeButton(in view: UIView) -> UIButton? {
            if type(of: view) == UIButton.self, let button = view as? UIButton { return button }
            for sub in view.subviews {
                if let hit = closeButton(in: sub) { return hit }
            }
            return nil
        }
        let close = try XCTUnwrap(closeButton(in: searchContainer), "Expected the system close button beside the field")
        close.sendActions(for: .touchUpInside)
        for _ in 0..<60 {
            window.layoutIfNeeded()
            if tabVC.selectedTab === filmsTab, !focusedField.isFirstResponder { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(tabVC.selectedTab === filmsTab, "Dismissing search must return to Films, where it was opened from")
        XCTAssertFalse(focusedField.isFirstResponder, "Dismissing search must resign the keyboard")
        XCTAssertEqual(focusedField.text ?? "", "", "Dismissing search must clear the query")

        // Selecting Search tab a second time must automatically focus the field again, once the dismissal
        // has finished animating.
        for _ in 0..<15 {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(20))
        }
        tabVC.selectedTab = searchTab
        tabVC.delegate?.tabBarController?(tabVC, didSelectTab: searchTab, previousTab: filmsTab)
        var refocusedField: UITextField?
        for _ in 0..<100 {
            window.layoutIfNeeded()
            if let tf = findSearchTextField(in: host.view), tf.isFirstResponder {
                refocusedField = tf
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(refocusedField?.isFirstResponder == true, "Re-selecting Search tab must focus the search field again")
    }

    @MainActor
    func testBottomScrollClearanceAddsGenerousRoomAboveTabBarInSettingsAndScrollViews() async throws {
        XCTAssertGreaterThanOrEqual(PlyDesign.standardBottomClearance, 56)

        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-bottom-clearance",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        func findScroll(in view: UIView) -> UIScrollView? {
            if let s = view as? UIScrollView { return s }
            for sub in view.subviews {
                if let hit = findScroll(in: sub) { return hit }
            }
            return nil
        }

        func bottomContentInset(for root: AnyView) async throws -> (adjusted: CGFloat, safe: CGFloat, contentHeight: CGFloat, tabBarMinY: CGFloat?) {
            let host = UIHostingController(
                rootView: root
                    .preferredColorScheme(.dark)
                    .environment(connection)
                    .environment(model)
                    .environment(playbackScene)
            )
            window.rootViewController = host
            window.makeKeyAndVisible()

            var foundScroll: UIScrollView?
            for _ in 0..<50 {
                window.layoutIfNeeded()
                if let s = findScroll(in: host.view), s.adjustedContentInset.bottom > 0 {
                    foundScroll = s
                    break
                }
                try await Task.sleep(for: .milliseconds(20))
            }
            let scroll = try XCTUnwrap(foundScroll, "Expected UIScrollView")
            try await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            func findTabBar(in vc: UIViewController) -> UITabBar? {
                if let tb = vc as? UITabBarController { return tb.tabBar }
                for c in vc.children { if let hit = findTabBar(in: c) { return hit } }
                return nil
            }
            let tabMinY = findTabBar(in: host)?.convert(findTabBar(in: host)?.bounds ?? .zero, to: window).minY
            return (scroll.adjustedContentInset.bottom, scroll.safeAreaInsets.bottom, scroll.contentSize.height, tabMinY)
        }

        let plainForm = try await bottomContentInset(for: AnyView(TabView {
            Tab("Settings", systemImage: "switch.2") {
                NavigationStack {
                    Form { Text("Bottom") }
                }
            }
        }))
        let settings = try await bottomContentInset(for: AnyView(TabView {
            Tab("Settings", systemImage: "switch.2") {
                SettingsView()
            }
        }))
        let settingsExtra = settings.adjusted - plainForm.adjusted
        XCTAssertGreaterThanOrEqual(
            settingsExtra,
            PlyDesign.standardBottomClearance - 2.0,
            "Settings Form must add \(PlyDesign.standardBottomClearance)pt extra bottom clearance above baseline (\(plainForm) -> \(settings))"
        )
        XCTAssertEqual(settings.tabBarMinY ?? 0, plainForm.tabBarMinY ?? 0, accuracy: 0.5, "Bottom scroll clearance must not push the tab bar upward")

        let plainScroll = try await bottomContentInset(for: AnyView(TabView {
            Tab("Test", systemImage: "house") {
                ScrollView {
                    VStack {
                        Spacer(minLength: 900)
                        Text("Bottom")
                    }
                }
            }
        }))
        let paddedScroll = try await bottomContentInset(for: AnyView(TabView {
            Tab("Test", systemImage: "house") {
                ScrollView {
                    VStack {
                        Spacer(minLength: 900)
                        Text("Bottom")
                    }
                }
                .plyTabBarBottomClearance()
            }
        }))
        let scrollExtra = paddedScroll.adjusted - plainScroll.adjusted
        XCTAssertGreaterThanOrEqual(
            scrollExtra,
            PlyDesign.standardBottomClearance - 2.0,
            "ScrollView must add \(PlyDesign.standardBottomClearance)pt extra bottom clearance above baseline (\(plainScroll) -> \(paddedScroll))"
        )
        XCTAssertEqual(paddedScroll.tabBarMinY ?? 0, plainScroll.tabBarMinY ?? 0, accuracy: 0.5, "Bottom scroll clearance must not push the tab bar upward")
    }

    @MainActor
    func testFilterMenuSectionOrderAndDropdownPlacement() async throws {
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        // 1. Verify section order inside LibraryFilterDropdownMenu: Sort By (Rating) -> Attributes -> Genre -> Release Era.
        var sort: MovieSort = .title
        var genre: String? = nil
        var decade: DecadeFilter = .all
        var only4K = false
        var onlyHDR = false

        var frames: [String: CGRect] = [:]
        let menuHost = UIHostingController(
            rootView: LibraryFilterDropdownMenu(
                sort: Binding(get: { sort }, set: { sort = $0 }),
                availableGenres: ["Action", "Drama"],
                selectedGenre: Binding(get: { genre }, set: { genre = $0 }),
                selectedDecade: Binding(get: { decade }, set: { decade = $0 }),
                attributes: [
                    LibraryFilterAttribute(id: "4k", title: "4K UHD", isOn: Binding(get: { only4K }, set: { only4K = $0 })),
                    LibraryFilterAttribute(id: "hdr", title: "HDR", isOn: Binding(get: { onlyHDR }, set: { onlyHDR = $0 }))
                ],
                hasActiveSelection: false,
                onReset: {}
            )
            .onPreferenceChange(FilterMenuFramesKey.self) { frames = $0 }
            .preferredColorScheme(.dark)
        )
        window.rootViewController = menuHost
        window.makeKeyAndVisible()

        for _ in 0..<40 {
            window.layoutIfNeeded()
            if frames["Rating"] != nil && frames["Attributes"] != nil && frames["4K UHD"] != nil
                && frames["Genre"] != nil && frames["All Genres"] != nil && frames["All Eras"] != nil {
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }

        let rating = try XCTUnwrap(frames["Rating"], "Expected Rating option in Sort By section")
        let attributesHeader = try XCTUnwrap(frames["Attributes"], "Expected Attributes section header")
        let uhd = try XCTUnwrap(frames["4K UHD"], "Expected 4K UHD option in Attributes section")
        let genreHeader = try XCTUnwrap(frames["Genre"], "Expected Genre section header")
        let allGenres = try XCTUnwrap(frames["All Genres"], "Expected All Genres option in Genre section")
        let era = try XCTUnwrap(frames["All Eras"], "Expected All Eras option in Release Era section")

        XCTAssertLessThan(rating.maxY, attributesHeader.minY, "Attributes must sit below Rating")
        XCTAssertLessThan(attributesHeader.maxY, uhd.minY, "4K UHD must sit under Attributes header")
        XCTAssertLessThan(uhd.maxY, genreHeader.minY, "Attributes must sit above Genre section")
        XCTAssertLessThan(genreHeader.maxY, allGenres.minY, "All Genres must sit under Genre header")
        XCTAssertLessThan(allGenres.maxY, era.minY, "Release Era must follow Genre section")

        // 2. Verify dropdown opens strictly below the circular Filter button with a gap and trailing alignment.
        final class ToolbarProbeState: ObservableObject {
            @Published var isOpen = false
        }

        struct DropdownToolbarProbe: View {
            @State private var sort: MovieSort = .title
            @State private var decade: DecadeFilter = .all
            @State private var only4K = false
            @ObservedObject var state: ToolbarProbeState

            var body: some View {
                NavigationStack {
                    ScrollView { Color.clear.frame(height: 900) }
                        .plyLibraryFilterToolbar(
                            collectionsKind: .films,
                            scrollOffset: 0,
                            sort: $sort,
                            availableGenres: ["Action"],
                            selectedGenre: .constant(nil),
                            selectedDecade: $decade,
                            attributes: [LibraryFilterAttribute(id: "4k", title: "4K UHD", isOn: $only4K)],
                            hasActiveSelection: false,
                            isMenuOpen: $state.isOpen,
                            onReset: {}
                        )
                        .plyMainPageLogo()
                }
            }
        }

        let probeState = ToolbarProbeState()
        var toolbarFrames: [String: CGRect] = [:]
        let toolbarHost = UIHostingController(
            rootView: DropdownToolbarProbe(state: probeState)
                .onPreferenceChange(FilterMenuFramesKey.self) { toolbarFrames = $0 }
                .preferredColorScheme(.dark)
        )
        window.rootViewController = toolbarHost
        window.makeKeyAndVisible()

        for _ in 0..<50 {
            window.layoutIfNeeded()
            if toolbarFrames["filterButton"] != nil { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let buttonFrame = try XCTUnwrap(toolbarFrames["filterButton"], "Expected Filter button frame")
        XCTAssertEqual(buttonFrame.width, PlyDesign.iconButtonSize, accuracy: 1.0, "Filter button must be a 44pt circle")
        XCTAssertEqual(buttonFrame.height, PlyDesign.iconButtonSize, accuracy: 1.0, "Filter button must be a 44pt circle")

        probeState.isOpen = true
        for _ in 0..<50 {
            window.layoutIfNeeded()
            if toolbarFrames["dropdown"] != nil { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let dropdownFrame = try XCTUnwrap(toolbarFrames["dropdown"], "Expected dropdown menu frame when opened")
        XCTAssertGreaterThanOrEqual(dropdownFrame.minY, buttonFrame.maxY + 7.0, "Dropdown must open below the Filter button with a gap")
        XCTAssertFalse(dropdownFrame.intersects(buttonFrame), "Dropdown must not cover the Filter button")
        XCTAssertEqual(dropdownFrame.maxX, buttonFrame.maxX, accuracy: 2.0, "Dropdown must align trailing edge with Filter button")
    }

    @MainActor
    func testSingleIconButtonsAreCircularAndPersonAndPreviewOmitDuplicateTitles() async throws {
        // 1. Every standalone single-icon circular button uses the shared GlassCircleIconLabel with width == height.
        let standardCircles: [(String, AnyView, CGFloat)] = [
            ("Close", AnyView(GlassCloseButton {}), PlyDesign.iconButtonSize),
            ("Filter", AnyView(FilterButtonLabel(hasActive: false)), PlyDesign.iconButtonSize),
            ("AirPlay", AnyView(GlassCircleIconLabel(systemImage: "airplayvideo")), PlyDesign.iconButtonSize),
            ("ResetGlass", AnyView(GlassCircleIconLabel(systemImage: "arrow.counterclockwise", size: PlyDesign.filterPillHeight, glyphSize: 13)), PlyDesign.filterPillHeight),
            ("ResetPillIconOnly", AnyView(Image(systemName: "arrow.counterclockwise").plyControlPill(iconOnly: true)), PlyDesign.filterPillHeight)
        ]
        for (label, view, expected) in standardCircles {
            let size = UIHostingController(rootView: view).sizeThatFits(in: CGSize(width: 200, height: 200))
            XCTAssertEqual(size.width, expected, accuracy: 0.5, "\(label) width must equal \(expected)")
            XCTAssertEqual(size.height, expected, accuracy: 0.5, "\(label) height must equal \(expected)")
            XCTAssertEqual(size.width, size.height, accuracy: 0.5, "\(label) must be an exact circle")
        }

        // 2. PersonView omits the duplicate static navigation bar title while keeping Back pinned and top blur disabled.
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-person-circle",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        func findNav(from vc: UIViewController) -> UINavigationController? {
            if let nav = vc as? UINavigationController { return nav }
            for c in vc.children { if let hit = findNav(from: c) { return hit } }
            return nil
        }
        func findScroll(in view: UIView) -> UIScrollView? {
            if let s = view as? UIScrollView { return s }
            for sub in view.subviews { if let hit = findScroll(in: sub) { return hit } }
            return nil
        }

        let personHost = UIHostingController(
            rootView: NavigationStack(path: .constant([Route.person(123)])) {
                Text("Search").plyDestinations()
            }
            .preferredColorScheme(.dark)
            .environment(connection)
            .environment(model)
            .environment(playbackScene)
        )
        window.rootViewController = personHost
        window.makeKeyAndVisible()

        var personNav: UINavigationController?
        var personScroll: UIScrollView?
        for _ in 0..<60 {
            window.layoutIfNeeded()
            if let nav = findNav(from: personHost),
               nav.viewControllers.count >= 2,
               let s = nav.topViewController.flatMap({ findScroll(in: $0.view) }) {
                personNav = nav
                personScroll = s
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        let pNav = try XCTUnwrap(personNav, "Expected pushed PersonView inside NavigationStack")
        let pScroll = try XCTUnwrap(personScroll, "Expected vertical ScrollView inside PersonView")
        XCTAssertTrue((pNav.topViewController?.navigationItem.title ?? "").isEmpty, "PersonView must not duplicate person name in top navigation bar")
        XCTAssertNotNil(pNav.navigationBar.backItem, "PersonView must keep Back button pinned")
        XCTAssertTrue(pScroll.topEdgeEffect.isHidden, "PersonView must disable top blur edge effect")

        // 3. CreditPreviewSheet omits the duplicate top title while keeping its Close button and glassy sheet material.
        let entry = try JSONDecoder().decode(
            PersonFilmographyEntry.self,
            from: Data(#"{"id":1,"tmdbId":91363,"kind":"series","title":"What If...?","character":"Bruce Banner / Hulk (voice)"}"#.utf8)
        )
        let previewHost = UIHostingController(
            rootView: CreditPreviewSheet(entry: entry)
                .preferredColorScheme(.dark)
                .environment(connection)
                .environment(model)
                .environment(playbackScene)
        )
        window.rootViewController = previewHost
        window.makeKeyAndVisible()

        var sheetNav: UINavigationController?
        for _ in 0..<50 {
            window.layoutIfNeeded()
            if let nav = findNav(from: previewHost),
               (nav.topViewController?.navigationItem.rightBarButtonItems?.count ?? 0)
                + (nav.topViewController?.navigationItem.trailingItemGroups.flatMap(\.barButtonItems).count ?? 0) >= 1 {
                sheetNav = nav
                break
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        let sNav = try XCTUnwrap(sheetNav, "Expected CreditPreviewSheet NavigationStack")
        XCTAssertTrue((sNav.topViewController?.navigationItem.title ?? "").isEmpty, "CreditPreviewSheet must not duplicate title in top bar")
    }

    @MainActor
    func testTimeLeftReadsInWholeMinutesRoundedUp() {
        XCTAssertEqual(Format.timeLeft(4980), "83m left")
        XCTAssertEqual(Format.timeLeft(7380), "123m left", "Long plays stay in minutes, as on the button")
        XCTAssertEqual(Format.timeLeft(61), "2m left", "A part-minute counts as a minute")
        XCTAssertEqual(Format.timeLeft(5), "1m left", "A few seconds still read as a minute")
    }

    /// A film in progress says what is left in the line under its title, after the rating; the hero's play
    /// button resumes it, so there is no second resume button.
    @MainActor
    func testHeroFactsEndWithTimeLeftForAFilmInProgress() throws {
        let started = try JSONDecoder().decode(Item.self, from: Data(#"""
        {"id": 1, "kind": "movie", "title": "F", "year": 2023, "durationSec": 5580,
         "rating": {"source": "metacritic", "value": 80, "scale": 100},
         "progress": {"positionSec": 600, "watched": false}}
        """#.utf8))
        let fresh = try JSONDecoder().decode(Item.self, from: Data(#"""
        {"id": 2, "kind": "movie", "title": "F", "year": 2023, "durationSec": 5580,
         "rating": {"source": "metacritic", "value": 80, "scale": 100}}
        """#.utf8))
        let freshFacts = ItemDetailView.heroFacts(item: fresh, detail: nil)
        XCTAssertFalse(freshFacts.contains("left"), "A film not started has no time left (got \(freshFacts))")
        XCTAssertEqual(ItemDetailView.heroFacts(item: started, detail: nil), freshFacts + " · 83m left",
                       "A film in progress ends its facts with what is left, after the rating")
    }

    /// A film in progress offers Play from start beside Mark as watched, the two the same height and
    /// `detailActionSpacing` apart, and no progress bar or "Resume from" line under the buttons.
    @MainActor
    func testFilmInProgressOffersPlayFromStartAndNoProgressBar() async throws {
        let itemJSON = Data(#"""
        {
          "id": 202, "kind": "movie", "title": "You Hurt My Feelings", "year": 2023, "durationSec": 5580,
          "titleId": 9, "progress": {"positionSec": 600, "watched": false},
          "streams": [{"index": 0, "type": "video", "codec": "h264"}], "subtitles": []
        }
        """#.utf8)
        StubDetailAPIURLProtocol.routes = ["/api/items/202": itemJSON, "/api/items": Data("[]".utf8)]
        defer { StubDetailAPIURLProtocol.routes = [:] }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubDetailAPIURLProtocol.self]
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-resume-buttons",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(
            base: base,
            info: serverInfo,
            api: APIClient(base: base, token: nil, device: device, session: URLSession(configuration: config))
        )
        let item = try JSONDecoder().decode(Item.self, from: itemJSON)
        XCTAssertEqual(item.resumeSec, 600, "Precondition: the film is in progress")

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        var frames: [String: CGRect] = [:]
        let host = UIHostingController(
            rootView: ItemDetailView(id: 202, seedItem: item)
                .onPreferenceChange(DetailLayoutFramesKey.self) { frames = $0 }
                .environment(connection)
                .environment(AppModel())
                .environment(PlaybackScene(presenter: PlayerPresenter()))
        )
        window.rootViewController = host
        window.makeKeyAndVisible()
        let needs = ["heroFacts", "actionRow", "watchedButton", "restartButton", "detailRow.Video"]
        for _ in 0..<100 where !needs.allSatisfy({ frames[$0] != nil }) {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(25))
        }

        let watched = try XCTUnwrap(frames["watchedButton"], "Expected Mark as watched")
        let restart = try XCTUnwrap(frames["restartButton"], "A film in progress offers Play from start")
        // Buttons of one row are one height, whatever their symbol (`arrow.counterclockwise` is taller).
        XCTAssertEqual(restart.height, watched.height, accuracy: 0.5,
                       "Play from start (\(restart.height)) and Mark as watched (\(watched.height)) differ")
        XCTAssertEqual(restart.minY, watched.minY, accuracy: 0.5, "The two sit on one line")
        XCTAssertEqual(restart.minX - watched.maxX, PlyDesign.detailActionSpacing, accuracy: 0.5,
                       "The buttons are detailActionSpacing apart (got \(restart.minX - watched.maxX))")
        let facts = try XCTUnwrap(frames["heroFacts"])
        let actions = try XCTUnwrap(frames["actionRow"])
        let video = try XCTUnwrap(frames["detailRow.Video"])

        XCTAssertLessThanOrEqual(actions.maxX, window.bounds.maxX, "The buttons wrap rather than run off the screen")
        // With no progress bar under the buttons, the tracks follow them on the page's usual rhythm.
        XCTAssertEqual(video.minY - actions.maxY, actions.minY - facts.maxY, accuracy: 2,
                       "Nothing sits between the buttons and the tracks (got \(video.minY - actions.maxY))")
    }

    @MainActor
    func testDetailActionButtonsMatchTopAndBottomGapOnFilmAndSeries() async throws {
        XCTAssertEqual(PlyDesign.detailSectionSpacing, 18)
        XCTAssertEqual(PlyDesign.detailHeroBottomPadding, 10)
        XCTAssertEqual(PlyDesign.detailRowVerticalPadding, 10)

        let itemJSON = Data(#"""
        {
          "id": 101,
          "kind": "movie",
          "title": "Beau Is Afraid",
          "year": 2023,
          "durationSec": 10740,
          "height": 1080,
          "container": "mp4",
          "rating": {"source": "metacritic", "value": 63, "scale": 100},
          "streams": [
            {"index": 0, "type": "video", "codec": "h264"},
            {"index": 1, "type": "audio", "codec": "aac", "channels": 6, "label": "Unknown language · AAC · 5.1"}
          ],
          "subtitles": [],
          "meta": {
            "id": 55,
            "title": "Beau Is Afraid",
            "certification": "R",
            "overview": "A paranoid man embarks on an epic odyssey to get home to his mother."
          }
        }
        """#.utf8)
        let showJSON = Data(#"""
        {
          "show": "Severance",
          "year": 2022,
          "overview": "Mark leads a team of office workers whose memories have been surgically divided.",
          "rating": {"source": "metacritic", "value": 83, "scale": 100},
          "meta": {
            "id": 88,
            "title": "Severance",
            "certification": "TV-MA",
            "overview": "Mark leads a team of office workers whose memories have been surgically divided."
          },
          "seasons": []
        }
        """#.utf8)
        StubDetailAPIURLProtocol.routes = [
            "/api/items/101": itemJSON,
            "/api/shows/Severance": showJSON,
            "/api/items": Data("[]".utf8)
        ]
        defer { StubDetailAPIURLProtocol.routes = [:] }

        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubDetailAPIURLProtocol.self]
        let session = URLSession(configuration: config)

        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-detail-gaps",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let api = APIClient(base: base, token: nil, device: device, session: session)
        let connection = Connection(base: base, info: serverInfo, api: api)
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        // 1. Film detail page (`ItemDetailView`)
        let initialItem = try JSONDecoder().decode(Item.self, from: itemJSON)
        var filmFrames: [String: CGRect] = [:]
        let filmHost = UIHostingController(
            rootView: ItemDetailView(id: 101, seedItem: initialItem)
                .onPreferenceChange(DetailLayoutFramesKey.self) { filmFrames = $0 }
                .environment(connection)
                .environment(model)
                .environment(playbackScene)
        )
        window.rootViewController = filmHost
        window.makeKeyAndVisible()

        for _ in 0..<80 {
            window.layoutIfNeeded()
            if filmFrames["heroFacts"] != nil,
               filmFrames["actionRow"] != nil,
               filmFrames["detailRow.Video"] != nil {
                break
            }
            try await Task.sleep(for: .milliseconds(25))
        }

        let filmFacts = try XCTUnwrap(filmFrames["heroFacts"], "Expected hero facts frame on film detail")
        let filmActions = try XCTUnwrap(filmFrames["actionRow"], "Expected Mark as watched row frame on film detail")
        let filmVideo = try XCTUnwrap(filmFrames["detailRow.Video"], "Expected Video row frame on film detail")

        let filmTopGap = filmActions.minY - filmFacts.maxY
        let filmBottomGap = filmVideo.minY - filmActions.maxY
        let expectedGap = PlyDesign.detailHeroBottomPadding + PlyDesign.detailSectionSpacing
        XCTAssertEqual(filmTopGap, filmBottomGap, accuracy: 2.0,
                       "Top gap above Mark as watched (\(filmTopGap)) must equal bottom gap to Video (\(filmBottomGap))")
        XCTAssertEqual(filmTopGap, expectedGap, accuracy: 2.0,
                       "Action row should preserve the bottom gap (\(expectedGap)pt) on both sides")

        // 2. Series detail page (`ShowView`)
        var showFrames: [String: CGRect] = [:]
        let showHost = UIHostingController(
            rootView: ShowView(name: "Severance")
                .onPreferenceChange(DetailLayoutFramesKey.self) { showFrames = $0 }
                .environment(connection)
                .environment(model)
                .environment(playbackScene)
        )
        window.rootViewController = showHost
        window.makeKeyAndVisible()

        for _ in 0..<80 {
            window.layoutIfNeeded()
            if showFrames["heroFacts"] != nil,
               showFrames["actionRow"] != nil,
               showFrames["showOverview"] != nil {
                break
            }
            try await Task.sleep(for: .milliseconds(25))
        }

        let showFacts = try XCTUnwrap(showFrames["heroFacts"], "Expected hero facts frame on series detail")
        let showActions = try XCTUnwrap(showFrames["actionRow"], "Expected Mark series watched row frame on series detail")
        let showOverview = try XCTUnwrap(showFrames["showOverview"], "Expected overview frame on series detail")

        let showTopGap = showActions.minY - showFacts.maxY
        let showBottomGap = showOverview.minY - showActions.maxY
        XCTAssertEqual(showTopGap, showBottomGap, accuracy: 2.0,
                       "Top gap above Mark series watched (\(showTopGap)) must equal bottom gap to overview (\(showBottomGap))")
    }

    @MainActor
    func testSearchResultTitlesAlignToPosterRadius() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-search-header",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        // A search result row: text aligns near the top of the poster, inset by posterCornerRadius (6pt),
        // while round person rows stay vertically centered.
        var posterRowFrames: [String: CGRect] = [:]
        let rowHost = UIHostingController(
            rootView: NavigationStack {
                BrowseRow(
                    image: nil,
                    placeholder: "film",
                    title: "Always at the Carlyle",
                    subtitle: "2018 · 1h 32m",
                    route: .item(1)
                )
            }
            .onPreferenceChange(SearchLayoutFramesKey.self) { posterRowFrames = $0 }
            .preferredColorScheme(.dark)
            .environment(connection)
            .environment(model)
            .environment(playbackScene)
        )
        window.rootViewController = rowHost
        window.makeKeyAndVisible()

        for _ in 0..<50 {
            window.layoutIfNeeded()
            if posterRowFrames["rowPoster"] != nil, posterRowFrames["rowText"] != nil { break }
            try await Task.sleep(for: .milliseconds(20))
        }

        let poster = try XCTUnwrap(posterRowFrames["rowPoster"], "Expected poster frame in BrowseRow")
        let rowText = try XCTUnwrap(posterRowFrames["rowText"], "Expected text stack frame in BrowseRow")
        let topInset = rowText.minY - poster.minY
        XCTAssertEqual(topInset, BrowseRow.posterCornerRadius, accuracy: 1.0,
                       "Search result title stack must align to top of poster + corner radius (\(BrowseRow.posterCornerRadius)pt), got \(topInset)")

        var roundRowFrames: [String: CGRect] = [:]
        let roundHost = UIHostingController(
            rootView: NavigationStack {
                BrowseRow(
                    image: nil,
                    placeholder: "person.fill",
                    title: "Tilda Swinton",
                    subtitle: "Actor",
                    round: true,
                    route: .person(42)
                )
            }
            .onPreferenceChange(SearchLayoutFramesKey.self) { roundRowFrames = $0 }
            .preferredColorScheme(.dark)
            .environment(connection)
            .environment(model)
            .environment(playbackScene)
        )
        window.rootViewController = roundHost
        window.makeKeyAndVisible()

        for _ in 0..<50 {
            window.layoutIfNeeded()
            if roundRowFrames["rowPoster"] != nil, roundRowFrames["rowText"] != nil { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let roundPoster = try XCTUnwrap(roundRowFrames["rowPoster"])
        let roundText = try XCTUnwrap(roundRowFrames["rowText"])
        XCTAssertEqual(roundText.midY, roundPoster.midY, accuracy: 1.5,
                       "Circular person search rows must remain vertically centered")
    }
    #endif
}
