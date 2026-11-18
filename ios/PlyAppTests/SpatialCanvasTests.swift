import CoreGraphics
import SwiftUI
import UIKit
import XCTest
@testable import Ply

/// Answers every request with a small PNG after a short delay (so concurrent loads overlap) and counts them.
private nonisolated final class CountingImageURLProtocol: URLProtocol {
    nonisolated(unsafe) private static var started = 0
    private static let lock = NSLock()
    /// How many loads began, read under the same lock the loader threads write under.
    static var requests: Int { lock.withLock { started } }
    private static let png: Data = UIGraphicsImageRenderer(size: CGSize(width: 8, height: 8)).pngData { ctx in
        UIColor.orange.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: 8, height: 8))
    }

    static func reset() {
        lock.withLock { started = 0 }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lock.withLock { Self.started += 1 }
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) { [self] in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1",
                                           headerFields: ["Content-Type": "image/png"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Self.png)
            client?.urlProtocolDidFinishLoading(self)
        }
    }

    override func stopLoading() {}
}

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

    /// Home's backdrop starts in the colours it last showed, so a cold launch is not a dark screen while the
    /// library and the first poster load over the network.
    func testHomePaletteSurvivesARelaunch() throws {
        let suite = "ply.tests.palette.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }

        XCTAssertEqual(PosterPalette.lastShown(in: defaults), .fallback, "A first launch starts from the fallback")
        let shown = PosterPalette(
            topLeft: PaletteColor(r: 0.4, g: 0.2, b: 0.1),
            topRight: PaletteColor(r: 0.3, g: 0.3, b: 0.1),
            bottomLeft: PaletteColor(r: 0.1, g: 0.1, b: 0.2),
            bottomRight: PaletteColor(r: 0.5, g: 0.2, b: 0.05),
            center: PaletteColor(r: 0.2, g: 0.15, b: 0.1)
        )
        shown.remember(in: defaults)
        XCTAssertEqual(PosterPalette.lastShown(in: defaults), shown, "The next launch starts in the colours shown last")
    }

    /// The poster Home shows and the one its backdrop samples are one download: the palette waits on the card's
    /// own load instead of fetching the same file again beside it.
    @MainActor
    func testPaletteSamplingSharesThePosterDownload() async throws {
        CountingImageURLProtocol.reset()
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [CountingImageURLProtocol.self]
        let pipeline = ImagePipeline(session: URLSession(configuration: config))
        let url = try XCTUnwrap(URL(string: "http://127.0.0.1:1/art/poster.png"))

        async let shown = pipeline.image(for: url)
        async let sampled = pipeline.cgImage(for: url)
        let (image, cgImage) = await (shown, sampled)

        XCTAssertNotNil(image, "Positive control: the card's image loaded")
        XCTAssertNotNil(cgImage, "Positive control: the palette's image loaded")
        XCTAssertEqual(CountingImageURLProtocol.requests, 1, "One poster, one download")
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
            from: Data(#"{"id":10,"kind":"movie","title":"Air","year":2023,"durationSec":6840,"backdrop":"/art/hero-film.png"}"#.utf8)
        )
        let episode = try JSONDecoder().decode(
            Item.self,
            from: Data(#"{"id":20,"kind":"episode","title":"The Yankee Dodge","show":"The Artful Dodger","season":1,"episode":1,"durationSec":2940,"still":"/art/hero-ep.png"}"#.utf8)
        )
        let collection = try JSONDecoder().decode(
            CollectionDetail.self,
            from: Data(#"{"id":5,"name":"Alice in Wonderland Collection","overview":"Wonderland.","backdrop":"/art/hero-col.png","titles":[]}"#.utf8)
        )

        let renderer = UIGraphicsImageRenderer(size: CGSize(width: 40, height: 40))
        let brightArt = renderer.image { ctx in
            UIColor(red: 0.95, green: 0.45, blue: 0.15, alpha: 1.0).setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 40, height: 40))
        }
        for path in ["/art/hero-film.png", "/art/hero-ep.png", "/art/hero-col.png"] {
            if let url = connection.api.url(path) {
                ImagePipeline.shared.seed(brightArt, for: url)
            }
        }
        defer { ImagePipeline.shared.removeAll() }

        let cases: [(name: String, view: AnyView)] = [
            ("Film", AnyView(ItemDetailView(id: movie.id, seedItem: movie))),
            ("Episode", AnyView(ItemDetailView(id: episode.id, seedItem: episode))),
            ("Series", AnyView(ShowView(name: "The Artful Dodger"))),
            ("Collection", AnyView(CollectionView(id: collection.id, name: collection.name, seedCollection: collection)))
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
            if entry.name == "Collection" {
                XCTAssertTrue((detailVC.navigationItem.title ?? "").isEmpty, "Collection detail must not repeat its title in the navigation bar")
            }
            let minY = try XCTUnwrap(observedMinY, "\(entry.name): HeroBackdropArtwork did not report its minY")
            XCTAssertLessThanOrEqual(minY, 1.0, "\(entry.name): HeroBackdropArtwork stopped below top edge at y=\(minY)")

            // Rendered pixels inside the top safe area (well above navBar.maxY) must not be clipped to black (#000).
            if entry.name != "Series" {
                window.layoutIfNeeded()
                let snap = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
                    window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
                }
                if let cg = snap.cgImage,
                   let data = cg.dataProvider?.data,
                   let ptr = CFDataGetBytePtr(data) {
                    let bytesPerPixel = cg.bitsPerPixel / 8
                    let sampleX = Int(Double(cg.width) * 0.5)
                    let sampleY = min(Int(10.0 * snap.scale), cg.height - 1)
                    let offset = sampleY * cg.bytesPerRow + sampleX * bytesPerPixel
                    let maxChannel = max(ptr[offset], max(ptr[offset + 1], ptr[offset + 2]))
                    XCTAssertGreaterThan(maxChannel, 8, "\(entry.name): top safe-area pixels at y=10pt were clipped black")
                }
            }
        }
    }
    #endif
}
