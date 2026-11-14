import Foundation

/// Which episode to play after one ends (next-episode autoplay).
nonisolated enum NextEpisodeSelection {
    /// The item to play after `current`: the next distinct episode in season/number order.
    /// Several files of one episode count once, so a second file of the current episode is not
    /// "the next episode"; the last episode answers nil.
    static func after(_ current: Item, in episodes: [Item]) -> Item? {
        let ordered = episodes.filter(\.isEpisode).sorted {
            ($0.season ?? 0, $0.episode ?? 0) < ($1.season ?? 0, $1.episode ?? 0)
        }
        var seen = Set<String>()
        var distinct: [Item] = []
        for episode in ordered {
            let key = "\(episode.season ?? 0):\(episode.episode ?? 0)"
            if seen.insert(key).inserted { distinct.append(episode) }
        }
        guard let index = distinct.firstIndex(where: {
            ($0.season ?? 0) == (current.season ?? 0) && ($0.episode ?? 0) == (current.episode ?? 0)
        }), index + 1 < distinct.count else { return nil }
        return distinct[index + 1]
    }
}
