import SwiftUI

struct HomeView: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    if !library.continueWatching.isEmpty {
                        Shelf("Continue Watching") {
                            ForEach(library.continueWatching) { item in
                                NavigationLink(value: Route.item(item.id)) {
                                    LandscapeCard(item: item).frame(width: 240)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    if !library.movies.isEmpty {
                        Shelf("Recently Added Movies") {
                            ForEach(library.recentMovies) { movie in
                                NavigationLink(value: Route.item(movie.id)) {
                                    PosterCard(title: movie.title, caption: movie.year.map(String.init), image: movie.poster,
                                               watched: movie.isWatched, fraction: movie.fraction)
                                        .frame(width: 120)
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
            .navigationTitle(connection.info.name)
            .plyDestinations()
        }
    }
}

struct MoviesView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""

    var body: some View {
        let library = connection.library
        let movies = query.isEmpty ? library.movies : library.movies.filter { $0.title.localizedStandardContains(query) }
        NavigationStack {
            ScrollView {
                LazyVGrid(columns: posterColumns, spacing: 16) {
                    ForEach(movies) { movie in
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
                if !query.isEmpty && movies.isEmpty {
                    ContentUnavailableView.search(text: query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.movies.isEmpty)
                }
            }
            .plySearchable($query, prompt: "Movies")
            .plyRefreshable { await library.reload() }
            .navigationTitle("Movies")
            .plyDestinations()
        }
    }
}

struct ShowsView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""

    var body: some View {
        let library = connection.library
        let shows = query.isEmpty ? library.shows : library.shows.filter { $0.show.localizedStandardContains(query) }
        NavigationStack {
            ScrollView {
                LazyVGrid(columns: posterColumns, spacing: 16) {
                    ForEach(shows) { show in
                        NavigationLink(value: Route.show(show.show)) {
                            PosterCard(title: show.show, caption: caption(show), image: show.poster, watched: show.allWatched)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding()
            }
            .overlay {
                if !query.isEmpty && shows.isEmpty {
                    ContentUnavailableView.search(text: query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.shows.isEmpty)
                }
            }
            .plySearchable($query, prompt: "Shows")
            .plyRefreshable { await library.reload() }
            .navigationTitle("Shows")
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
