import Foundation
import Observation

/// What the browse and search pages need beyond the library tabs: the genres and collections the
/// server knows, and the people the server matches to a query. The titles, shows and episodes it
/// searches are the library's (`LibraryModel`), which reads and prepares them once.
@Observable final class BrowseModel {
    private(set) var genres: [GenreCount] = []
    private(set) var collections: [CollectionCard] = []
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
