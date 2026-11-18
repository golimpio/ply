import Foundation

/// What the iOS Search tab's results cover (no Genres: Films and Series already filter by genre).
nonisolated enum SearchScope: String, CaseIterable, Identifiable, Sendable {
    case all = "All"
    case films = "Films"
    case series = "Series"
    case people = "People"
    case collections = "Collections"

    var id: String { rawValue }
}

/// Bounded, case-insensitively deduplicated recent search queries persisted in `UserDefaults`.
nonisolated enum SearchHistory {
    static let maxCount = 6
    static let defaultKey = "ply.search.recent"

    static func load(defaults: UserDefaults = .standard, key: String = defaultKey) -> [String] {
        defaults.stringArray(forKey: key) ?? []
    }

    @discardableResult
    static func record(_ query: String, defaults: UserDefaults = .standard, key: String = defaultKey) -> [String] {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        var current = load(defaults: defaults, key: key)
        guard !trimmed.isEmpty else { return current }

        current.removeAll { $0.caseInsensitiveCompare(trimmed) == .orderedSame }
        current.insert(trimmed, at: 0)
        if current.count > maxCount {
            current = Array(current.prefix(maxCount))
        }
        defaults.set(current, forKey: key)
        return current
    }

    @discardableResult
    static func remove(_ query: String, defaults: UserDefaults = .standard, key: String = defaultKey) -> [String] {
        var current = load(defaults: defaults, key: key)
        current.removeAll { $0.caseInsensitiveCompare(query) == .orderedSame }
        defaults.set(current, forKey: key)
        return current
    }

    static func clear(defaults: UserDefaults = .standard, key: String = defaultKey) {
        defaults.removeObject(forKey: key)
    }
}
