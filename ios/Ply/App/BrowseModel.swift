import Foundation
import Observation

/// What the browse and search pages need beyond the library tabs: the genres and collections the
/// server knows, every episode (for search), and the people the server matches to a query.
@Observable final class BrowseModel {
    private(set) var genres: [GenreCount] = []
    private(set) var collections: [CollectionCard] = []
    /// Every episode of the library, for search: loaded once, with the rest of the page.
    private(set) var episodes: [Item] = []
    private(set) var people: [PersonCard] = []
    private(set) var loaded = false
    /// Why the lists could not be loaded, so that a failure is not shown as an empty library.
    private(set) var error: String?
    private(set) var peopleError: String?

    private let api: APIClient

    init(api: APIClient) {
        self.api = api
    }

    /// The three lists. A server without a collections list (an older ply) still answers the
    /// others, so that one 404 is an empty list; a genres or episodes failure is reported.
    func load() async {
        do {
            async let genres = api.genres()
            async let collections = api.collections()
            async let episodes = api.allEpisodes()
            self.genres = try await genres
            self.collections = (try? await collections) ?? []
            self.episodes = try await episodes
            error = nil
        } catch is CancellationError {
            return // a newer load is asking; keep what is on the page
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }

    /// The people whose name matches q; the caller debounces. A query too short asks nothing.
    func searchPeople(_ q: String) async {
        let query = q.trimmingCharacters(in: .whitespacesAndNewlines)
        guard query.count >= 2 else {
            people = []
            peopleError = nil
            return
        }
        do {
            people = try await api.people(query)
            peopleError = nil
        } catch is CancellationError {
        } catch {
            people = []
            peopleError = error.localizedDescription
        }
    }

    func clearPeople() {
        people = []
        peopleError = nil
    }
}
