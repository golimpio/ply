import Foundation
import Observation

enum CollectionKindFilter: String, CaseIterable, Identifiable {
    case films = "Films"
    case series = "Series"

    var id: String { rawValue }
    var titleKind: String {
        switch self {
        case .films: "movie"
        case .series: "series"
        }
    }
}

nonisolated struct HomeGenreRail: Identifiable, Hashable {
    let genre: GenreCount
    let titles: [TitleCard]

    var id: Int64 { genre.id }
}

/// What the browse and search pages need beyond the library tabs: the genres and collections the
/// server knows, and the people the server matches to a query. The titles, shows and episodes it
/// searches are the library's (`LibraryModel`), which reads and prepares them once.
@Observable final class BrowseModel {
    private(set) var genres: [GenreCount] = []
    private(set) var homeGenreRails: [HomeGenreRail] = []
    private(set) var collections: [CollectionCard] = []
    private(set) var collectionKinds: [Int64: Set<String>] = [:]
    private(set) var resolvingCollectionKinds = false
    private(set) var people: [PersonCard] = []
    private(set) var loaded = false
    /// Why the lists could not be loaded, so that a failure is not shown as an empty library.
    private(set) var error: String?
    private(set) var peopleError: String?

    private let api: APIClient

    init(api: APIClient) {
        self.api = api
    }

    /// The lists. A server without a collections list (an older ply) still answers the others, so
    /// that one 404 is an empty list; a genres failure is reported.
    func load() async {
        do {
            async let genres = api.genres()
            async let collections = api.collections()
            self.genres = try await genres
            self.collections = (try? await collections) ?? []
            error = nil
        } catch is CancellationError {
            return // a newer load is asking; keep what is on the page
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }

    /// Loads curated genre showcase rails for Home (top 3 genres with >= 4 titles, matching web).
    func loadHomeGenreRails() async {
        do {
            let genres = try await api.genres()
            self.genres = genres
            let top = LibraryModel.topHomeGenres(genres)
            guard !top.isEmpty else {
                homeGenreRails = []
                return
            }
            let api = self.api
            var fetched: [Int64: [TitleCard]] = [:]
            await withTaskGroup(of: (Int64, [TitleCard])?.self) { group in
                for genre in top {
                    group.addTask {
                        guard let list = try? await api.titles(genre: genre.id) else { return nil }
                        return (genre.id, LibraryModel.uniqueTitles(list))
                    }
                }
                for await pair in group {
                    if let (id, list) = pair {
                        fetched[id] = list
                    }
                }
            }
            homeGenreRails = top.compactMap { genre in
                guard let titles = fetched[genre.id], !titles.isEmpty else { return nil }
                return HomeGenreRail(genre: genre, titles: titles)
            }
        } catch is CancellationError {
            return
        } catch {
            // Genre rails are supplemental: keep Home working even if metadata genres fail.
        }
    }

    /// Collections filtered to Films or Series, using server counts when present or cached detail kinds.
    func collections(for filter: CollectionKindFilter) -> [CollectionCard] {
        Self.filterCollections(collections, kindsByID: collectionKinds, for: filter)
    }

    static func filterCollections(
        _ cards: [CollectionCard],
        kindsByID: [Int64: Set<String>] = [:],
        for filter: CollectionKindFilter
    ) -> [CollectionCard] {
        cards.filter { card in
            if let movies = card.movieCount, let series = card.seriesCount {
                return filter == .films ? movies > 0 : series > 0
            }
            if let kinds = kindsByID[card.id] {
                return kinds.contains(filter.titleKind)
            }
            // Before fallback kinds resolve, keep films visible rather than flashing an empty list.
            return filter == .films
        }
    }

    /// Backfills `collectionKinds` when connected to a server that omits `movieCount` / `seriesCount`.
    func resolveCollectionKindsIfNeeded() async {
        let missing = collections.filter {
            ($0.movieCount == nil || $0.seriesCount == nil) && collectionKinds[$0.id] == nil
        }
        guard !missing.isEmpty, !resolvingCollectionKinds else { return }
        resolvingCollectionKinds = true
        defer { resolvingCollectionKinds = false }

        let api = self.api
        await withTaskGroup(of: (Int64, Set<String>)?.self) { group in
            var iterator = missing.makeIterator()
            for _ in 0..<min(6, missing.count) {
                if let next = iterator.next() {
                    group.addTask {
                        guard let detail = try? await api.collection(next.id) else { return nil }
                        return (next.id, Set(detail.titles.map(\.kind)))
                    }
                }
            }
            for await result in group {
                if let (id, kinds) = result {
                    collectionKinds[id] = kinds
                }
                if let next = iterator.next() {
                    group.addTask {
                        guard let detail = try? await api.collection(next.id) else { return nil }
                        return (next.id, Set(detail.titles.map(\.kind)))
                    }
                }
            }
        }
    }

    /// The people whose name matches q, asked as typing pauses (`PeopleQuery`). A query too short
    /// asks nothing, and clears what the last one found.
    func searchPeople(_ q: String) async {
        do {
            guard let found = try await PeopleQuery.people(q, api: api) else {
                people = []
                peopleError = nil
                return
            }
            people = found
            peopleError = nil
        } catch is CancellationError {
        } catch {
            people = []
            peopleError = error.localizedDescription
        }
    }
}
