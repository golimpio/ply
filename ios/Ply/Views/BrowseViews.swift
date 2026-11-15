import SwiftUI

// The pages the library's metadata adds: a search over everything the server knows, and the
// people, genres and collections the web UI has (docs/M11-CLIENTS.md, "Browsing").

extension TitleCard {
    /// Where a card opens: a film's file, a series' show page. nil when neither is known.
    var route: Route? {
        if isSeries { return show.map(Route.show) }
        return itemId.map(Route.item)
    }

    /// The line under the title on a poster.
    var caption: String? {
        if isSeries {
            let n = episodeCount ?? 0
            return n == 1 ? "1 episode" : "\(n) episodes"
        }
        return yearText
    }
}

/// A poster for a title the metadata lists (a genre's, a person's, a collection's).
struct TitlePoster: View {
    let card: TitleCard
    var caption: String?
    var width: CGFloat?

    var body: some View {
        let poster = PosterCard(title: card.title, caption: caption ?? card.caption, image: card.poster,
                                watched: card.isWatched)
        Group {
            if let route = card.route {
                NavigationLink(value: route) { poster }
                    .buttonStyle(.plain)
            } else {
                poster
            }
        }
        .frame(width: width)
    }
}

/// The reusable poster grid of the browse pages.
func titleGrid(_ cards: [TitleCard]) -> some View {
    LazyVGrid(columns: posterColumns, spacing: 16) {
        ForEach(cards) { TitlePoster(card: $0) }
    }
}

// MARK: - browse and search

struct BrowseView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        let browse = connection.browse
        let library = connection.library
        NavigationStack {
            ScrollView {
                #if os(tvOS)
                HStack(spacing: 8) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    TextField("Titles, shows, episodes, people, genres", text: $query)
                }
                .font(.headline)
                .padding(.horizontal)
                .padding(.top, 10)
                #endif
                if trimmed.isEmpty {
                    browseHome(browse)
                } else {
                    results(browse, library)
                }
                #if os(iOS)
                Spacer().frame(height: 74)
                #endif
            }
            .plySearchable($query, prompt: "Titles, shows, episodes, people, genres")
            .plyRefreshable {
                await library.reload()
                await browse.load()
            }
            .task(id: library.generation) {
                async let lists: Void = browse.load()
                async let episodes: Void = library.ensureEpisodes()
                _ = await (lists, episodes)
            }
            .task(id: query) { await connection.browse.searchPeople(trimmed) }
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarLeading) {
                    PlyBrand(size: 28)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Text("Search")
                        .font(.title3.bold())
                        .foregroundStyle(.primary)
                }
                #endif
            }
            #if os(iOS)
            .navigationTitle("")
            .plyInlineTitle()
            #else
            .navigationTitle("Search")
            #endif
            .plyDestinations()
        }
    }

    @ViewBuilder
    private func browseHome(_ browse: BrowseModel) -> some View {
        VStack(alignment: .leading, spacing: 28) {
            if let error = browse.error {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Some of the browse lists could not be loaded")
                        .font(.subheadline.weight(.semibold))
                    Text(error).font(.caption).foregroundStyle(.secondary)
                    Button("Try again") { Task { await browse.load() } }
                        .font(.caption)
                }
                .padding(.horizontal)
            }
            if !browse.collections.isEmpty {
                Shelf("Collections") {
                    ForEach(browse.collections) { collection in
                        NavigationLink(value: Route.collection(collection.id, collection.name)) {
                            PosterCard(title: collection.name,
                                       caption: collection.count == 1 ? "1 title" : "\(collection.count) titles",
                                       image: collection.poster)
                                .frame(width: shelfCardWidth)
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            // Always offered: the server has no list of people, but its search always works.
            VStack(alignment: .leading, spacing: 10) {
                Text("People").font(.title3.bold()).padding(.horizontal)
                NavigationLink(value: Route.people) {
                    HStack(spacing: 10) {
                        Image(systemName: "person.2.fill").foregroundStyle(.secondary)
                        Text("Search the people in your library")
                        Spacer(minLength: 0)
                        Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                    }
                    .font(.subheadline)
                    .padding(12)
                    .background(.quaternary, in: .rect(cornerRadius: 8))
                }
                .buttonStyle(.plain)
                .padding(.horizontal)
            }
            EpisodesFailure()
            if !browse.genres.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    Text("Genres").font(.title3.bold()).padding(.horizontal)
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: genreTileWidth), spacing: 8)], spacing: 8) {
                        ForEach(browse.genres) { genre in
                            NavigationLink(value: Route.genre(genre.id, genre.name)) {
                                HStack {
                                    Text(genre.name).lineLimit(1)
                                    Spacer(minLength: 0)
                                    Text("\(genre.count)").foregroundStyle(.secondary)
                                }
                                .font(.subheadline)
                                .padding(10)
                                .background(.quaternary, in: .rect(cornerRadius: 8))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding(.horizontal)
                }
            }
            if browse.loaded, browse.collections.isEmpty, browse.genres.isEmpty, browse.error == nil {
                ContentUnavailableView("No genres or collections yet", systemImage: "square.grid.2x2",
                                       description: Text("They come from the library's metadata. The search and the People page work already."))
            } else if !browse.loaded {
                ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
            }
        }
        .padding(.vertical)
    }

    @ViewBuilder
    private func results(_ browse: BrowseModel, _ library: LibraryModel) -> some View {
        // Ranked as the web ranks a search: the whole query as a word, then a run of characters, then
        // characters scattered through the text. The lists were prepared when they were loaded.
        let movies = library.movieSearch.search(trimmed, limit: 20)
        let shows = library.showSearch.search(trimmed, limit: 20)
        let episodes = library.episodeSearch.search(trimmed, limit: 20)
        // The server's own search (GET /api/people?q=) is the only list of people it has; its matches
        // are ranked here like every other kind, best first.
        let people = Fuzzy.search(browse.people, query: trimmed, limit: 10)
        let genres = Fuzzy.search(browse.genres, query: trimmed)
        let collections = Fuzzy.search(browse.collections, query: trimmed)
        let anything = !movies.isEmpty || !shows.isEmpty || !episodes.isEmpty
            || !people.isEmpty || !genres.isEmpty || !collections.isEmpty || browse.peopleError != nil

        VStack(alignment: .leading, spacing: 22) {
            if let error = browse.peopleError {
                Text("People search failed: \(error)")
                    .font(.caption).foregroundStyle(.secondary).padding(.horizontal)
            }
            if !movies.isEmpty {
                section("Movies", movies.map { movie in
                    BrowseRow(image: movie.poster, placeholder: "film", title: movie.title,
                              subtitle: Format.facts(movie.year.map(String.init), Format.runtime(movie.durationSec)),
                              route: .item(movie.id))
                })
            }
            if !shows.isEmpty {
                section("Shows", shows.map { show in
                    BrowseRow(image: show.poster, placeholder: "tv", title: show.show,
                              subtitle: show.episodeCount == 1 ? "1 episode" : "\(show.episodeCount) episodes",
                              route: .show(show.show))
                })
            }
            if !episodes.isEmpty {
                section("Episodes", episodes.map { episode in
                    BrowseRow(image: episode.landscapeImage, placeholder: "tv",
                              title: episode.episodeTitle ?? episode.title,
                              subtitle: Format.facts(episode.show, episode.episodeCode, episode.year.map(String.init)),
                              route: .item(episode.id))
                })
            }
            if !people.isEmpty {
                section("People", people.map { person in
                    BrowseRow(image: person.profile, placeholder: "person.fill", title: person.name,
                              subtitle: person.knownFor ?? "Person", round: true, route: .person(person.id))
                })
            }
            if !genres.isEmpty {
                section("Genres", genres.map { genre in
                    BrowseRow(image: nil, placeholder: "tag", title: genre.name,
                              subtitle: genre.count == 1 ? "1 title" : "\(genre.count) titles",
                              route: .genre(genre.id, genre.name))
                })
            }
            if !collections.isEmpty {
                section("Collections", collections.map { collection in
                    BrowseRow(image: collection.poster, placeholder: "rectangle.stack", title: collection.name,
                              subtitle: collection.count == 1 ? "1 title" : "\(collection.count) titles",
                              route: .collection(collection.id, collection.name))
                })
            }
            if !anything {
                ContentUnavailableView.search(text: trimmed)
                    .frame(maxWidth: .infinity)
                    .padding(.top, 40)
            }
        }
        .padding(.vertical, 8)
    }

    private func section(_ title: String, _ rows: [BrowseRow]) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.title3.bold()).padding(.horizontal)
            ForEach(rows) { $0 }
        }
    }
}

/// One line of a search result: a picture, a title, a line under it, and where it goes.
struct BrowseRow: View, Identifiable {
    let image: String?
    let placeholder: String
    let title: String
    let subtitle: String?
    var round = false
    let route: Route

    var id: String { "\(route)-\(title)" }

    var body: some View {
        NavigationLink(value: route) {
            HStack(spacing: 12) {
                RemoteImage(path: image, placeholder: placeholder)
                    .frame(width: 48, height: round ? 48 : 64)
                    .clipShape(RoundedRectangle(cornerRadius: round ? 24 : 6))
                VStack(alignment: .leading, spacing: 3) {
                    Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                    if let subtitle, !subtitle.isEmpty {
                        Text(subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
            }
            .padding(.horizontal)
            .padding(.vertical, 4)
            .contentShape(.rect)
            // One line to VoiceOver: the title, and what the line under it says.
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(title)
            .accessibilityValue(subtitle ?? "")
        }
        .buttonStyle(.plain)
    }
}

// MARK: - people

/// The people the titles in the library are about. The server has no list of them, only a search
/// (`GET /api/people?q=`), so the page asks as typing pauses; an empty query prompts for a name.
struct PeopleView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""
    @State private var people: [PersonCard] = []
    @State private var searchError: String?
    @State private var searching = false

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        ScrollView {
            #if os(tvOS)
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search people", text: $query)
            }
            .font(.headline)
            .padding(.horizontal)
            .padding(.top, 10)
            #endif
            content
        }
        .navigationTitle("People")
        .plyInlineTitle()
        .plySearchable($query, prompt: "People")
        .task(id: query) { await search() }
        .plyDestinations()
    }

    @ViewBuilder
    private var content: some View {
        if let searchError {
            Text("The search failed: \(searchError)")
                .font(.caption).foregroundStyle(.secondary).padding(.horizontal).padding(.top, 12)
        }
        if trimmed.isEmpty {
            ContentUnavailableView("Search for a person", systemImage: "person.2",
                                   description: Text("People come from the titles in your library."))
                .padding(.top, 40)
        } else if people.isEmpty, searchError == nil, searching {
            ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
        } else if people.isEmpty, searchError == nil {
            ContentUnavailableView.search(text: trimmed).padding(.top, 40)
        } else {
            LazyVGrid(columns: posterColumns, spacing: 16) {
                ForEach(people) { person in
                    NavigationLink(value: Route.person(person.id)) {
                        PosterCard(title: person.name, caption: person.knownFor, image: person.profile)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding()
        }
    }

    /// People are looked up as typing pauses (`PeopleQuery`); a query too short asks nothing.
    private func search() async {
        searching = true
        defer { searching = false }
        do {
            guard let found = try await PeopleQuery.people(trimmed, api: connection.api) else {
                people = []
                searchError = nil
                return
            }
            people = found
            searchError = nil
        } catch is CancellationError {
        } catch {
            people = []
            searchError = error.localizedDescription
        }
    }
}

// MARK: - person

struct PersonView: View {
    let id: Int64
    @Environment(Connection.self) private var connection
    @State private var person: Person?
    @State private var filmography: [PersonFilmographyEntry] = []
    @State private var error: String?

    private var externalFilms: [PersonFilmographyEntry] {
        filmography.filter { !$0.isSeries && $0.isExternal }
    }

    private var externalSeries: [PersonFilmographyEntry] {
        filmography.filter { $0.isSeries && $0.isExternal }
    }

    var body: some View {
        ScrollView {
            if let person {
                VStack(alignment: .leading, spacing: 24) {
                    header(person)
                    ForEach(CreditSection.sections(person.credits)) { section in
                        Shelf(section.title) {
                            ForEach(section.entries) { entry in
                                CreditCard(entry: entry)
                            }
                        }
                    }
                    if !externalFilms.isEmpty {
                        Shelf("Other Films") {
                            ForEach(externalFilms) { entry in
                                FilmographyCard(entry: entry)
                            }
                        }
                    }
                    if !externalSeries.isEmpty {
                        Shelf("Other Series") {
                            ForEach(externalSeries) { entry in
                                FilmographyCard(entry: entry)
                            }
                        }
                    }
                    if person.credits.isEmpty && externalFilms.isEmpty && externalSeries.isEmpty {
                        Text("None of their work is in the library right now.")
                            .font(.callout).foregroundStyle(.secondary).padding(.horizontal)
                    }
                }
                .padding(.bottom)
            }
        }
        .navigationTitle(person?.name ?? "Person")
        .plyInlineTitle()
        .overlay {
            if let error, person == nil {
                ContentUnavailableView("Cannot load this person", systemImage: "person.crop.circle.badge.exclamationmark",
                                       description: Text(error))
            } else if person == nil {
                ProgressView()
            }
        }
        .task { await load() }
    }

    private func header(_ person: Person) -> some View {
        HStack(alignment: .top, spacing: 16) {
            RemoteImage(path: person.profile, placeholder: "person.fill")
                .aspectRatio(2 / 3, contentMode: .fit)
                .frame(width: shelfCardWidth)
                .clipShape(.rect(cornerRadius: 8))
            VStack(alignment: .leading, spacing: 6) {
                Text(person.name).font(.title.bold())
                let facts = Format.facts(person.knownFor.map { "Known for \($0)" },
                                         Self.life(person), person.placeOfBirth)
                if !facts.isEmpty {
                    Text(facts).font(.subheadline).foregroundStyle(.secondary)
                }
                if let biography = person.biography, !biography.isEmpty {
                    Text(biography).font(.callout).lineLimit(10)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal)
        .padding(.top, 4)
    }

    /// "1964–", "1930–2004".
    private static func life(_ person: Person) -> String? {
        guard let born = person.birthday?.prefix(4), !born.isEmpty else { return nil }
        if let died = person.deathday?.prefix(4), !died.isEmpty { return "\(born)–\(died)" }
        return "\(born)–"
    }

    private func load() async {
        do {
            async let p = connection.api.person(id)
            async let f = connection.api.personFilmography(id)
            self.person = try await p
            self.filmography = (try? await f) ?? []
            error = nil
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}

enum CreditGroup: String, CaseIterable {
    case acting = "Acting"
    case directing = "Directing"
    case writing = "Writing"
    case other = "Other"
}

struct CreditSection: Identifiable {
    struct Entry: Identifiable {
        let card: TitleCard
        let detail: String
        var id: Int64 { card.id }
    }

    let group: CreditGroup
    let entries: [Entry]
    var id: String { group.rawValue }
    var title: String { group.rawValue }

    static func sections(_ credits: [PersonCredit]) -> [CreditSection] {
        var byGroup: [CreditGroup: [Int64: (card: TitleCard, details: [String])]] = [:]
        for credit in credits {
            let group = group(of: credit)
            var titles = byGroup[group] ?? [:]
            var entry = titles[credit.title.id] ?? (card: credit.title, details: [])
            let detail = ((credit.kind == "cast" ? credit.character : credit.job) ?? "")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            if !detail.isEmpty, !entry.details.contains(detail) { entry.details.append(detail) }
            titles[credit.title.id] = entry
            byGroup[group] = titles
        }
        return CreditGroup.allCases.compactMap { group in
            guard let titles = byGroup[group] else { return nil }
            let entries = titles.values
                .map { Entry(card: $0.card, detail: $0.details.joined(separator: ", ")) }
                .sorted { $0.card.year > $1.card.year }
            return CreditSection(group: group, entries: entries)
        }
    }

    private static func group(of credit: PersonCredit) -> CreditGroup {
        if credit.kind == "cast" { return .acting }
        switch (credit.job ?? "").trimmingCharacters(in: .whitespaces).lowercased() {
        case "director": return .directing
        case "writer", "screenplay", "story", "novel", "creator": return .writing
        default: return .other
        }
    }
}

struct CreditCard: View {
    let entry: CreditSection.Entry

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            TitlePoster(card: entry.card, caption: entry.card.yearText, width: shelfCardWidth)
            if !entry.detail.isEmpty {
                Text(entry.detail)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .truncationMode(.tail)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(width: shelfCardWidth, alignment: .leading)
    }
}

struct FilmographyCard: View {
    let entry: PersonFilmographyEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            PosterCard(title: entry.title, caption: entry.yearText, image: entry.poster)
            if let desc = entry.roleDescription {
                Text(desc)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .truncationMode(.tail)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(width: shelfCardWidth, alignment: .leading)
    }
}

// MARK: - genre and collection

struct GenreView: View {
    let id: Int64
    let name: String
    @Environment(Connection.self) private var connection
    @State private var titles: [TitleCard] = []
    @State private var error: String?
    @State private var loaded = false

    var body: some View {
        ScrollView {
            titleGrid(titles)
                .padding()
        }
        .navigationTitle(name)
        .plyInlineTitle()
        .overlay {
            if let error, titles.isEmpty {
                ContentUnavailableView("Cannot load this genre", systemImage: "exclamationmark.triangle",
                                       description: Text(error))
            } else if loaded, titles.isEmpty {
                ContentUnavailableView("No titles here yet", systemImage: "tag",
                                       description: Text("No title in this genre is in the library."))
            } else if !loaded {
                ProgressView()
            }
        }
        .task { await load() }
    }

    private func load() async {
        do {
            titles = try await connection.api.titles(genre: id)
            error = nil
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }
}

struct CollectionView: View {
    let id: Int64
    let name: String
    @Environment(Connection.self) private var connection
    @State private var collection: CollectionDetail?
    @State private var error: String?

    var body: some View {
        ScrollView {
            if let collection {
                VStack(alignment: .leading, spacing: 16) {
                    Backdrop(path: collection.backdrop ?? collection.titles.first?.backdrop)
                    VStack(alignment: .leading, spacing: 8) {
                        Text(collection.name).font(.title.bold())
                        if let overview = collection.overview, !overview.isEmpty {
                            Text(overview).font(.callout)
                        }
                        Text(collection.titles.count == 1 ? "1 title in your library" : "\(collection.titles.count) titles in your library")
                            .font(.subheadline).foregroundStyle(.secondary)
                    }
                    .padding(.horizontal)
                    titleGrid(collection.titles)
                        .padding(.horizontal)
                }
                .padding(.bottom)
            }
        }
        .navigationTitle(name)
        .plyInlineTitle()
        .overlay {
            if let error, collection == nil {
                ContentUnavailableView("Cannot load this collection", systemImage: "exclamationmark.triangle",
                                       description: Text(error))
            } else if collection == nil {
                ProgressView()
            }
        }
        .task { await load() }
    }

    private func load() async {
        do {
            collection = try await connection.api.collection(id)
            error = nil
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}
