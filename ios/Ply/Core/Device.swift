import Foundation
import Security
import UIKit

/// This app as a device of ply (docs/M11-CLIENTS.md, "Devices"): one id, made when it is first
/// opened and kept, and what it registers itself as.
enum Device {
    private static let attemptKey = "ply.attempt"

    /// What a device id is: the server ignores anything else.
    nonisolated static func isValidID(_ s: String) -> Bool {
        isValidDeviceID(s)
    }

    /// Kept in the Keychain beside the tokens, which outlive an uninstall: a token is only good with the
    /// id it was paired for (a paired device registers and acks only as itself).
    static let id: String = {
        if let id = Keychain.read(service: "ply.device", account: "id"), isValidID(id) { return id }
        let id = UUID().uuidString
        Keychain.write(id, service: "ply.device", account: "id")
        return id
    }()

    static var registration: DeviceRegistration {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
        return DeviceRegistration(name: UIDevice.current.name, kind: Device.kind, app: Device.app, version: version)
    }

    /// The kind the server files this app under (docs/M11-CLIENTS.md, "Devices").
    static var kind: String {
        switch UIDevice.current.userInterfaceIdiom {
        case .pad: "ipados"
        case .tv: "tvos"
        default: "ios"
        }
    }

    /// The app's name in the device list: one app, whatever the platform.
    static var app: String {
        #if os(tvOS)
        "ply-tvos"
        #else
        "ply-ios"
        #endif
    }

    /// The number to send with the next POST /playback: larger than any this device sent before,
    /// across restarts. The clock's milliseconds, made to go up even when the clock goes back.
    static func nextAttempt() -> Int64 {
        let defaults = UserDefaults.standard
        let stored = Int64(defaults.string(forKey: attemptKey) ?? "") ?? 0
        let next = DeviceContext.nextAttempt(after: stored, now: Int64(Date().timeIntervalSince1970 * 1000))
        defaults.set(String(next), forKey: attemptKey)
        return next
    }
}

/// The live device context: the id kept in the Keychain, the name and kind UIKit
/// reports, and the attempt number in UserDefaults.
extension DeviceContext {
    static var live: DeviceContext {
        DeviceContext(id: Device.id, registration: Device.registration, nextAttempt: { Device.nextAttempt() })
    }
}

/// This device's id, and its token for each server it paired with (by server id). Neither moves to
/// another phone with a backup: a new phone is a new device, and pairs.
enum Keychain {
    static func token(for server: String) -> String? {
        read(service: "ply.token", account: server)
    }

    /// nil removes it.
    static func setToken(_ token: String?, for server: String) {
        write(token, service: "ply.token", account: server)
    }

    static func read(service: String, account: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    /// nil removes it.
    static func write(_ value: String?, service: String, account: String) {
        let match: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(match as CFDictionary)
        guard let value else { return }
        var add = match
        add[kSecValueData as String] = Data(value.utf8)
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(add as CFDictionary, nil)
    }
}

/// The server this app last connected to.
struct SavedServer: Codable, Equatable {
    let id: String
    let name: String
    let base: URL

    private static let key = "ply.server"

    static func load() -> SavedServer? {
        UserDefaults.standard.data(forKey: key).flatMap { try? JSONDecoder().decode(SavedServer.self, from: $0) }
    }

    func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(self), forKey: Self.key)
    }

    static func clear() {
        UserDefaults.standard.removeObject(forKey: key)
    }
}
