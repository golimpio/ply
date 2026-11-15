import UIKit
import XCTest
@testable import Ply

nonisolated final class PlaybackArtworkTests: XCTestCase {
    /// MediaRemote asks `MPMediaItemArtwork` for its image on its own queue; doing that from the
    /// main actor used to trip a libdispatch assertion and crash a local play.
    @MainActor
    func testArtworkCanBeReadOffTheMainThread() {
        let read = DispatchQueue.global(qos: .userInitiated).sync {
            Self.artworkReadsOnABackgroundQueue()
        }
        XCTAssertTrue(read)
    }

    private nonisolated static func artworkReadsOnABackgroundQueue() -> Bool {
        let png = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2n0kAAAAASUVORK5CYII=")!
        guard let image = UIImage(data: png) else { return false }
        let artwork = PlaybackController.artwork(for: image)
        return artwork.image(at: image.size) != nil
    }
}
