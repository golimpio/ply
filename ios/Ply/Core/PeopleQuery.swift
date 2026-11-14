import Foundation

/// People are looked up as typing pauses, not on every letter: a query too short asks nothing, and a
/// question a newer one has replaced is dropped. The Browse search and the People page both ask so.
@MainActor enum PeopleQuery {
    /// The people the server matches to `q` (`GET /api/people?q=`, its only list of people), or nil
    /// when `q` is too short to ask about. Throws as the API does; a cancelled ask throws Cancellation.
    static func people(_ q: String, api: APIClient) async throws -> [PersonCard]? {
        let query = q.trimmingCharacters(in: .whitespacesAndNewlines)
        guard query.count >= 2 else { return nil }
        // A typist's pause: the caller's task is cancelled when the query changes, which ends this.
        try await Task.sleep(for: .milliseconds(150))
        try Task.checkCancellation()
        return try await api.people(query)
    }
}
