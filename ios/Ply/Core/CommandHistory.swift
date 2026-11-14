import Foundation

/// The newest remote-control seq this device acted on, per server, kept across launches so that a
/// command it performed but could not ack is not performed again from a snapshot
/// (docs/M11-CLIENTS.md, "Remote control").
enum CommandHistory {
    private static func key(_ server: String) -> String { "ply.commands.\(server)" }

    static func lastHandled(for server: String, defaults: UserDefaults = .standard) -> Int64 {
        Int64(defaults.string(forKey: key(server)) ?? "") ?? 0
    }

    static func setLastHandled(_ seq: Int64, for server: String, defaults: UserDefaults = .standard) {
        defaults.set(String(seq), forKey: key(server))
    }
}
