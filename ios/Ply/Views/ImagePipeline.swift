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

    /// Synchronously returns a decoded image already resident in memory, avoiding placeholder flicker on scroll.
    func cachedImage(for url: URL?) -> UIImage? {
        guard let url else { return nil }
        return cache.object(forKey: url.absoluteString as NSString)
    }

    /// A `Sendable` `CGImage` from the same coalesced full-size load `RemoteImage` makes, so a poster shown and
    /// sampled at once (Home's card and its backdrop palette) downloads once.
    func cgImage(for url: URL?) async -> CGImage? {
        await image(for: url)?.cgImage
    }

    /// Fetches, decodes off the main actor, and caches the image, coalescing duplicate in-flight loads.
    func image(for url: URL?) async -> UIImage? {
        guard let url else { return nil }
        let key = url.absoluteString
        if let hit = cache.object(forKey: key as NSString) {
            return hit
        }
        if let existing = inFlight[key] {
            return await existing.value
        }

        let session = self.session
        let task = Task<UIImage?, Never> {
            await Self.fetchAndDecode(url: url, session: session)
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

    /// Pre-populates the memory cache (used by tests verifying image rendering without network I/O).
    func seed(_ image: UIImage, for url: URL) {
        cache.setObject(image, forKey: url.absoluteString as NSString)
    }

    private nonisolated static func fetchAndDecode(url: URL, session: URLSession) async -> UIImage? {
        do {
            let (data, response) = try await session.data(from: url)
            if let http = response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
                return nil
            }
            return decode(data: data)
        } catch {
            return nil
        }
    }

    nonisolated static func decode(data: Data) -> UIImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        let options: [CFString: Any] = [kCGImageSourceShouldCacheImmediately: true]
        guard let cgImage = CGImageSourceCreateImageAtIndex(source, 0, options as CFDictionary) else { return nil }
        return UIImage(cgImage: cgImage)
    }
}
