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
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
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

    func testDetailClearanceAndContentPaddingUseSharedDesignTokens() {
        XCTAssertEqual(PlyDesign.defaultDetailBottomClearance, PlyDesign.standardBottomClearance)
        XCTAssertEqual(PlyDesign.contentHorizontalPadding, 16)
    }

    @MainActor
    func testHeaderLogoSizeIsScaled30PercentFrom22PtBaseline() {
        let logoSize = PlyLogo().size
        XCTAssertEqual(PlyDesign.headerLogoSize, 22 * 1.3, accuracy: 0.01)
        XCTAssertEqual(logoSize, PlyDesign.headerLogoSize, accuracy: 0.01)
    }
}
