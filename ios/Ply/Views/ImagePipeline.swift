import ImageIO
import SwiftUI
import UIKit

/// Shared in-memory decoded image cache and coalesced downloader for `RemoteImage` and `PosterPaletteExtractor`.
@MainActor
final class ImagePipeline {
    static let shared = ImagePipeline()

    private let cache = NSCache<NSString, UIImage>()
    private let session: URLSession
    private var inFlight: [String: Task<UIImage?, Never>] = [:]

    init(session: URLSession = .shared, totalCostLimit: Int = ImagePipeline.defaultCostLimit) {
        self.session = session
        cache.totalCostLimit = totalCostLimit
    }

    private static var defaultCostLimit: Int {
        #if os(tvOS)
        160 * 1024 * 1024
        #else
        96 * 1024 * 1024
        #endif
    }

    private static func cacheKey(for url: URL, maxPixelSize: Int?) -> String {
        if let maxPixelSize, maxPixelSize > 0 {
            return "\(url.absoluteString)#max=\(maxPixelSize)"
        }
        return url.absoluteString
    }

    /// Synchronously returns a decoded image already resident in memory, avoiding placeholder flicker on scroll.
    func cachedImage(for url: URL?, maxPixelSize: Int? = nil) -> UIImage? {
        guard let url else { return nil }
        return cache.object(forKey: Self.cacheKey(for: url, maxPixelSize: maxPixelSize) as NSString)
    }

    /// Returns a `Sendable` `CGImage`, preferring any full-size cached image before fetching a thumbnail.
    func cgImage(for url: URL?, maxPixelSize: Int = 160) async -> CGImage? {
        if let fullHit = cachedImage(for: url, maxPixelSize: nil)?.cgImage {
            return fullHit
        }
        return await image(for: url, maxPixelSize: maxPixelSize)?.cgImage
    }

    /// Fetches, downsamples/decodes off the main actor, and caches the image, coalescing duplicate in-flight loads.
    func image(for url: URL?, maxPixelSize: Int? = nil) async -> UIImage? {
        guard let url else { return nil }
        let key = Self.cacheKey(for: url, maxPixelSize: maxPixelSize)
        if let hit = cache.object(forKey: key as NSString) {
            return hit
        }
        if let existing = inFlight[key] {
            return await existing.value
        }

        let session = self.session
        let task = Task<UIImage?, Never> {
            await Self.fetchAndDecode(url: url, maxPixelSize: maxPixelSize, session: session)
        }
        inFlight[key] = task
        let decoded = await task.value
        inFlight.removeValue(forKey: key)

        if let decoded {
            let cost = decoded.cgImage.map { $0.bytesPerRow * $0.height }
                ?? Int(decoded.size.width * decoded.size.height * 4)
            cache.setObject(decoded, forKey: key as NSString, cost: max(1, cost))
        }
        return decoded
    }

    /// Clears cached images (primarily for test isolation).
    func removeAll() {
        cache.removeAllObjects()
    }

    private nonisolated static func fetchAndDecode(
        url: URL,
        maxPixelSize: Int?,
        session: URLSession
    ) async -> UIImage? {
        do {
            let (data, response) = try await session.data(from: url)
            if let http = response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
                return nil
            }
            return decode(data: data, maxPixelSize: maxPixelSize)
        } catch {
            return nil
        }
    }

    nonisolated static func decode(data: Data, maxPixelSize: Int?) -> UIImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        if let maxPixelSize, maxPixelSize > 0 {
            let options: [CFString: Any] = [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceShouldCacheImmediately: true,
                kCGImageSourceThumbnailMaxPixelSize: maxPixelSize
            ]
            if let cgImage = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) {
                return UIImage(cgImage: cgImage)
            }
        }
        let options: [CFString: Any] = [kCGImageSourceShouldCacheImmediately: true]
        guard let cgImage = CGImageSourceCreateImageAtIndex(source, 0, options as CFDictionary) else { return nil }
        return UIImage(cgImage: cgImage)
    }
}
