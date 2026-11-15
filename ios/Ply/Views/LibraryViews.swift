import SwiftUI

/// An editorial hero banner at the top of Home, highlighting an in-progress or featured film.
struct HeroBanner: View {
    let item: Item
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene

    private var kicker: String {
        if item.resumeSec > 0 {
            return "Continue Watching"
        }
        return item.isEpisode ? "Featured Series" : "Featured Film"
    }

    private var titleText: String {
        item.isEpisode ? (item.show ?? item.title) : item.title
    }

    private var metaFacts: String {
        let durationOrEpisode = item.isEpisode
            ? Format.facts(item.episodeCode, item.episodeTitle)
            : (item.durationSec > 0 ? Format.runtime(Double(item.durationSec)) : nil)
        return Format.facts(item.year.map(String.init), durationOrEpisode, item.rating?.label)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            NavigationLink(value: Route.item(item.id)) {
                ZStack(alignment: .bottomLeading) {
                    RemoteImage(path: item.backdrop ?? item.landscapeImage, placeholder: "film")
                        .aspectRatio(16 / 9, contentMode: .fit)
                        .clipShape(.rect(cornerRadius: 12))
                        .overlay {
                            LinearGradient(
                                colors: [.clear, .black.opacity(0.85)],
                                startPoint: .center,
                                endPoint: .bottom
                            )
                            .clipShape(.rect(cornerRadius: 12))
                        }

                    VStack(alignment: .leading, spacing: 4) {
                        HStack(spacing: 6) {
                            Circle()
                                .fill(Color.accentColor)
                                .frame(width: 7, height: 7)
                            Text(kicker.uppercased())
                                .font(.system(size: 11, weight: .bold, design: .monospaced))
                                .foregroundStyle(.secondary)
                        }

                        Text(titleText)
                            .font(.system(size: 24, weight: .bold, design: .serif))
                            .foregroundStyle(.white)
                            .lineLimit(1)
                    }
                    .padding(14)
                }
            }
            .buttonStyle(.plain)
            .hoverEffect()

            VStack(alignment: .leading, spacing: 8) {
                if !metaFacts.isEmpty {
                    Text(metaFacts)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.secondary)
                }

                if let overview = item.overview, !overview.isEmpty {
                    Text(overview)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }

                HStack(spacing: 12) {
                    Button {
                        let start = item.resumeSec > 0 ? item.resumeSec : 0
                        model.play(PlayRequest(itemId: item.id, startSec: start), in: playbackScene)
                    } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "play.fill")
                            Text(item.resumeSec > 0 ? "Resume" : "Play")
                                .fontWeight(.semibold)
                        }
                        .padding(.vertical, 8)
                        .padding(.horizontal, 18)
                    }
                    .buttonStyle(.borderedProminent)
                    .clipShape(Capsule())

                    NavigationLink(value: Route.item(item.id)) {
                        HStack(spacing: 6) {
                            Image(systemName: "info.circle")
                            Text("Details")
                                .fontWeight(.medium)
                        }
                        .padding(.vertical, 8)
                        .padding(.horizontal, 16)
                    }
                    .buttonStyle(.bordered)
                    .clipShape(Capsule())
                }
                .padding(.top, 2)
            }
            .padding(.horizontal, 4)
        }
        .padding(.horizontal)
    }
}

struct HomeView: View {
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene

    private var featuredItem: Item? {
        let library = connection.library
        if let continueItem = library.continueWatching.first {
            return continueItem
        }
        if let movieWithBackdrop = library.recentMovies.first(where: { $0.backdrop != nil }) {
            return movieWithBackdrop
        }
        return library.recentMovies.first ?? library.movies.first
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    #if os(tvOS)
                    HStack(spacing: 12) {
                        PlyBrand(size: 36)
                        Text("Home")
                            .font(.title2.weight(.medium))
                            .foregroundStyle(.secondary)
                        Spacer()
                    }
                    .padding(.horizontal)
                    .padding(.top, 8)
                    #endif

                    if let featured = featuredItem {
                        HeroBanner(item: featured)
                    }

                    if !library.continueWatching.isEmpty {
                        Shelf("Continue Watching", subtitle: "Pick up where you left off") {
                            ForEach(library.continueWatching) { item in
                                NavigationLink(value: Route.item(item.id)) {
                                    LandscapeCard(item: item).frame(width: shelfWideCardWidth)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    if !library.upcoming.isEmpty {
                        Shelf("Coming Up", subtitle: "Next in series") {
                            ForEach(library.upcoming) { item in
                                NavigationLink(value: Route.show(item.show)) {
                                    UpcomingCard(upcoming: item).frame(width: shelfWideCardWidth)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    if !library.movies.isEmpty {
                        Shelf("Recently Added Films", subtitle: "Latest additions to your archive") {
                            ForEach(library.recentMovies) { movie in
                                NavigationLink(value: Route.item(movie.id)) {
                                    PosterCard(title: movie.title, caption: movie.year.map(String.init), image: movie.poster,
                                               watched: movie.isWatched, fraction: movie.fraction)
                                        .frame(width: shelfCardWidth)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    if !library.shows.isEmpty {
                        Shelf("Series", subtitle: "Television series in your library") {
                            ForEach(library.shows) { show in
                                NavigationLink(value: Route.show(show.show)) {
                                    PosterCard(title: show.show, caption: show.episodeCount == 1 ? "1 episode" : "\(show.episodeCount) episodes",
                                               image: show.poster, watched: show.allWatched)
                                        .frame(width: shelfCardWidth)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
                .padding(.vertical)
            }
            .overlay { LibraryStatus(library: library, isEmpty: library.movies.isEmpty && library.shows.isEmpty) }
            .plyRefreshable { await library.reload() }
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarLeading) {
                    PlyBrand(size: 28)
                }
                #endif
            }
            .navigationTitle("")
            .plyInlineTitle()
            .plyDestinations()
        }
    }
}

enum MovieSort: String, CaseIterable, Identifiable {
    case title = "A to Z"
    case added = "Recently Added"
    case year = "Newest"
    case duration = "Longest"
    case rating = "Rating"

    var id: String { rawValue }
}

struct MoviesView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""
    @State private var sort: MovieSort = .title
    @State private var only4K = false
    @State private var onlyHDR = false
    @State private var onlyUnwatched = false

    private var filteredMovies: [Item] {
        var list = connection.library.movies
        if !query.isEmpty {
            list = list.filter { $0.title.localizedStandardContains(query) }
        }
        if only4K {
            list = list.filter { $0.is4K }
        }
        if onlyHDR {
            list = list.filter { $0.isHDR }
        }
        if onlyUnwatched {
            list = list.filter { !$0.isWatched }
        }
        switch sort {
        case .title:
            let byTitle: (String, String) -> Bool = { $0.localizedStandardCompare($1) == .orderedAscending }
            return list.sorted { byTitle($0.sortTitle ?? $0.title, $1.sortTitle ?? $1.title) }
        case .added:
            return list.sorted { ($0.addedAt ?? 0) > ($1.addedAt ?? 0) }
        case .year:
            return list.sorted { ($0.year ?? 0) > ($1.year ?? 0) }
        case .duration:
            return list.sorted { $0.durationSec > $1.durationSec }
        case .rating:
            return list.sorted { ($0.rating?.score ?? 0) > ($1.rating?.score ?? 0) }
        }
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollView {
                #if os(tvOS)
                HStack(spacing: 8) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    TextField("Search films", text: $query)
                }
                .font(.headline)
                .padding(.horizontal)
                .padding(.top, 10)
                #endif

                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        Menu {
                            Picker("Sort", selection: $sort) {
                                ForEach(MovieSort.allCases) { Text($0.rawValue).tag($0) }
                            }
                        } label: {
                            Label(sort.rawValue, systemImage: "arrow.up.arrow.down")
                                .font(.footnote)
                        }
                        .buttonStyle(.bordered)

                        FilterChip(title: "4K", isOn: $only4K)
                        FilterChip(title: "HDR", isOn: $onlyHDR)
                        FilterChip(title: "Unwatched", isOn: $onlyUnwatched)
                    }
                    .padding(.horizontal)
                    .padding(.top, 4)
                }

                LazyVGrid(columns: posterColumns, spacing: 16) {
                    ForEach(filteredMovies) { movie in
                        NavigationLink(value: Route.item(movie.id)) {
                            PosterCard(title: movie.title, caption: movie.year.map(String.init), image: movie.poster,
                                       watched: movie.isWatched, fraction: movie.fraction)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding()
            }
            .overlay {
                if (!query.isEmpty || only4K || onlyHDR || onlyUnwatched) && filteredMovies.isEmpty {
                    ContentUnavailableView.search(text: query.isEmpty ? "filtered films" : query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.movies.isEmpty)
                }
            }
            .plySearchable($query, prompt: "Films")
            .plyRefreshable { await library.reload() }
            .navigationTitle("Films")
            .plyDestinations()
        }
    }
}

enum ShowSort: String, CaseIterable, Identifiable {
    case title = "A to Z"
    case added = "Recently Updated"
    case rating = "Rating"
    case episodes = "Most Episodes"

    var id: String { rawValue }
}

struct ShowsView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""
    @State private var sort: ShowSort = .title
    @State private var onlyUnwatched = false

    private var filteredShows: [ShowSummary] {
        var list = connection.library.shows
        if !query.isEmpty {
            list = list.filter { $0.show.localizedStandardContains(query) }
        }
        if onlyUnwatched {
            list = list.filter { !$0.allWatched }
        }
        let byTitle: (String, String) -> Bool = { $0.localizedStandardCompare($1) == .orderedAscending }
        switch sort {
        case .title:
            return list.sorted { byTitle($0.sortTitle ?? $0.show, $1.sortTitle ?? $1.show) }
        case .added:
            // Newest first, a tie by name (`sorted` is not documented stable, and the web ties on it).
            let latest = connection.library.latestAddedAt
            return list.sorted { a, b in
                let (one, other) = (latest[a.show] ?? 0, latest[b.show] ?? 0)
                return one == other
                    ? byTitle(a.sortTitle ?? a.show, b.sortTitle ?? b.show)
                    : one > other
            }
        case .rating:
            // On the server's 0-100 score, so sources on other scales compare; a show with no rating
            // comes last (-1, as the web sorts it), and a tie is by name.
            return list.sorted { a, b in
                let (one, other) = (a.rating?.score ?? -1, b.rating?.score ?? -1)
                return one == other
                    ? byTitle(a.sortTitle ?? a.show, b.sortTitle ?? b.show)
                    : one > other
            }
        case .episodes:
            return list.sorted { $0.episodeCount > $1.episodeCount }
        }
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollView {
                #if os(tvOS)
                HStack(spacing: 8) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    TextField("Search series", text: $query)
                }
                .font(.headline)
                .padding(.horizontal)
                .padding(.top, 10)
                #endif

                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        Menu {
                            Picker("Sort", selection: $sort) {
                                ForEach(ShowSort.allCases) { Text($0.rawValue).tag($0) }
                            }
                        } label: {
                            Label(sort.rawValue, systemImage: "arrow.up.arrow.down")
                                .font(.footnote)
                        }
                        .buttonStyle(.bordered)

                        FilterChip(title: "Unwatched", isOn: $onlyUnwatched)
                    }
                    .padding(.horizontal)
                    .padding(.top, 4)
                }

                if sort == .added {
                    EpisodesFailure()
                }

                LazyVGrid(columns: posterColumns, spacing: 16) {
                    ForEach(filteredShows) { show in
                        NavigationLink(value: Route.show(show.show)) {
                            PosterCard(title: show.show, caption: caption(show), image: show.poster, watched: show.allWatched)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding()
            }
            .overlay {
                if (!query.isEmpty || onlyUnwatched) && filteredShows.isEmpty {
                    ContentUnavailableView.search(text: query.isEmpty ? "filtered series" : query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.shows.isEmpty)
                }
            }
            .plySearchable($query, prompt: "Series")
            .plyRefreshable { await library.reload() }
            // Newest first is a show's newest file, which the episodes list says; fetch it only when
            // that sort is chosen, and again after a reload has forgotten it.
            .task(id: sort) { if sort == .added { await library.ensureEpisodes() } }
            .task(id: library.generation) { if sort == .added { await library.ensureEpisodes() } }
            .navigationTitle("Series")
            .plyDestinations()
        }
    }

    private func caption(_ show: ShowSummary) -> String {
        let left = show.episodeCount - (show.watchedEpisodes ?? 0)
        if left > 0 && left < show.episodeCount { return "\(left) left" }
        return show.episodeCount == 1 ? "1 episode" : "\(show.episodeCount) episodes"
    }
}

struct SettingsView: View {
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model

    var body: some View {
        let info = connection.info
        NavigationStack {
            Form {
                Section("Server") {
                    LabeledContent("Name", value: info.name)
                    LabeledContent("Address", value: connection.base.absoluteString)
                    LabeledContent("Version", value: info.version)
                    LabeledContent("API", value: "\(info.api) (this app: \(clientAPIVersion))")
                }
                Section("This device") {
                    LabeledContent("Name", value: Device.registration.name)
                    LabeledContent("Paired", value: connection.paired ? "Yes" : info.authRequired ? "No" : "Not needed")
                    LabeledContent("Device id") {
                        Text(Device.id)
                            .font(.caption.monospaced())
                            #if os(iOS)
                            .textSelection(.enabled)
                            #endif
                    }
                }
                Section {
                    Button("Connect to another server") { model.chooseServer() }
                    Button("Forget this server", role: .destructive) { model.disconnect() }
                } footer: {
                    Text("Forgetting the server removes this device's token; the owner can also revoke it in Settings › Devices.")
                }
            }
            .navigationTitle("Settings")
        }
    }
}

struct FilterChip: View {
    let title: String
    @Binding var isOn: Bool

    var body: some View {
        Button {
            isOn.toggle()
        } label: {
            Text(title)
                .font(.footnote)
                #if os(iOS)
                .padding(.horizontal, 10)
                .padding(.vertical, 5)
                .background(isOn ? AnyShapeStyle(.tint) : AnyShapeStyle(.quaternary), in: .capsule)
                .foregroundStyle(isOn ? .white : .primary)
                #else
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(isOn ? Color.white.opacity(0.3) : Color.white.opacity(0.1), in: .capsule)
                #endif
        }
        .buttonStyle(.plain)
        // A filter is a toggle, and VoiceOver should say whether it is on.
        .accessibilityAddTraits(isOn ? [.isSelected] : [])
    }
}

struct UpcomingCard: View {
    let upcoming: UpcomingShow

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            RemoteImage(path: upcoming.backdrop ?? upcoming.poster, placeholder: "tv")
                .aspectRatio(16 / 9, contentMode: .fit)
                .overlay(alignment: .topTrailing) {
                    Text(Format.airDate(upcoming.nextAirDate))
                        .font(.caption2.weight(.semibold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 3)
                        .background(.black.opacity(0.65), in: .capsule)
                        .padding(6)
                        .foregroundStyle(.white)
                }
                .clipShape(.rect(cornerRadius: 8))
            Text(upcoming.show)
                .font(.footnote.weight(.medium)).lineLimit(1)
            Text(episodeText)
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .contentShape(.rect)
    }

    private var episodeText: String {
        if let ep = upcoming.nextEpisode {
            let code = "S\(ep.season) · E\(ep.episode)"
            let name = (ep.name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return name.isEmpty ? code : "\(code): \(name)"
        }
        return "New episode"
    }
}
