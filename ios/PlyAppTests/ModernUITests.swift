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
        if !tabVC.tabs.isEmpty {
            XCTAssertTrue(tabVC.tabs.contains { $0 is UISearchTab }, "Expected native UISearchTab for Tab(role: .search)")
        }
        XCTAssertEqual(host.traitCollection.userInterfaceStyle, .dark)
    }
    #endif
}
