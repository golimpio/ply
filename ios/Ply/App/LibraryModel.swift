import Foundation
import Observation

/// The lists the tabs show, fetched again when the feed says the library changed. The episodes —
/// the Browse search reads them, and a show's newest file is the latest of its own — are fetched
/// when a page first needs them rather than with Home, and only once per library version.
@Observable final class LibraryModel {
    private(set) var movies: [Item] = []
    private(set) var shows: [ShowSummary] = []
    /// Every episode of the library, as card rows, once a page has asked for them (`ensureEpisodes`).
    private(set) var episodes: [Item] = []
    private(set) var continueWatching: [Item] = []
    private(set) var upcoming: [UpcomingShow] = []
    private(set) var loaded = false
    private(set) var error: String?
    /// Why the episodes could not be loaded, when they could not.
    private(set) var episodesError: String?
    /// Whether the episodes were asked for and answered (an empty library is loaded too).
    private(set) var episodesLoaded = false
    /// Goes up on every reload, so that open pages can fetch their own data again.
    private(set) var generation = 0
    /// Each show's newest file, for the "Recently updated" sort.
    private(set) var latestAddedAt: [String: Int64] = [:]
    /// The lists prepared for searching: each row's text is folded once, not once per keystroke.
    private(set) var movieSearch = Fuzzy.Prepared<Item>([])
    private(set) var showSearch = Fuzzy.Prepared<ShowSummary>([])
    private(set) var episodeSearch = Fuzzy.Prepared<Item>([])

    private let api: APIClient
    private var pending: Task<Void, Never>?
    /// The fetch of the episodes in flight: every page that asks while it runs awaits the same one,
    /// so that opening Browse and sorting Shows by "Recently updated" costs a single request.
    private var loadingEpisodes: Task<Void, Never>?

    init(api: APIClient) {
        self.api = api
    }

    /// The newest movies first.
    var recentMovies: [Item] {
        Array(movies.sorted { ($0.addedAt ?? 0) > ($1.addedAt ?? 0) }.prefix(20))
    }

    func reload() async {
        do {
            async let movies = api.movies()
            async let shows = api.shows()
            async let continueWatching = api.continueWatching()
            async let upcoming = api.upcoming()
            let byTitle: (String, String) -> Bool = { $0.localizedStandardCompare($1) == .orderedAscending }
            self.movies = try await movies.sorted { byTitle($0.sortTitle ?? $0.title, $1.sortTitle ?? $1.title) }
            self.shows = try await shows.sorted { byTitle($0.sortTitle ?? $0.show, $1.sortTitle ?? $1.show) }
            self.continueWatching = try await continueWatching
            self.upcoming = (try? await upcoming) ?? []
            movieSearch = Fuzzy.Prepared(self.movies)
            showSearch = Fuzzy.Prepared(self.shows)
            forgetEpisodes()
            error = nil
        } catch is CancellationError {
            return
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
        generation += 1
    }

    /// The episodes, fetched on first ask and once per library version: a page that needs them calls
    /// this, and every call while a fetch runs awaits that one fetch. A reload forgets them.
    func ensureEpisodes() async {
        if episodesLoaded { return }
        if let loadingEpisodes {
            await loadingEpisodes.value
            return
        }
        let task = Task { [weak self] in
            guard let self else { return }
            await self.fetchEpisodes()
        }
        loadingEpisodes = task
        await task.value
        loadingEpisodes = nil
    }

    /// Fetches the episodes, and asks again if the library changed while the answer was on its way, so
    /// that a reload during the fetch does not leave every page that needs them with nothing.
    private func fetchEpisodes() async {
        while !episodesLoaded {
            let generation = self.generation
            do {
                let episodes = try await api.allEpisodes()
                guard generation == self.generation else { continue }
                self.episodes = episodes
                episodeSearch = Fuzzy.Prepared(episodes)
                latestAddedAt = ShowOrder.latestAddedAt(episodes)
                episodesError = nil
                episodesLoaded = true
            } catch is CancellationError {
                return
            } catch {
                episodesError = error.localizedDescription
                return
            }
        }
    }

    /// The episodes a reload has not read yet, and what was worked out from them.
    private func forgetEpisodes() {
        episodesLoaded = false
        episodes = []
        episodeSearch = Fuzzy.Prepared([])
        latestAddedAt = [:]
    }

    /// A reload after a quiet second, as the feed's library event asks: a burst of changes is one.
    func scheduleReload() {
        pending?.cancel()
        pending = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            await self?.reload()
        }
    }

    func cancel() {
        pending?.cancel()
    }
}
