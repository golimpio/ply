import AVKit
import SwiftUI
import UIKit
import XCTest
@testable import Ply

/// The Search tab's page keeps the main pages' header: the same ply wordmark in the same top-left slot,
/// and a recent-searches block whose rows share one vertical rhythm and the page's content edges.
#if os(iOS)
/// Answers every request with an empty JSON list (or a route's own body), so a hosted screen renders its
/// real, connected state: no "could not be loaded" banners in the way of the layout under test.
private nonisolated final class EmptyListURLProtocol: URLProtocol {
    nonisolated(unsafe) static var routes: [String: Data] = [:]

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: 200,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.routes[request.url?.path ?? ""] ?? Data("[]".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

nonisolated final class SearchHeaderLayoutTests: XCTestCase {
    /// A window on the app's own scene, so every measurement is in the same coordinates the screen uses.
    @MainActor
    private func window<V: View>(_ view: V) throws -> UIWindow {
        let scene = try XCTUnwrap(
            UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        )
        let window = UIWindow(windowScene: scene)
        window.rootViewController = UIHostingController(rootView: view.preferredColorScheme(.dark))
        window.makeKeyAndVisible()
        return window
    }

    /// Waits for the page to finish loading and settle, so nothing moves under the measurement.
    @MainActor
    private func settle(_ window: UIWindow, until ready: () -> Bool, timeout: TimeInterval = 3) {
        let deadline = Date().addingTimeInterval(timeout)
        var satisfied = false
        while Date() < deadline {
            window.layoutIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.02))
            if !satisfied, ready() {
                RunLoop.current.run(until: Date().addingTimeInterval(0.25))
                window.layoutIfNeeded()
                satisfied = true
            }
            if satisfied { break }
        }
    }

    @MainActor
    private func searchEnvironment() throws -> (Connection, AppModel, PlaybackScene) {
        let info = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-search-header",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        return (
            Connection(base: base, info: info, api: APIClient(base: base, token: nil, device: device)),
            AppModel(),
            PlaybackScene(presenter: PlayerPresenter())
        )
    }

    /// The Search tab with the given recent searches already recorded, as it looks with a server.
    @MainActor
    private func searchTab(recent: [String]) throws -> some View {
        let (connection, model, playback) = try searchEnvironment()
        UserDefaults.standard.set(recent, forKey: SearchHistory.defaultKey)
        URLProtocol.registerClass(EmptyListURLProtocol.self)
        return SearchTabView()
            .environment(connection)
            .environment(model)
            .environment(playback)
    }

    /// Hosts `view` and returns the frames its `reportSearchFrame` calls publish.
    @MainActor
    private func frames<V: View>(for view: V, needs: [String]) throws -> [String: CGRect] {
        var collected: [String: CGRect] = [:]
        let window = try window(
            view.onPreferenceChange(SearchLayoutFramesKey.self) { collected = $0 }
        )
        defer { window.isHidden = true }

        settle(window) { needs.allSatisfy { collected[$0] != nil } }
        UserDefaults.standard.removeObject(forKey: SearchHistory.defaultKey)
        URLProtocol.unregisterClass(EmptyListURLProtocol.self)
        return collected
    }

    /// Renders the Search tab at the screen's own scale, with the frames its `reportSearchFrame` calls
    /// publish in that same window.
    @MainActor
    private func render(recent: [String]) throws -> (image: UIImage, frames: [String: CGRect]) {
        var collected: [String: CGRect] = [:]
        let window = try window(
            try searchTab(recent: recent).onPreferenceChange(SearchLayoutFramesKey.self) { collected = $0 }
        )
        defer { window.isHidden = true }

        settle(window) { recent.isEmpty || collected["recentHeaderText"] != nil }
        UserDefaults.standard.removeObject(forKey: SearchHistory.defaultKey)
        URLProtocol.unregisterClass(EmptyListURLProtocol.self)

        let renderer = UIGraphicsImageRenderer(bounds: window.bounds)
        let image = renderer.image { _ in
            window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
        }
        return (image, collected)
    }

    /// The render's pixels, top row first, with the scale from points to pixels.
    @MainActor
    private func rasterise(_ image: UIImage) throws -> (pixels: [UInt8], width: Int, height: Int, scale: CGFloat) {
        let cgImage = try XCTUnwrap(image.cgImage)
        let width = cgImage.width
        let height = cgImage.height

        var pixels = [UInt8](repeating: 0, count: width * height * 4)
        let context = try XCTUnwrap(CGContext(
            data: &pixels,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: width * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ))
        // A CGImage drawn into a bitmap lands top row first, which is the order this returns.
        context.draw(cgImage, in: CGRect(x: 0, y: 0, width: width, height: height))

        return (pixels, width, height, CGFloat(width) / image.size.width)
    }

    /// The bands of visible ink that cross `columns`, in points from the top of the render.
    ///
    /// The rhythm is measured on ink rather than on text boxes: a caption and a subheadline have
    /// different leading, so equal box gaps are not equal visible gaps.
    @MainActor
    private func inkBands(in image: UIImage, columns: Range<CGFloat>, threshold: UInt8 = 40) throws -> [ClosedRange<CGFloat>] {
        let (pixels, width, height, scale) = try rasterise(image)
        let first = Int(columns.lowerBound * scale)
        let last = min(width, Int(columns.upperBound * scale))
        var bands: [ClosedRange<CGFloat>] = []
        var start: Int?
        for y in 0..<height {
            var lit = false
            for x in first..<last where !lit {
                let index = (y * width + x) * 4
                lit = pixels[index] > threshold
                    || pixels[index + 1] > threshold
                    || pixels[index + 2] > threshold
            }
            if lit, start == nil { start = y }
            if !lit, let began = start {
                bands.append(CGFloat(began) / scale ... CGFloat(y - 1) / scale)
                start = nil
            }
        }
        if let began = start {
            bands.append(CGFloat(began) / scale ... CGFloat(height - 1) / scale)
        }
        return bands
    }

    /// The rightmost inked x inside a horizontal band, in points: the ink edge of whatever ends there.
    @MainActor
    private func rightInkEdge(
        in image: UIImage,
        band: ClosedRange<CGFloat>,
        columns: Range<CGFloat>,
        threshold: UInt8 = 40
    ) throws -> CGFloat {
        let (pixels, width, height, scale) = try rasterise(image)
        let first = Int(columns.lowerBound * scale)
        let last = min(width, Int(columns.upperBound * scale))
        let top = max(0, Int(band.lowerBound * scale))
        let bottom = min(height - 1, Int(band.upperBound * scale))

        var rightmost = first
        for y in top...bottom {
            for x in first..<last {
                let index = (y * width + x) * 4
                if pixels[index] > threshold || pixels[index + 1] > threshold || pixels[index + 2] > threshold {
                    rightmost = max(rightmost, x)
                }
            }
        }
        return CGFloat(rightmost) / scale
    }

    /// The Search page's logo must sit exactly where `plyMainPageLogo` puts it on every main page.
    @MainActor
    func testSearchLogoMatchesMainPageToolbarLogo() throws {
        let mainPage = try frames(for: NavigationStack { Color.black.plyMainPageLogo() }, needs: ["navLogo"])
        let navLogo = try XCTUnwrap(
            mainPage["navLogo"],
            "Expected the main-page header logo in the top-left toolbar slot"
        )

        let search = try frames(for: try searchTab(recent: []), needs: ["navLogo"])
        let logo = try XCTUnwrap(search["navLogo"], "Expected the ply logo on the Search page")

        XCTAssertEqual(logo.minX, navLogo.minX, accuracy: 0.5,
                       "Search logo must start on the main pages' leading edge")
        XCTAssertEqual(logo.minY, navLogo.minY, accuracy: 0.5,
                       "Search logo must sit on the main pages' top edge")
        XCTAssertEqual(logo.width, navLogo.width, accuracy: 0.5,
                       "Search logo must be the main pages' size")
        XCTAssertEqual(logo.height, navLogo.height, accuracy: 0.5)
    }

    /// Search's result groups sit on Home's shelf rhythm: a section gap between groups, and the title a
    /// header gap above its rows, so titles never crowd the results around them.
    @MainActor
    func testSearchResultGroupsUseTheShelfRhythm() async throws {
        let (environment, model, playback) = try searchEnvironment()
        EmptyListURLProtocol.routes = [
            "/api/items": Data(#"[{"id":1,"kind":"movie","title":"Dune","year":2021,"durationSec":9300},{"id":2,"kind":"movie","title":"Dune: Part Two","year":2024,"durationSec":9960}]"#.utf8),
            "/api/shows": Data(#"[{"show":"Dune: Prophecy","episodeCount":6}]"#.utf8)
        ]
        defer { EmptyListURLProtocol.routes = [:] }
        // The stub answers this connection's own session only, not every request in the test process.
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [EmptyListURLProtocol.self]
        let connection = Connection(
            base: environment.base,
            info: environment.info,
            api: APIClient(
                base: environment.base,
                token: nil,
                device: DeviceContext(
                    id: "test-device-search-rhythm",
                    registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
                    nextAttempt: { 1 }
                ),
                session: URLSession(configuration: config)
            )
        )
        await connection.library.reload()
        XCTAssertFalse(connection.library.movies.isEmpty, "Precondition: the library has films to find")

        var collected: [String: CGRect] = [:]
        let hosted = try window(
            SearchTabView(initialQuery: "dune")
                .environment(connection).environment(model).environment(playback)
                .onPreferenceChange(SearchLayoutFramesKey.self) { collected = $0 }
        )
        defer { hosted.isHidden = true }
        let needs = ["section:Films", "section:Series", "sectionTitle:Series", "sectionRows:Series"]
        settle(hosted) { needs.allSatisfy { collected[$0] != nil } }

        let films = try XCTUnwrap(collected["section:Films"], "Expected the Films group")
        let series = try XCTUnwrap(collected["section:Series"], "Expected the Series group")
        let title = try XCTUnwrap(collected["sectionTitle:Series"])
        let rows = try XCTUnwrap(collected["sectionRows:Series"])

        XCTAssertEqual(series.minY - films.maxY, PlyDesign.sectionSpacing, accuracy: 1,
                       "Groups must be a section gap apart (got \(series.minY - films.maxY))")
        XCTAssertEqual(rows.minY - title.maxY, PlyDesign.sectionHeaderSpacing, accuracy: 1,
                       "A title must sit a header gap above its rows (got \(rows.minY - title.maxY))")
    }

    /// The Recent Searches title's ink band and its two rows' bands under it. The title is found by its own
    /// frame: the system draws the logo and the search field elsewhere on the page, above or below it.
    @MainActor
    private func recentSearchBands(in image: UIImage, frames: [String: CGRect]) throws -> [ClosedRange<CGFloat>] {
        let header = try XCTUnwrap(frames["recentHeaderText"], "Expected the Recent Searches title")
        // A column on the leading edge holding the title and the rows' clocks.
        let bands = try inkBands(in: image, columns: 16..<200)
        let title = try XCTUnwrap(
            bands.firstIndex { $0.contains(header.midY) },
            "Expected ink on the Recent Searches title at \(header), got \(bands)"
        )
        return try XCTUnwrap(
            bands.count >= title + 3 ? Array(bands[title..<title + 3]) : nil,
            "Expected the Recent Searches title and its two results, got \(bands)"
        )
    }

    /// The gap under "Recent Searches" matches the gap between the results below it.
    @MainActor
    func testRecentSearchesShareOneVerticalRhythm() throws {
        let (image, frames) = try render(recent: ["test", "asa"])
        let block = try recentSearchBands(in: image, frames: frames)

        let headerToFirst = block[1].lowerBound - block[0].upperBound
        let betweenResults = block[2].lowerBound - block[1].upperBound

        // A caption's line box is a little shorter than a subheadline's, so the gap under the title can
        // sit up to ~2pt above the row gap; the ink is what the eye compares, and it matches.
        XCTAssertEqual(headerToFirst, betweenResults, accuracy: 2.5,
                       "The gap from Recent Searches to the first result (\(headerToFirst)) must match the gap between results (\(betweenResults))")
    }

    /// The recent searches start on the logo's leading edge, and Clear and the rows' delete affordances
    /// end on the page's trailing content edge, the mirror of it.
    @MainActor
    func testRecentSearchesAlignToContentEdges() throws {
        let frames = try frames(
            for: try searchTab(recent: ["test", "asa"]),
            needs: ["navLogo", "recentHeaderText", "recentClear", "recentRemoveGlyph"]
        )
        let logo = try XCTUnwrap(frames["navLogo"])
        let header = try XCTUnwrap(frames["recentHeaderText"])
        let clear = try XCTUnwrap(frames["recentClear"])
        let delete = try XCTUnwrap(frames["recentRemoveGlyph"], "Expected a delete affordance on the recent searches")
        let contentEdge = try screenWidth() - PlyDesign.contentHorizontalPadding

        XCTAssertEqual(header.minX, logo.minX, accuracy: 1.0,
                       "Recent Searches must start on the logo's leading edge")
        XCTAssertEqual(clear.maxX, contentEdge, accuracy: 1.0,
                       "Clear must end on the trailing content edge")
        XCTAssertEqual(delete.maxX, contentEdge, accuracy: 1.5,
                       "The remove affordance must end on the trailing content edge")
    }

    /// The recent searches end on one right edge with Clear, and that edge is the page's content edge:
    /// measured on the rendered ink, where the icon's side bearing showed up as a visible inset.
    @MainActor
    func testRecentSearchRowsEndOnTheContentEdge() throws {
        let (image, frames) = try render(recent: ["test", "asa"])
        let block = try recentSearchBands(in: image, frames: frames)

        let rightHalf = 200..<image.size.width
        let clearEdge = try rightInkEdge(in: image, band: block[0], columns: rightHalf)
        let firstRowEdge = try rightInkEdge(in: image, band: block[1], columns: rightHalf)
        let lastRowEdge = try rightInkEdge(in: image, band: block[2], columns: rightHalf)
        let contentEdge = image.size.width - PlyDesign.contentHorizontalPadding

        XCTAssertEqual(firstRowEdge, clearEdge, accuracy: 0.6,
                       "The delete icon must end where Clear ends (icon \(firstRowEdge), Clear \(clearEdge))")
        XCTAssertEqual(lastRowEdge, clearEdge, accuracy: 0.6,
                       "Every row's delete icon must end where Clear ends (icon \(lastRowEdge), Clear \(clearEdge))")
        XCTAssertEqual(clearEdge, contentEdge, accuracy: 1.5,
                       "Clear must end on the content edge (Clear \(clearEdge), edge \(contentEdge))")
    }

    /// `delete.left` is the row's delete affordance, and it must resolve on this OS.
    @MainActor
    func testRecentSearchRemoveButtonUsesDeleteLeftSymbol() throws {
        XCTAssertEqual(RecentSearchRemoveButton.systemImage, "delete.left",
                       "The row's delete affordance is SF Symbol delete.left, not a bare xmark")
        XCTAssertNotNil(UIImage(systemName: RecentSearchRemoveButton.systemImage),
                        "\(RecentSearchRemoveButton.systemImage) must be an available SF Symbol")
    }

    /// The search field has no cancel button, the round X at the bar's trailing end: the previous tab's button
    /// at the leading end already leaves search, so the X only repeated it.
    @MainActor
    func testSearchFieldHasNoCancelButton() throws {
        let window = try window(try searchTab(recent: []))
        defer {
            window.isHidden = true
            UserDefaults.standard.removeObject(forKey: SearchHistory.defaultKey)
            URLProtocol.unregisterClass(EmptyListURLProtocol.self)
        }
        var search: UISearchController?
        settle(window) {
            search = Self.searchControllers(in: window.rootViewController).first
            return search != nil
        }
        let controller = try XCTUnwrap(search, "The Search tab installs the system search controller")

        // Searching is when the X would show: an inactive search bar never has one.
        controller.searchBar.searchTextField.becomeFirstResponder()
        settle(window) { controller.isActive }
        XCTAssertTrue(controller.isActive, "Precondition: search is open")
        XCTAssertFalse(controller.automaticallyShowsCancelButton, "The search field must not show its cancel X")
        XCTAssertFalse(controller.searchBar.showsCancelButton, "The open search bar must not show its cancel X")
    }

    /// Every search controller in the navigation items under `controller`.
    @MainActor
    private static func searchControllers(in controller: UIViewController?) -> [UISearchController] {
        guard let controller else { return [] }
        return [controller.navigationItem.searchController].compactMap { $0 }
            + controller.children.flatMap { searchControllers(in: $0) }
    }

    /// The width of the screen the hosted windows fill.
    @MainActor
    private func screenWidth() throws -> CGFloat {
        try XCTUnwrap(
            UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        ).screen.bounds.width
    }

    /// The ply logo is at the same X and Y on every screen that draws it, the Search tab included: every
    /// main page puts it in the navigation bar's leading slot with `plyMainPageLogo`.
    @MainActor
    func testHeaderLogoIsInTheSamePlaceOnEveryScreen() async throws {
        let (connection, model, playback) = try searchEnvironment()
        UserDefaults.standard.set(["test"], forKey: SearchHistory.defaultKey)
        URLProtocol.registerClass(EmptyListURLProtocol.self)
        defer {
            URLProtocol.unregisterClass(EmptyListURLProtocol.self)
            UserDefaults.standard.removeObject(forKey: SearchHistory.defaultKey)
        }
        await connection.library.reload()
        await connection.browse.load()

        func logo<V: View>(_ screen: V, key: String) throws -> CGRect {
            var collected: [String: CGRect] = [:]
            let hosted = try window(
                screen
                    .environment(connection)
                    .environment(model)
                    .environment(playback)
                    .onPreferenceChange(SearchLayoutFramesKey.self) { collected = $0 }
            )
            defer { hosted.isHidden = true }
            settle(hosted) { collected[key] != nil }
            return try XCTUnwrap(collected[key], "Expected the ply logo on this screen")
        }

        let home = try logo(HomeView(), key: "navLogo")
        let films = try logo(MoviesView(), key: "navLogo")
        let series = try logo(ShowsView(), key: "navLogo")
        let settings = try logo(SettingsView(), key: "navLogo")
        let search = try logo(SearchTabView(), key: "navLogo")

        for (name, frame) in [("Home", home), ("Films", films), ("Series", series), ("Settings", settings)] {
            XCTAssertEqual(frame.minX, home.minX, accuracy: 0.5,
                           "\(name)'s logo must start on Home's X (home \(home), \(name) \(frame))")
            XCTAssertEqual(frame.minY, home.minY, accuracy: 0.5,
                           "\(name)'s logo must sit on Home's Y (home \(home), \(name) \(frame))")
        }
        XCTAssertEqual(search.minX, home.minX, accuracy: 0.5,
                       "Search's logo must start on the pages' X (home \(home), search \(search))")
        XCTAssertEqual(search.minY, home.minY, accuracy: 0.5,
                       "Search's logo must sit on the pages' Y (home \(home), search \(search))")
        XCTAssertEqual(search.width, home.width, accuracy: 0.5, "Search's logo must be the pages' size")
        XCTAssertEqual(search.height, home.height, accuracy: 0.5, "Search's logo must be the pages' size")
    }
}
#endif
