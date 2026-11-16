import CoreGraphics
import SwiftUI
import UIKit
import XCTest
@testable import Ply

nonisolated final class SpatialCanvasTests: XCTestCase {
    func testTonedColorClampsBrightWhiteAndPreservesChroma() {
        let white = PosterPaletteExtractor.tonedColor(red: 255, green: 255, blue: 255)
        XCTAssertLessThanOrEqual(white.r, 0.46)
        XCTAssertLessThanOrEqual(white.g, 0.46)
        XCTAssertLessThanOrEqual(white.b, 0.46)

        let crimson = PosterPaletteExtractor.tonedColor(red: 160, green: 24, blue: 24)
        XCTAssertGreaterThan(crimson.r, crimson.g * 4)
        XCTAssertGreaterThan(crimson.r, crimson.b * 4)
    }

    func testExtractFromQuadrantsReadsCorrectOrientation() throws {
        let size = 40
        let colorSpace = CGColorSpaceCreateDeviceRGB()
        let bitmapInfo = CGImageAlphaInfo.premultipliedLast.rawValue
        guard let ctx = CGContext(
            data: nil,
            width: size,
            height: size,
            bitsPerComponent: 8,
            bytesPerRow: size * 4,
            space: colorSpace,
            bitmapInfo: bitmapInfo
        ) else {
            XCTFail("Could not create test CGContext")
            return
        }

        // CoreGraphics origin (0,0) is bottom-left:
        // Top-left (y >= 20, x < 20): Red
        ctx.setFillColor(red: 1, green: 0, blue: 0, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 20, width: 20, height: 20))
        // Top-right (y >= 20, x >= 20): Green
        ctx.setFillColor(red: 0, green: 1, blue: 0, alpha: 1)
        ctx.fill(CGRect(x: 20, y: 20, width: 20, height: 20))
        // Bottom-left (y < 20, x < 20): Blue
        ctx.setFillColor(red: 0, green: 0, blue: 1, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 0, width: 20, height: 20))
        // Bottom-right (y < 20, x >= 20): Warm Amber
        ctx.setFillColor(red: 1, green: 0.5, blue: 0, alpha: 1)
        ctx.fill(CGRect(x: 20, y: 0, width: 20, height: 20))

        let image = try XCTUnwrap(ctx.makeImage())
        let palette = PosterPaletteExtractor.extract(from: image)

        XCTAssertGreaterThan(palette.topLeft.r, palette.topLeft.g * 3)
        XCTAssertGreaterThan(palette.topRight.g, palette.topRight.r * 3)
        XCTAssertGreaterThan(palette.bottomLeft.b, palette.bottomLeft.r * 3)
        XCTAssertGreaterThan(palette.bottomRight.r, palette.bottomRight.g)
        XCTAssertGreaterThan(palette.bottomRight.g, palette.bottomRight.b * 3)
    }

    @MainActor
    func testFallbackPaletteProvidesNineMeshColors() {
        XCTAssertEqual(PosterPalette.fallback.meshColors.count, 9)
    }

    @MainActor
    func testSpatialPortalStateOpenAndDismiss() throws {
        let json = Data(#"{"id":42,"kind":"movie","title":"Arrival","durationSec":6960}"#.utf8)
        let item = try JSONDecoder().decode(Item.self, from: json)
        let state = SpatialPortalState()

        XCTAssertFalse(state.isPresented)
        state.open(item)
        XCTAssertTrue(state.isPresented)
        XCTAssertEqual(state.expandedItem?.id, 42)

        state.dismiss()
        XCTAssertFalse(state.isPresented)
        XCTAssertNil(state.expandedItem)
    }

    func testWrappingFlowLayoutPreservesVariableWidthsAndWrapsRows() {
        let pillSizes = [
            CGSize(width: 90, height: 34),   // Action 115
            CGSize(width: 120, height: 34),  // Adventure 94
            CGSize(width: 110, height: 34)   // Animation 24
        ]
        // Container width 240 fits pills 1 + 2 (90 + 8 + 120 = 218), wrapping pill 3 onto row 2.
        let layout = WrappingFlowLayout.computeLayout(
            sizes: pillSizes,
            maxWidth: 240,
            spacing: 8,
            lineSpacing: 8
        )
        XCTAssertEqual(layout.frames.count, 3)
        XCTAssertEqual(layout.frames[0].origin, CGPoint(x: 0, y: 0))
        XCTAssertEqual(layout.frames[0].width, 90)
        XCTAssertEqual(layout.frames[1].origin, CGPoint(x: 98, y: 0))
        XCTAssertEqual(layout.frames[1].width, 120)
        XCTAssertEqual(layout.frames[2].origin, CGPoint(x: 0, y: 42))
        XCTAssertEqual(layout.totalSize.height, 76)
    }

    @MainActor
    func testCollectionKindFilteringSupportsServerCountsAndFallbackKinds() {
        let filmOnly = CollectionCard(id: 1, name: "Matrix", count: 3, movieCount: 3, seriesCount: 0)
        let seriesOnly = CollectionCard(id: 2, name: "Star Trek", count: 2, movieCount: 0, seriesCount: 2)
        let mixed = CollectionCard(id: 3, name: "MCU", count: 5, movieCount: 3, seriesCount: 2)
        let legacySeries = CollectionCard(id: 4, name: "Legacy Series", count: 1)

        let cards = [filmOnly, seriesOnly, mixed, legacySeries]
        let fallbackKinds: [Int64: Set<String>] = [4: ["series"]]

        let films = BrowseModel.filterCollections(cards, kindsByID: fallbackKinds, for: .films)
        XCTAssertEqual(films.map(\.id), [1, 3])

        let series = BrowseModel.filterCollections(cards, kindsByID: fallbackKinds, for: .series)
        XCTAssertEqual(series.map(\.id), [2, 3, 4])
    }

    @MainActor
    func testHeroBackdropAlphaMaskEndsTransparentAndTopVignetteDarkensTop() {
        let maskStops = HeroBackdropArtwork.bottomAlphaMaskStops
        XCTAssertGreaterThanOrEqual(maskStops.count, 3)
        XCTAssertEqual(maskStops.first?.location, 0.0)
        XCTAssertEqual(maskStops.last?.location, 1.0)

        let topAlpha = UIColor(maskStops.first!.color).cgColor.alpha
        let bottomAlpha = UIColor(maskStops.last!.color).cgColor.alpha
        XCTAssertEqual(topAlpha, 1.0, accuracy: 0.001, "Top of hero artwork must remain opaque")
        XCTAssertEqual(bottomAlpha, 0.0, accuracy: 0.001, "Bottom of hero artwork must mask to transparent to blend seamlessly into LivingMeshBackdrop")

        let topStops = HeroBackdropArtwork.topVignetteStops
        XCTAssertEqual(topStops.first?.location, 0.0)
        XCTAssertGreaterThan(UIColor(topStops.first!.color).cgColor.alpha, 0.5)
        XCTAssertEqual(UIColor(topStops.last!.color).cgColor.alpha, 0.0, accuracy: 0.001)
    }

    #if os(iOS)
    @MainActor
    func testFilmEpisodeAndShowDetailHeroExtendToTopEdgeInsideNavigationController() async throws {
        let serverInfo = try JSONDecoder().decode(
            ServerInfo.self,
            from: Data(#"{"id":"1","name":"Test","version":"1","authRequired":false,"api":1,"apiMin":1}"#.utf8)
        )
        let base = URL(string: "http://127.0.0.1:1")!
        let device = DeviceContext(
            id: "test-device-1",
            registration: DeviceRegistration(name: "Test", kind: "ios", app: "ply", version: "1"),
            nextAttempt: { 1 }
        )
        let connection = Connection(base: base, info: serverInfo, api: APIClient(base: base, token: nil, device: device))
        let model = AppModel()
        let playbackScene = PlaybackScene(presenter: PlayerPresenter())

        let movie = try JSONDecoder().decode(
            Item.self,
            from: Data(#"{"id":10,"kind":"movie","title":"Air","year":2023,"durationSec":6840}"#.utf8)
        )
        let episode = try JSONDecoder().decode(
            Item.self,
            from: Data(#"{"id":20,"kind":"episode","title":"The Yankee Dodge","show":"The Artful Dodger","season":1,"episode":1,"durationSec":2940}"#.utf8)
        )

        let cases: [(name: String, view: AnyView)] = [
            ("Film", AnyView(ItemDetailView(id: movie.id, seedItem: movie))),
            ("Episode", AnyView(ItemDetailView(id: episode.id, seedItem: episode))),
            ("Series", AnyView(ShowView(name: "The Artful Dodger"))),
            ("FilmPortal", AnyView(ItemDetailView(id: movie.id, seedItem: movie, onDismissPortal: {})))
        ]

        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }

        for entry in cases {
            var observedMinY: CGFloat?
            let root = UIViewController()
            let nav = UINavigationController(rootViewController: root)
            let detailVC = UIHostingController(
                rootView: entry.view
                    .onPreferenceChange(HeroBackdropMinYKey.self) { value in
                        if let value { observedMinY = value }
                    }
                    .environment(connection)
                    .environment(model)
                    .environment(playbackScene)
            )
            window.rootViewController = nav
            window.makeKeyAndVisible()
            nav.pushViewController(detailVC, animated: false)

            for _ in 0..<100 where observedMinY == nil {
                try await Task.sleep(for: .milliseconds(20))
            }

            // Positive control: the navigation bar really sits below the status bar (> 44pt from screen top).
            XCTAssertGreaterThan(nav.navigationBar.frame.maxY, 44, "\(entry.name): expected non-zero navigation bar height")
            let minY = try XCTUnwrap(observedMinY, "\(entry.name): HeroBackdropArtwork did not report its minY")
            XCTAssertLessThanOrEqual(minY, 1.0, "\(entry.name): HeroBackdropArtwork stopped below top edge at y=\(minY)")
        }
    }
    #endif
}
