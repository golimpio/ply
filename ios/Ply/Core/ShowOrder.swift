import Foundation

/// The order of a list of shows that the library's files decide, rather than the server: a show's
/// newest file is the latest `addedAt` of its episodes, which the "Recently updated" sort reads.
nonisolated enum ShowOrder {
    /// Each show's newest file: the latest `addedAt` among its episodes. A show with no episode that
    /// says when it was added is absent, and sorts as the oldest.
    static func latestAddedAt(_ episodes: [Item]) -> [String: Int64] {
        var latest: [String: Int64] = [:]
        for episode in episodes {
            guard let show = episode.show, let added = episode.addedAt else { continue }
            latest[show] = max(latest[show] ?? .min, added)
        }
        return latest
    }
}
