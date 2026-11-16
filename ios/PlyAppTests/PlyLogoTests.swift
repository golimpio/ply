import SwiftUI
import XCTest
@testable import Ply

nonisolated final class PlyLogoTests: XCTestCase {
    func testLogoShapesFillExpectedProportionsWithinBounds() {
        let rect = CGRect(x: 0, y: 0, width: 698, height: 488)
        let wordmark = PlyLogoWordmarkShape().path(in: rect)
        let play = PlyLogoPlayShape().path(in: rect)

        XCTAssertFalse(wordmark.isEmpty)
        XCTAssertFalse(play.isEmpty)

        // Wordmark spans from left edge of `p` to right edge of `y` and top of `l` to descenders.
        let wordBounds = wordmark.boundingRect
        XCTAssertEqual(wordBounds.minX, 0, accuracy: 1)
        XCTAssertGreaterThan(wordBounds.maxX, 690)
        XCTAssertEqual(wordBounds.minY, 0, accuracy: 1)
        XCTAssertEqual(wordBounds.maxY, 487, accuracy: 1)

        // Play triangle sits strictly inside the `p` bowl counter.
        let playBounds = play.boundingRect
        XCTAssertGreaterThan(playBounds.minX, 80)
        XCTAssertLessThan(playBounds.maxX, 245)
        XCTAssertGreaterThan(playBounds.minY, 145)
        XCTAssertLessThan(playBounds.maxY, 320)
        XCTAssertGreaterThan(playBounds.width, 140)
        XCTAssertGreaterThan(playBounds.height, 150)
    }

    @MainActor
    func testPlyLogoRendersOpaquePixelsAndOrangePlayMark() throws {
        let renderer = ImageRenderer(content: PlyLogo(size: 48, color: .white).background(Color.black))
        renderer.scale = 1
        let cgImage = try XCTUnwrap(renderer.cgImage)

        let width = cgImage.width
        let height = cgImage.height
        XCTAssertGreaterThan(width, 60)
        XCTAssertGreaterThan(height, 40)

        var bytes = [UInt8](repeating: 0, count: width * height * 4)
        let ctx = try XCTUnwrap(CGContext(
            data: &bytes,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: width * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ))
        ctx.draw(cgImage, in: CGRect(x: 0, y: 0, width: width, height: height))

        var whitePixels = 0
        var orangePixels = 0
        for i in stride(from: 0, to: bytes.count, by: 4) {
            let r = bytes[i], g = bytes[i + 1], b = bytes[i + 2]
            if r > 220 && g > 220 && b > 220 { whitePixels += 1 }
            if r > 200 && g > 80 && g < 180 && b < 80 { orangePixels += 1 }
        }
        XCTAssertGreaterThan(whitePixels, 100, "wordmark letters p, l, y should render in white")
        XCTAssertGreaterThan(orangePixels, 40, "play triangle inside p should render in amber-orange")
    }

    @MainActor
    func testHeroPlayButtonUsesGlassFrameAndOrangePlayTriangle() throws {
        let button = HeroPlayButton {}
        let renderer = ImageRenderer(content: button.background(Color.black))
        renderer.scale = 1
        let cgImage = try XCTUnwrap(renderer.cgImage)

        XCTAssertEqual(cgImage.width, 76)
        XCTAssertEqual(cgImage.height, 76)

        var bytes = [UInt8](repeating: 0, count: cgImage.width * cgImage.height * 4)
        let ctx = try XCTUnwrap(CGContext(
            data: &bytes,
            width: cgImage.width,
            height: cgImage.height,
            bitsPerComponent: 8,
            bytesPerRow: cgImage.width * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ))
        ctx.draw(cgImage, in: CGRect(x: 0, y: 0, width: cgImage.width, height: cgImage.height))

        var orangePixels = 0
        for i in stride(from: 0, to: bytes.count, by: 4) {
            let r = bytes[i], g = bytes[i + 1], b = bytes[i + 2]
            if r > 180 && g > 70 && g < 185 && b < 90 { orangePixels += 1 }
        }
        XCTAssertGreaterThan(orangePixels, 40, "hero play button triangle should render in amber-orange")
    }

    @MainActor
    func testGlassCloseButtonUsesStandard44PtTargetSize() throws {
        let host = UIHostingController(rootView: GlassCloseButton(accessibilityLabel: "Close details") {})
        let size = host.sizeThatFits(in: CGSize(width: 200, height: 200))
        XCTAssertEqual(size.width, 44, accuracy: 0.5)
        XCTAssertEqual(size.height, 44, accuracy: 0.5)
    }

    @MainActor
    func testGlassCircleIconLabelUsesStandard44PtTargetSize() throws {
        let host = UIHostingController(rootView: GlassCircleIconLabel(systemImage: "airplayvideo"))
        let size = host.sizeThatFits(in: CGSize(width: 200, height: 200))
        XCTAssertEqual(size.width, PlyDesign.iconButtonSize, accuracy: 0.5)
        XCTAssertEqual(size.height, PlyDesign.iconButtonSize, accuracy: 0.5)
    }

    @MainActor
    func testGlassIconButtonMatchesGlassCloseButtonTargetSize() throws {
        let airplayHost = UIHostingController(
            rootView: GlassIconButton(systemImage: "airplayvideo", accessibilityLabel: "Play on Apple TV") {}
        )
        let closeHost = UIHostingController(rootView: GlassCloseButton {})
        XCTAssertEqual(airplayHost.sizeThatFits(in: CGSize(width: 200, height: 200)), closeHost.sizeThatFits(in: CGSize(width: 200, height: 200)))
    }

    @MainActor
    func testHeroPlayButtonWithProgressMatchesStandardSize() throws {
        let plain = UIHostingController(rootView: HeroPlayButton {})
        let resumed = UIHostingController(rootView: HeroPlayButton(accessibilityLabel: "Resume", progress: 0.5) {})
        let plainSize = plain.sizeThatFits(in: CGSize(width: 200, height: 200))
        let resumedSize = resumed.sizeThatFits(in: CGSize(width: 200, height: 200))
        XCTAssertEqual(plainSize, CGSize(width: PlyDesign.heroPlayTouchSize, height: PlyDesign.heroPlayTouchSize))
        XCTAssertEqual(resumedSize, plainSize)
    }

    #if os(iOS)
    @MainActor
    func testPlyCloseToolbarPlacesCloseButtonOnTopRight() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        defer { window.isHidden = true }
        let host = UIHostingController(rootView: NavigationStack {
            Color.black
                .navigationTitle("Trailer")
                .plyInlineTitle()
                .plyCloseToolbar(accessibilityLabel: "Close trailer") {}
        })
        window.rootViewController = host
        window.makeKeyAndVisible()

        func findNavController(from vc: UIViewController) -> UINavigationController? {
            if let nav = vc as? UINavigationController { return nav }
            for child in vc.children {
                if let nav = findNavController(from: child) { return nav }
            }
            return nil
        }

        var trailingCount = 0
        var leadingCount = 0
        let deadline = Date().addingTimeInterval(1.0)
        while Date() < deadline {
            window.layoutIfNeeded()
            if let item = findNavController(from: host)?.topViewController?.navigationItem {
                trailingCount = (item.rightBarButtonItems?.count ?? 0) + item.trailingItemGroups.flatMap(\.barButtonItems).count
                leadingCount = (item.leftBarButtonItems?.count ?? 0) + item.leadingItemGroups.flatMap(\.barButtonItems).count
                if trailingCount > 0 { break }
            }
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        }

        XCTAssertGreaterThan(trailingCount, 0, "close toolbar item must be on the top-right")
        XCTAssertEqual(leadingCount, 0, "close toolbar item must not be on the left")
    }
    #endif

    func testTabBarBottomClearanceExceedsFloatingDockWithBreathingGap() {
        let dockFootprint = PlyDesign.floatingTabBarHeight + PlyDesign.floatingTabBarBottomInset
        XCTAssertEqual(dockFootprint, 66)
        XCTAssertGreaterThanOrEqual(PlyDesign.floatingTabBarBreathingGap, 32)
        XCTAssertEqual(
            PlyDesign.tabBarBottomClearance,
            dockFootprint + PlyDesign.floatingTabBarBreathingGap
        )
    }

    @MainActor
    func testHeaderLogoSizeIsScaled30PercentFrom22PtBaseline() {
        let logoSize = PlyLogo().size
        XCTAssertEqual(PlyDesign.headerLogoSize, 22 * 1.3, accuracy: 0.01)
        XCTAssertEqual(logoSize, PlyDesign.headerLogoSize, accuracy: 0.01)
    }

    func testTabIconsShareOpticalHeightAndBalancedWidths() {
        let box = CGRect(x: 0, y: 0, width: 24, height: 24)
        var heights: [CGFloat] = []

        for isFilled in [false, true] {
            for kind in PlyTabIcon.Kind.allCases {
                let bounds = PlyTabIcon.layers(for: kind, in: box, isFilled: isFilled).combinedBounds
                XCTAssertFalse(bounds.isEmpty, "\(kind) (filled=\(isFilled)) should not be empty")
                XCTAssertGreaterThanOrEqual(bounds.minY, 3.0, "\(kind) should sit inside top optical margin")
                XCTAssertLessThanOrEqual(bounds.maxY, 21.0, "\(kind) should sit inside bottom optical margin")
                XCTAssertEqual(bounds.height, 17.1, accuracy: 0.5, "\(kind) height should match the shared optical band")
                XCTAssertEqual(bounds.width, 17.3, accuracy: 0.9, "\(kind) width should stay balanced across tabs")
                heights.append(bounds.height)
            }
        }

        let spread = (heights.max() ?? 0) - (heights.min() ?? 0)
        XCTAssertLessThanOrEqual(spread, 0.8, "all 5 tab icons should share perceived height within 0.8 units")
    }

    @MainActor
    func testTabIconsRenderOpaquePixelsAndFilledStateIsHeavier() throws {
        for kind in PlyTabIcon.Kind.allCases {
            let outlineCount = try countWhitePixels(in: PlyTabIcon(kind: kind, isFilled: false, size: 20))
            let filledCount = try countWhitePixels(in: PlyTabIcon(kind: kind, isFilled: true, size: 20))
            XCTAssertGreaterThan(outlineCount, 40, "\(kind) outline should render visible strokes")
            XCTAssertGreaterThan(filledCount, outlineCount, "\(kind) active state should carry more visual weight than outline")
        }
    }

    @MainActor
    private func countWhitePixels<V: View>(in icon: V) throws -> Int {
        let renderer = ImageRenderer(content: icon.foregroundStyle(Color.white).background(Color.black))
        renderer.scale = 2
        let cgImage = try XCTUnwrap(renderer.cgImage)
        var bytes = [UInt8](repeating: 0, count: cgImage.width * cgImage.height * 4)
        let ctx = try XCTUnwrap(CGContext(
            data: &bytes,
            width: cgImage.width,
            height: cgImage.height,
            bitsPerComponent: 8,
            bytesPerRow: cgImage.width * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ))
        ctx.draw(cgImage, in: CGRect(x: 0, y: 0, width: cgImage.width, height: cgImage.height))
        var count = 0
        for i in stride(from: 0, to: bytes.count, by: 4) where bytes[i] > 180 && bytes[i + 1] > 180 && bytes[i + 2] > 180 {
            count += 1
        }
        return count
    }
}
