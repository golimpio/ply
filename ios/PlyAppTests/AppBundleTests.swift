import XCTest
@testable import Ply

/// What the built app must declare to find and play from a ply server on the LAN (docs/M11-CLIENTS.md,
/// "Platforms"). Only a test hosted in the app sees its generated Info.plist, so these run on a simulator
/// (`make app-test`, `make tv-test`), once in the iPhone/iPad app and once in the Apple TV app.
nonisolated final class AppBundleTests: XCTestCase {
    private func info(_ key: String) -> Any? { Bundle.main.object(forInfoDictionaryKey: key) }

    /// The positive control: the checks below read the app's bundle, not the test bundle's.
    func testRunsInsideTheApp() {
        XCTAssertEqual(Bundle.main.bundleURL.lastPathComponent, "Ply.app")
        XCTAssertNotNil(info("CFBundleIdentifier"))
    }

    func testLocalNetworkAccessIsDeclared() {
        let usage = info("NSLocalNetworkUsageDescription") as? String ?? ""
        XCTAssertFalse(usage.isEmpty, "without a usage description iOS never asks for the local network")
        let services = info("NSBonjourServices") as? [String] ?? []
        XCTAssertTrue(services.contains(Discovery.serviceType),
                      "NSBonjourServices \(services) does not list \(Discovery.serviceType): the browse is refused")
    }

    func testPlainHTTPToTheLANIsAllowed() {
        let ats = info("NSAppTransportSecurity") as? [String: Any]
        XCTAssertEqual(ats?["NSAllowsLocalNetworking"] as? Bool, true, "a server on http://<LAN address> would be refused")
    }
}
