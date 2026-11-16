import CoreImage
import CoreImage.CIFilterBuiltins
import ImageIO
import SwiftUI

/// A single RGB sample in linear-friendly [0, 1] range for `PosterPalette`.
nonisolated struct PaletteColor: Equatable, Sendable {
    let r: Double
    let g: Double
    let b: Double

    var color: Color {
        Color(red: r, green: g, blue: b)
    }

    func opacity(_ alpha: Double) -> Color {
        color.opacity(alpha)
    }

    func scaled(by factor: Double) -> PaletteColor {
        PaletteColor(
            r: min(1, max(0, r * factor)),
            g: min(1, max(0, g * factor)),
            b: min(1, max(0, b * factor))
        )
    }
}

/// Five-region chromatic palette extracted from a poster or backdrop for `LivingMeshBackdrop`.
nonisolated struct PosterPalette: Equatable, Sendable {
    let topLeft: PaletteColor
    let topRight: PaletteColor
    let bottomLeft: PaletteColor
    let bottomRight: PaletteColor
    let center: PaletteColor

    static let fallback = PosterPalette(
        topLeft: PaletteColor(r: 0.16, g: 0.11, b: 0.08),
        topRight: PaletteColor(r: 0.08, g: 0.12, b: 0.18),
        bottomLeft: PaletteColor(r: 0.05, g: 0.04, b: 0.06),
        bottomRight: PaletteColor(r: 0.20, g: 0.09, b: 0.04),
        center: PaletteColor(r: 0.12, g: 0.09, b: 0.10)
    )

    /// 3x3 lattice colors for `MeshGradient`, carrying the extracted artwork tones through the full canvas.
    var meshColors: [Color] {
        [
            topLeft.color, topLeft.opacity(0.88), topRight.color,
            topLeft.opacity(0.76), center.color, topRight.opacity(0.76),
            bottomLeft.scaled(by: 0.80).color, center.scaled(by: 0.65).color, bottomRight.scaled(by: 0.80).color
        ]
    }
}

/// Extracts and caches quadrant `PosterPalette`s from artwork URLs using CoreImage `CIAreaAverage`.
actor PosterPaletteExtractor {
    static let shared = PosterPaletteExtractor()

    private static let ciContext = CIContext(options: [.workingColorSpace: NSNull()])
    private var cache: [URL: PosterPalette] = [:]

    func palette(for url: URL?) async -> PosterPalette {
        guard let url else { return .fallback }
        if let cached = cache[url] { return cached }

        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            guard let source = CGImageSourceCreateWithData(data as CFData, nil),
                  let cgImage = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
                return .fallback
            }
            let extracted = Self.extract(from: cgImage)
            cache[url] = extracted
            return extracted
        } catch {
            return .fallback
        }
    }

    /// Synchronously samples four quadrants and the center region of `cgImage`.
    nonisolated static func extract(from cgImage: CGImage) -> PosterPalette {
        let ciImage = CIImage(cgImage: cgImage)
        let ext = ciImage.extent
        guard ext.width > 1, ext.height > 1 else { return .fallback }

        let halfW = ext.width / 2
        let halfH = ext.height / 2

        // CIImage's Y=0 is at the bottom, so top quadrants sit at `ext.midY`.
        let tl = averageColor(ciImage, rect: CGRect(x: ext.minX, y: ext.midY, width: halfW, height: halfH))
        let tr = averageColor(ciImage, rect: CGRect(x: ext.midX, y: ext.midY, width: halfW, height: halfH))
        let bl = averageColor(ciImage, rect: CGRect(x: ext.minX, y: ext.minY, width: halfW, height: halfH))
        let br = averageColor(ciImage, rect: CGRect(x: ext.midX, y: ext.minY, width: halfW, height: halfH))
        let center = averageColor(ciImage, rect: ext.insetBy(dx: ext.width * 0.25, dy: ext.height * 0.25))

        return PosterPalette(topLeft: tl, topRight: tr, bottomLeft: bl, bottomRight: br, center: center)
    }

    /// Clamps and tones an 8-bit RGB sample so bright artwork preserves white editorial contrast.
    nonisolated static func tonedColor(red: UInt8, green: UInt8, blue: UInt8) -> PaletteColor {
        let r = Double(red) / 255.0
        let g = Double(green) / 255.0
        let b = Double(blue) / 255.0
        let maxChannel = max(r, g, b)
        let toneScale = maxChannel > 0.72 ? (0.45 / maxChannel) : 0.62
        return PaletteColor(r: r * toneScale, g: g * toneScale, b: b * toneScale)
    }

    private nonisolated static func averageColor(_ image: CIImage, rect: CGRect) -> PaletteColor {
        let filter = CIFilter.areaAverage()
        filter.inputImage = image
        filter.extent = rect
        guard let output = filter.outputImage else { return PosterPalette.fallback.center }
        var bitmap = [UInt8](repeating: 0, count: 4)
        ciContext.render(
            output,
            toBitmap: &bitmap,
            rowBytes: 4,
            bounds: CGRect(x: 0, y: 0, width: 1, height: 1),
            format: .RGBA8,
            colorSpace: nil
        )
        return tonedColor(red: bitmap[0], green: bitmap[1], blue: bitmap[2])
    }
}

/// A living chromatic field driven by `PosterPalette` and subtle `PhaseAnimator` control-point drift.
struct LivingMeshBackdrop: View {
    let palette: PosterPalette
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private enum DriftPhase: CaseIterable {
        case origin, driftA, driftB

        var interiorPoint: SIMD2<Float> {
            switch self {
            case .origin: SIMD2(0.50, 0.44)
            case .driftA: SIMD2(0.60, 0.36)
            case .driftB: SIMD2(0.40, 0.52)
            }
        }
    }

    var body: some View {
        ZStack {
            Color(red: 0.02, green: 0.02, blue: 0.03)

            if #available(iOS 18.0, tvOS 18.0, *) {
                if reduceMotion {
                    meshView(interior: DriftPhase.origin.interiorPoint)
                } else {
                    PhaseAnimator(DriftPhase.allCases) { phase in
                        meshView(interior: phase.interiorPoint)
                    } animation: { _ in
                        .easeInOut(duration: 5.5)
                    }
                }
            } else {
                ZStack {
                    RadialGradient(
                        colors: [palette.topLeft.opacity(0.85), .clear],
                        center: .topLeading,
                        startRadius: 20,
                        endRadius: 460
                    )
                    RadialGradient(
                        colors: [palette.topRight.opacity(0.75), palette.center.opacity(0.45), .clear],
                        center: .topTrailing,
                        startRadius: 20,
                        endRadius: 500
                    )
                }
                .animation(.easeInOut(duration: 0.75), value: palette)
            }

            LinearGradient(
                stops: [
                    .init(color: Color.black.opacity(0.04), location: 0.0),
                    .init(color: Color.black.opacity(0.14), location: 0.32),
                    .init(color: Color.black.opacity(0.34), location: 0.58),
                    .init(color: Color.black.opacity(0.56), location: 0.82),
                    .init(color: Color.black.opacity(0.74), location: 1.0)
                ],
                startPoint: .top,
                endPoint: .bottom
            )
        }
        .ignoresSafeArea()
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    @available(iOS 18.0, tvOS 18.0, *)
    private func meshView(interior: SIMD2<Float>) -> some View {
        MeshGradient(
            width: 3,
            height: 3,
            points: [
                SIMD2(0.0, 0.0), SIMD2(0.5, 0.0), SIMD2(1.0, 0.0),
                SIMD2(0.0, 0.5), interior, SIMD2(1.0, 0.5),
                SIMD2(0.0, 1.0), SIMD2(0.5, 1.0), SIMD2(1.0, 1.0)
            ],
            colors: palette.meshColors,
            smoothsColors: true
        )
        .opacity(0.92)
        .animation(.spring(duration: 0.85, bounce: 0), value: palette)
    }
}

/// Coordinates the in-place Spatial Portal expansion between `HomeView` and `MainTabs` chrome.
@Observable
final class SpatialPortalState {
    var expandedItem: Item?
    var isPresented: Bool { expandedItem != nil }

    func open(_ item: Item) {
        expandedItem = item
    }

    func dismiss() {
        expandedItem = nil
    }
}

extension View {
    /// Conditionally applies `matchedGeometryEffect` when participating in a Spatial Portal transition.
    @ViewBuilder
    func spatialMatchedGeometry(id: String, in namespace: Namespace.ID?, isSource: Bool = true) -> some View {
        if let namespace {
            matchedGeometryEffect(id: id, in: namespace, isSource: isSource)
        } else {
            self
        }
    }
}
