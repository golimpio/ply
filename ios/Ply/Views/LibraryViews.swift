import SwiftUI
/// The Cinema Canvas & Peeking Deck spotlighting featured, in-progress, and recent items on Home.
struct PeekingCanvasDeckView: View {
    let items: [Item]
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var activeItemId: Int64?

    private var activeItem: Item? {
        if let id = activeItemId, let item = items.first(where: { $0.id == id }) {
            return item
        }
        return items.first
    }

    private var cardWidth: CGFloat {
        #if os(tvOS)
        340
        #else
        260
        #endif
    }

    private var cardHeight: CGFloat {
        #if os(tvOS)
        480
        #else
        375
        #endif
    }

    var body: some View {
        if !items.isEmpty, let active = activeItem {
            VStack(spacing: 16) {
                // The Horizontal Peeking Canvas Carousel
                GeometryReader { proxy in
                    let screenWidth = proxy.size.width
                    let sideInset = max(16, (screenWidth - cardWidth) / 2)

                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 14) {
                            ForEach(items) { item in
                                canvasCard(item, isCenter: item.id == active.id)
                                    .id(item.id)
                                    .scrollTransition(.interactive, axis: .horizontal) { content, phase in
                                        content
                                            .scaleEffect(phase.isIdentity ? 1.0 : 0.92)
                                            .opacity(phase.isIdentity ? 1.0 : 0.55)
                                            .brightness(phase.isIdentity ? 0.0 : -0.06)
                                    }
                            }
                        }
                        .scrollTargetLayout()
                    }
                    .scrollTargetBehavior(.viewAligned)
                    .safeAreaPadding(.horizontal, sideInset)
                    .scrollPosition(id: $activeItemId)
                }
                .frame(height: cardHeight + 20)

                // Title & Metadata Block (matching reference specification)
                VStack(spacing: 8) {
                    // Release Year
                    if let year = active.year {
                        Text(String(year))
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(Color(white: 0.6))
                    }

                    // Main Title (Bold clean sans-serif)
                    Text(active.isEpisode ? (active.show ?? active.title) : active.title)
                        .font(.system(size: 26, weight: .bold, design: .default))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                        .multilineTextAlignment(.center)

                    // Frosted Glass Metadata Pills Row
                    HStack(spacing: 8) {
                        // Genre Pill
                        if let genres = active.genres, let first = genres.first {
                            Text(first)
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(.white.opacity(0.9))
                                .padding(.horizontal, 14)
                                .padding(.vertical, 7)
                                .background(
                                    Capsule()
                                        .fill(Color.white.opacity(0.10))
                                        .overlay(Capsule().strokeBorder(Color.white.opacity(0.12), lineWidth: 1))
                                )
                        } else if active.isEpisode, let code = active.episodeCode {
                            Text(code)
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(.white.opacity(0.9))
                                .padding(.horizontal, 14)
                                .padding(.vertical, 7)
                                .background(
                                    Capsule()
                                        .fill(Color.white.opacity(0.10))
                                        .overlay(Capsule().strokeBorder(Color.white.opacity(0.12), lineWidth: 1))
                                )
                        }

                        // Duration Pill
                        if active.durationSec > 0 {
                            Text(Format.runtime(active.durationSec))
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(.white.opacity(0.9))
                                .padding(.horizontal, 14)
                                .padding(.vertical, 7)
                                .background(
                                    Capsule()
                                        .fill(Color.white.opacity(0.10))
                                        .overlay(Capsule().strokeBorder(Color.white.opacity(0.12), lineWidth: 1))
                                )
                        }

                        // Star Rating Pill
                        if let rating = active.rating {
                            HStack(spacing: 4) {
                                Image(systemName: "star.fill")
                                    .font(.system(size: 11))
                                    .foregroundStyle(Color.yellow)
                                Text(rating.label)
                                    .font(.subheadline.weight(.bold))
                                    .foregroundStyle(.white)
                            }
                            .padding(.horizontal, 14)
                            .padding(.vertical, 7)
                            .background(
                                Capsule()
                                    .fill(Color.white.opacity(0.10))
                                    .overlay(Capsule().strokeBorder(Color.white.opacity(0.12), lineWidth: 1))
                            )
                        }
                    }
                    .padding(.top, 2)

                    // Pagination Dots
                    if items.count > 1 {
                        HStack(spacing: 6) {
                            ForEach(Array(items.prefix(8).enumerated()), id: \.element.id) { index, item in
                                if item.id == active.id {
                                    Capsule()
                                        .fill(Color.orange)
                                        .frame(width: 22, height: 5)
                                } else {
                                    Circle()
                                        .fill(Color.white.opacity(0.28))
                                        .frame(width: 5.5, height: 5.5)
                                        .onTapGesture {
                                            withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) {
                                                activeItemId = item.id
                                            }
                                            #if os(iOS)
                                            UISelectionFeedbackGenerator().selectionChanged()
                                            #endif
                                        }
                                }
                            }
                        }
                        .padding(.top, 6)
                    }
                }
                .animation(.easeInOut(duration: 0.2), value: active.id)
                .padding(.horizontal, 20)
                .frame(maxWidth: .infinity, alignment: .center)
            }
            .frame(maxWidth: .infinity, alignment: .center)
            .onAppear {
                if activeItemId == nil {
                    activeItemId = items.first?.id
                }
            }
        }
    }

    private func canvasCard(_ item: Item, isCenter: Bool) -> some View {
        NavigationLink(value: Route.item(item.id)) {
            ZStack(alignment: .bottom) {
                RemoteImage(path: item.poster, placeholder: "film")
                    .aspectRatio(2 / 3, contentMode: .fill)
                    .frame(width: cardWidth, height: cardHeight)
                    .clipShape(RoundedRectangle(cornerRadius: 28, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: 28, style: .continuous)
                            .strokeBorder(Color.white.opacity(isCenter ? 0.16 : 0.08), lineWidth: 1)
                    )

                // Bottom subtle scrim for contrast
                LinearGradient(
                    colors: [.clear, .black.opacity(0.65)],
                    startPoint: .center,
                    endPoint: .bottom
                )
                .clipShape(RoundedRectangle(cornerRadius: 28, style: .continuous))
                .allowsHitTesting(false)

                // Centered Bottom Play Pill (like in reference image)
                Button {
                    #if os(iOS)
                    UIImpactFeedbackGenerator(style: .medium).impactOccurred()
                    #endif
                    let start = item.resumeSec > 0 ? item.resumeSec : 0
                    model.play(PlayRequest(itemId: item.id, startSec: start), in: playbackScene)
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: "play.fill")
                            .font(.system(size: 10, weight: .bold))
                            .foregroundStyle(Color.orange)
                        Text(item.resumeSec > 0 ? "Resume" : "Play")
                            .font(.caption.weight(.bold))
                            .foregroundStyle(.white)
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 7)
                    .background(
                        Capsule()
                            .fill(Color.black.opacity(0.70))
                            .overlay(Capsule().strokeBorder(Color.white.opacity(0.18), lineWidth: 0.8))
                    )
                }
                .buttonStyle(.plain)
                .padding(.bottom, 16)
                .opacity(isCenter ? 1.0 : 0.0)
                .animation(.easeInOut(duration: 0.2), value: isCenter)
            }
            .contentShape(RoundedRectangle(cornerRadius: 28, style: .continuous))
            .shadow(
                color: Color.black.opacity(isCenter ? 0.65 : 0.35),
                radius: isCenter ? 22 : 10,
                y: isCenter ? 12 : 5
            )
        }
        .buttonStyle(.plain)
    }
}

/// Backward compatibility alias
typealias OrganicFannedDeckView = PeekingCanvasDeckView

struct HomeView: View {
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene

    private var featuredItems: [Item] {
        let library = connection.library
        var list: [Item] = []
        for item in library.continueWatching {
            if !list.contains(where: { $0.id == item.id }) {
                list.append(item)
            }
        }
        for movie in library.recentMovies {
            if !list.contains(where: { $0.id == movie.id }) {
                list.append(movie)
            }
            if list.count >= 8 { break }
        }
        if list.isEmpty {
            list = Array(library.movies.prefix(5))
        }
        return list
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    #if os(iOS)
                    HStack {
                        PlyLogo(size: 24)
                        Spacer()
                    }
                    .padding(.horizontal, 20)
                    .padding(.top, 4)
                    #elseif os(tvOS)
                    HStack(spacing: 12) {
                        PlyLogo(size: 36)
                        Text("Home")
                            .font(.title2.weight(.medium))
                            .foregroundStyle(.secondary)
                        Spacer()
                    }
                    .padding(.horizontal)
                    .padding(.top, 8)
                    #endif

                    if !featuredItems.isEmpty {
                        PeekingCanvasDeckView(items: featuredItems)
                            .frame(maxWidth: .infinity, alignment: .center)
                            .padding(.top, 4)
                            .padding(.bottom, 6)
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
                .padding(.top)
                #if os(iOS)
                .padding(.bottom, 74)
                #else
                .padding(.bottom)
                #endif
            }
            .overlay { LibraryStatus(library: library, isEmpty: library.movies.isEmpty && library.shows.isEmpty) }
            .plyRefreshable { await library.reload() }
            #if os(iOS)
            .toolbar(.hidden, for: .navigationBar)
            #endif
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
    @State private var selectedGenre: String? = nil
    @State private var selectedDecade: DecadeFilter = .all
    @State private var only4K = false
    @State private var onlyHDR = false
    @State private var onlyUnwatched = false
    #if os(iOS)
    @State private var isScrubberVisible = false
    @State private var scrubProgress: CGFloat = 0
    @State private var currentScrubLabel = ""
    @State private var hideTask: Task<Void, Never>? = nil
    #endif

    private var availableGenres: [String] {
        let all = connection.library.movies.flatMap { $0.genres ?? [] }
        return Array(Set(all)).sorted()
    }

    private var hasActiveFilters: Bool {
        selectedGenre != nil || selectedDecade != .all || only4K || onlyHDR || onlyUnwatched
    }

    private func resetFilters() {
        selectedGenre = nil
        selectedDecade = .all
        only4K = false
        onlyHDR = false
        onlyUnwatched = false
    }

    private var filteredMovies: [Item] {
        var list = connection.library.movies
        if !query.isEmpty {
            list = list.filter { $0.title.localizedStandardContains(query) }
        }
        if let selectedGenre {
            list = list.filter { $0.genres?.contains(selectedGenre) == true }
        }
        if selectedDecade != .all {
            list = list.filter { selectedDecade.matches(year: $0.year) }
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

    private func label(for movie: Item) -> String {
        switch sort {
        case .title:
            return alphabetLetter(of: movie.sortTitle ?? movie.title)
        case .year:
            return movie.year.map(String.init) ?? "#"
        case .rating:
            return movie.rating.map { "★\($0.label)" } ?? "•"
        case .added, .duration:
            return alphabetLetter(of: movie.sortTitle ?? movie.title)
        }
    }

    #if os(iOS)
    private func handleScroll(offset: CGFloat) {
        guard filteredMovies.count >= 8 else { return }
        if !isScrubberVisible {
            withAnimation(.easeInOut(duration: 0.2)) {
                isScrubberVisible = true
            }
        }
        let rows = CGFloat((filteredMovies.count + 2) / 3)
        let estHeight = max(1, rows * 210)
        let p = max(0, min(1, -offset / estHeight))
        scrubProgress = p
        let idx = max(0, min(filteredMovies.count - 1, Int(p * CGFloat(filteredMovies.count - 1))))
        currentScrubLabel = label(for: filteredMovies[idx])

        hideTask?.cancel()
        hideTask = Task {
            try? await Task.sleep(nanoseconds: 1_800_000_000)
            if !Task.isCancelled {
                withAnimation(.easeOut(duration: 0.35)) {
                    isScrubberVisible = false
                }
            }
        }
    }
    #endif

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ZStack(alignment: .trailing) {
                    ScrollView {
                        #if os(iOS)
                        GeometryReader { geo in
                            Color.clear.preference(
                                key: ScrollOffsetPreferenceKey.self,
                                value: geo.frame(in: .named("MoviesScroll")).minY
                            )
                        }
                        .frame(height: 0)
                        #endif

                        #if os(tvOS)
                        HStack(spacing: 8) {
                            Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                            TextField("Search films", text: $query)
                        }
                        .font(.headline)
                        .padding(.horizontal)
                        .padding(.top, 10)
                        #elseif os(iOS)
                        HStack {
                            PlyLogo(size: 24)
                            Spacer()
                        }
                        .padding(.horizontal)
                        .padding(.top, 4)
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

                                if let genre = selectedGenre {
                                    ActiveFilterChip(title: genre) {
                                        selectedGenre = nil
                                    }
                                }

                                if selectedDecade != .all {
                                    ActiveFilterChip(title: selectedDecade.rawValue) {
                                        selectedDecade = .all
                                    }
                                }

                                FilterChip(title: "4K", isOn: $only4K)
                                FilterChip(title: "HDR", isOn: $onlyHDR)
                                FilterChip(title: "Unwatched", isOn: $onlyUnwatched)

                                if hasActiveFilters {
                                    Button("Clear all") {
                                        resetFilters()
                                    }
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                                }
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
                                .id(movie.id)
                            }
                        }
                        .padding()
                        #if os(iOS)
                        .padding(.bottom, 74)
                        #endif
                    }
                    .coordinateSpace(name: "MoviesScroll")
                    #if os(iOS)
                    .onPreferenceChange(ScrollOffsetPreferenceKey.self) { offset in
                        handleScroll(offset: offset)
                    }
                    #endif

                    #if os(iOS)
                    if filteredMovies.count >= 8 {
                        FastScrollScrubber(
                            itemCount: filteredMovies.count,
                            currentLabel: currentScrubLabel,
                            onScrub: { index in
                                let movie = filteredMovies[index]
                                currentScrubLabel = label(for: movie)
                                proxy.scrollTo(movie.id, anchor: .top)
                            },
                            isVisible: $isScrubberVisible,
                            progress: $scrubProgress
                        )
                    }
                    #endif
                }
            }
            .overlay {
                if (!query.isEmpty || hasActiveFilters) && filteredMovies.isEmpty {
                    ContentUnavailableView.search(text: query.isEmpty ? "filtered films" : query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.movies.isEmpty)
                }
            }
            .plySearchable($query, prompt: "Films")
            .plyRefreshable { await library.reload() }
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Section("Sort By") {
                            Picker("Sort", selection: $sort) {
                                ForEach(MovieSort.allCases) { Text($0.rawValue).tag($0) }
                            }
                        }

                        if !availableGenres.isEmpty {
                            Section("Genre") {
                                Picker("Genre", selection: $selectedGenre) {
                                    Text("All Genres").tag(String?.none)
                                    ForEach(availableGenres, id: \.self) { genre in
                                        Text(genre).tag(Optional(genre))
                                    }
                                }
                            }
                        }

                        Section("Release Era") {
                            Picker("Era", selection: $selectedDecade) {
                                ForEach(DecadeFilter.allCases) { Text($0.rawValue).tag($0) }
                            }
                        }

                        Section("Attributes") {
                            Toggle("4K UHD", isOn: $only4K)
                            Toggle("HDR", isOn: $onlyHDR)
                            Toggle("Unwatched", isOn: $onlyUnwatched)
                        }

                        if hasActiveFilters {
                            Section {
                                Button(role: .destructive) {
                                    resetFilters()
                                } label: {
                                    Label("Reset Filters", systemImage: "arrow.counterclockwise")
                                }
                            }
                        }
                    } label: {
                        FilterButtonLabel(hasActive: hasActiveFilters)
                    }
                }
                #endif
            }
            #if os(iOS)
            .navigationTitle("")
            .plyInlineTitle()
            #else
            .navigationTitle("Films")
            #endif
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
    @State private var selectedDecade: DecadeFilter = .all
    @State private var onlyUnwatched = false
    #if os(iOS)
    @State private var isScrubberVisible = false
    @State private var scrubProgress: CGFloat = 0
    @State private var currentScrubLabel = ""
    @State private var hideTask: Task<Void, Never>? = nil
    #endif

    private var hasActiveFilters: Bool {
        selectedDecade != .all || onlyUnwatched
    }

    private func resetFilters() {
        selectedDecade = .all
        onlyUnwatched = false
    }

    private var filteredShows: [ShowSummary] {
        var list = connection.library.shows
        if !query.isEmpty {
            list = list.filter { $0.show.localizedStandardContains(query) }
        }
        if selectedDecade != .all {
            list = list.filter { selectedDecade.matches(year: $0.year) }
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

    private func label(for show: ShowSummary) -> String {
        return alphabetLetter(of: show.sortTitle ?? show.show)
    }

    #if os(iOS)
    private func handleScroll(offset: CGFloat) {
        guard filteredShows.count >= 8 else { return }
        if !isScrubberVisible {
            withAnimation(.easeInOut(duration: 0.2)) {
                isScrubberVisible = true
            }
        }
        let rows = CGFloat((filteredShows.count + 2) / 3)
        let estHeight = max(1, rows * 210)
        let p = max(0, min(1, -offset / estHeight))
        scrubProgress = p
        let idx = max(0, min(filteredShows.count - 1, Int(p * CGFloat(filteredShows.count - 1))))
        currentScrubLabel = label(for: filteredShows[idx])

        hideTask?.cancel()
        hideTask = Task {
            try? await Task.sleep(nanoseconds: 1_800_000_000)
            if !Task.isCancelled {
                withAnimation(.easeOut(duration: 0.35)) {
                    isScrubberVisible = false
                }
            }
        }
    }
    #endif

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ZStack(alignment: .trailing) {
                    ScrollView {
                        #if os(iOS)
                        GeometryReader { geo in
                            Color.clear.preference(
                                key: ScrollOffsetPreferenceKey.self,
                                value: geo.frame(in: .named("ShowsScroll")).minY
                            )
                        }
                        .frame(height: 0)
                        #endif

                        #if os(tvOS)
                        HStack(spacing: 8) {
                            Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                            TextField("Search series", text: $query)
                        }
                        .font(.headline)
                        .padding(.horizontal)
                        .padding(.top, 10)
                        #elseif os(iOS)
                        HStack {
                            PlyLogo(size: 24)
                            Spacer()
                        }
                        .padding(.horizontal)
                        .padding(.top, 4)
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

                                if selectedDecade != .all {
                                    ActiveFilterChip(title: selectedDecade.rawValue) {
                                        selectedDecade = .all
                                    }
                                }

                                FilterChip(title: "Unwatched", isOn: $onlyUnwatched)

                                if hasActiveFilters {
                                    Button("Clear all") {
                                        resetFilters()
                                    }
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                                }
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
                                .id(show.show)
                            }
                        }
                        .padding()
                        #if os(iOS)
                        .padding(.bottom, 74)
                        #endif
                    }
                    .coordinateSpace(name: "ShowsScroll")
                    #if os(iOS)
                    .onPreferenceChange(ScrollOffsetPreferenceKey.self) { offset in
                        handleScroll(offset: offset)
                    }
                    #endif

                    #if os(iOS)
                    if filteredShows.count >= 8 {
                        FastScrollScrubber(
                            itemCount: filteredShows.count,
                            currentLabel: currentScrubLabel,
                            onScrub: { index in
                                let show = filteredShows[index]
                                currentScrubLabel = label(for: show)
                                proxy.scrollTo(show.show, anchor: .top)
                            },
                            isVisible: $isScrubberVisible,
                            progress: $scrubProgress
                        )
                    }
                    #endif
                }
            }
            .overlay {
                if (!query.isEmpty || hasActiveFilters) && filteredShows.isEmpty {
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
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Section("Sort By") {
                            Picker("Sort", selection: $sort) {
                                ForEach(ShowSort.allCases) { Text($0.rawValue).tag($0) }
                            }
                        }

                        Section("Release Era") {
                            Picker("Era", selection: $selectedDecade) {
                                ForEach(DecadeFilter.allCases) { Text($0.rawValue).tag($0) }
                            }
                        }

                        Section("Attributes") {
                            Toggle("Unwatched Only", isOn: $onlyUnwatched)
                        }

                        if hasActiveFilters {
                            Section {
                                Button(role: .destructive) {
                                    resetFilters()
                                } label: {
                                    Label("Reset Filters", systemImage: "arrow.counterclockwise")
                                }
                            }
                        }
                    } label: {
                        FilterButtonLabel(hasActive: hasActiveFilters)
                    }
                }
                #endif
            }
            #if os(iOS)
            .navigationTitle("")
            .plyInlineTitle()
            #else
            .navigationTitle("Series")
            #endif
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
enum DecadeFilter: String, CaseIterable, Identifiable {
    case all = "All Eras"
    case twenties = "2020s"
    case tens = "2010s"
    case zeros = "2000s"
    case nineties = "1990s"
    case eighties = "1980s"
    case classic = "Pre-1980"

    var id: String { rawValue }

    func matches(year: Int?) -> Bool {
        guard let year else { return false }
        switch self {
        case .all: return true
        case .twenties: return year >= 2020
        case .tens: return year >= 2010 && year < 2020
        case .zeros: return year >= 2000 && year < 2010
        case .nineties: return year >= 1990 && year < 2000
        case .eighties: return year >= 1980 && year < 1990
        case .classic: return year < 1980
        }
    }
}

/// An active filter indicator chip with a quick dismissal '✕' button.
struct ActiveFilterChip: View {
    let title: String
    let onRemove: () -> Void

    var body: some View {
        Button(action: onRemove) {
            HStack(spacing: 4) {
                Text(title)
                Image(systemName: "xmark")
                    .font(.system(size: 9, weight: .bold))
            }
            .font(.footnote.weight(.medium))
            #if os(iOS)
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(Color.orange.opacity(0.18), in: .capsule)
            .overlay(
                Capsule().strokeBorder(Color.orange.opacity(0.4), lineWidth: 0.8)
            )
            .foregroundStyle(Color.orange)
            #else
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(Color.orange.opacity(0.3), in: .capsule)
            .foregroundStyle(Color.white)
            #endif
        }
        .buttonStyle(.plain)
    }
}

/// A glass capsule label for the top-trailing Filter button in library navigation bars.
struct FilterButtonLabel: View {
    let hasActive: Bool

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: hasActive ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease.circle")
                .font(.system(size: 14, weight: .semibold))
            Text("Filter")
                .font(.subheadline.weight(.medium))
        }
        .foregroundStyle(hasActive ? Color.orange : Color.white)
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .background(
            Capsule()
                .fill(hasActive ? Color.orange.opacity(0.18) : Color.white.opacity(0.08))
                .overlay(
                    Capsule()
                        .strokeBorder(hasActive ? Color.orange.opacity(0.4) : Color.white.opacity(0.12), lineWidth: 0.8)
                )
        )
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
