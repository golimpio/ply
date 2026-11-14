import Foundation
import Observation

/// The lists the tabs show, fetched again when the feed says the library changed.
@Observable final class LibraryModel {
    private(set) var movies: [Item] = []
    private(set) var shows: [ShowSummary] = []
    private(set) var continueWatching: [Item] = []
    private(set) var upcoming: [UpcomingShow] = []
    private(set) var loaded = false
    private(set) var error: String?
    /// Goes up on every reload, so that open pages can fetch their own data again.
    private(set) var generation = 0

    private let api: APIClient
    private var pending: Task<Void, Never>?

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
            error = nil
        } catch is CancellationError {
            return
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
        generation += 1
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
