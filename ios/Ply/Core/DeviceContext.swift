import Foundation

/// A device id is `[A-Za-z0-9_-]{8,64}`: the server ignores anything else
/// (docs/M11-CLIENTS.md, "Devices").
nonisolated func isValidDeviceID(_ s: String) -> Bool {
    (8...64).contains(s.count) && s.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }
}

/// What this app registers itself as (docs/M11-CLIENTS.md, "Devices").
nonisolated struct DeviceRegistration: Encodable, Sendable, Equatable {
    let name: String
    let kind: String
    let app: String
    let version: String
}

/// The device a request is made as: its id, what it registers as, and the number to
/// send with the next `POST /playback`. The app builds the live one from the Keychain
/// and UIKit (Device.swift); a test builds its own, which is why the core does not
/// import UIKit.
@MainActor
struct DeviceContext {
    let id: String
    let registration: DeviceRegistration
    /// Larger than any this device sent before, across restarts.
    let nextAttempt: () -> Int64

    /// The largest attempt the server takes is 2^53-1; a stored number past it is forgotten.
    nonisolated static func nextAttempt(after stored: Int64, now: Int64) -> Int64 {
        let limit: Int64 = 1 << 53 - 1
        let last = (1..<limit).contains(stored) ? stored : 0
        return max(now, last + 1)
    }
}
