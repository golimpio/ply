import SwiftUI
/// An organic 3D fanned deck spotlighting featured, in-progress, and recent items on Home.
struct OrganicFannedDeckView: View {
    let items: [Item]
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var selectedIndex: Int = 0
    @State private var dragOffset: CGFloat = 0
    @State private var isDragging: Bool = false
    @State private var dragDirectionLocked: Bool = false

    private var count: Int { items.count }

    private var activeItem: Item? {
        guard !items.isEmpty, selectedIndex < items.count else { return nil }
        return items[selectedIndex]
    }

    private var cardWidth: CGFloat {
        #if os(tvOS)
        280
        #else
        215
        #endif
    }

    private var cardHeight: CGFloat {
        #if os(tvOS)
        400
        #else
        315
        #endif
    }

    private var horizontalOffset: CGFloat {
        #if os(tvOS)
        70
        #else
        65
        #endif
    }

    var body: some View {
        if count > 0, let active = activeItem {
            VStack(spacing: 16) {
                // The Fanned Deck Stage - Perfectly centered horizontally across the full screen width
                ZStack {
                    // Ambient backlight behind the active card
                    RadialGradient(
                        colors: [
                            Color.orange.opacity(0.55),
                            Color.red.opacity(0.2),
                            Color.clear
                        ],
                        center: .center,
                        startRadius: 0,
                        endRadius: 150
                    )
                    .frame(width: cardWidth * 1.4, height: cardHeight * 0.7)
                    .blur(radius: 36)
                    .offset(x: dragOffset * 0.45, y: 15)

                    // Left Tilted Card (-10°)
                    if count > 1 {
                        let leftIdx = (selectedIndex - 1 + count) % count
                        let progress = min(1, max(-1, dragOffset / 160))
                        let xOffset = -horizontalOffset + (progress > 0 ? progress * horizontalOffset : dragOffset * 0.25)
                        let rot = -10.0 + (progress > 0 ? progress * 10.0 : 0)
                        let scale = 0.88 + (progress > 0 ? progress * 0.12 : 0)
                        let opac = 0.75 + (progress > 0 ? progress * 0.25 : max(-0.75, progress * 0.75))

                        deckCard(items[leftIdx], isCenter: false)
                            .rotationEffect(.degrees(rot))
                            .offset(x: xOffset, y: 12)
                            .scaleEffect(scale)
                            .opacity(max(0, opac))
                            .brightness(-0.12)
                            .zIndex(progress > 0.4 ? 10 : 2)
                            .onTapGesture {
                                withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) {
                                    selectedIndex = leftIdx
                                }
                                #if os(iOS)
                                UISelectionFeedbackGenerator().selectionChanged()
                                #endif
                            }
                    }

                    // Right Tilted Card (+10°)
                    if count > 1 {
                        let rightIdx = (selectedIndex + 1) % count
                        let progress = min(1, max(-1, dragOffset / 160))
                        let xOffset = horizontalOffset + (progress < 0 ? progress * horizontalOffset : dragOffset * 0.25)
                        let rot = 10.0 + (progress < 0 ? progress * 10.0 : 0)
                        let scale = 0.88 + (progress < 0 ? -progress * 0.12 : 0)
                        let opac = 0.75 + (progress < 0 ? -progress * 0.25 : min(0, -progress * 0.75))

                        deckCard(items[rightIdx], isCenter: false)
                            .rotationEffect(.degrees(rot))
                            .offset(x: xOffset, y: 12)
                            .scaleEffect(scale)
                            .opacity(max(0, opac))
                            .brightness(-0.12)
                            .zIndex(progress < -0.4 ? 10 : 2)
                            .onTapGesture {
                                withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) {
                                    selectedIndex = rightIdx
                                }
                                #if os(iOS)
                                UISelectionFeedbackGenerator().selectionChanged()
                                #endif
                            }
                    }

                    // Center Elevated Card (0°)
                    let progress = min(1, max(-1, dragOffset / 160))
                    let centerRot = progress * 10.0
                    let centerScale = 1.0 - abs(progress) * 0.12
                    let centerOpacity = 1.0 - abs(progress) * 0.2

                    deckCard(active, isCenter: true)
                        .rotationEffect(.degrees(centerRot))
                        .offset(x: dragOffset * 0.7, y: -4)
                        .scaleEffect(centerScale)
                        .opacity(centerOpacity)
                        .zIndex(abs(progress) > 0.4 ? 2 : 10)
                }
                .frame(maxWidth: .infinity, alignment: .center)
                .frame(height: cardHeight + 40)
                .contentShape(Rectangle())
                #if os(iOS)
                .highPriorityGesture(
                    DragGesture(minimumDistance: 8)
                        .onChanged { value in
                            if !dragDirectionLocked {
                                if abs(value.translation.width) > abs(value.translation.height) * 0.6 {
                                    dragDirectionLocked = true
                                    isDragging = true
                                }
                            }
                            if dragDirectionLocked {
                                dragOffset = value.translation.width
                            }
                        }
                        .onEnded { value in
                            dragDirectionLocked = false
                            let velocity = value.predictedEndTranslation.width - value.translation.width
                            let threshold: CGFloat = 45
                            if value.translation.width < -threshold || velocity < -80 {
                                withAnimation(.spring(response: 0.34, dampingFraction: 0.8)) {
                                    selectedIndex = (selectedIndex + 1) % count
                                    dragOffset = 0
                                }
                                UISelectionFeedbackGenerator().selectionChanged()
                            } else if value.translation.width > threshold || velocity > 80 {
                                withAnimation(.spring(response: 0.34, dampingFraction: 0.8)) {
                                    selectedIndex = (selectedIndex - 1 + count) % count
                                    dragOffset = 0
                                }
                                UISelectionFeedbackGenerator().selectionChanged()
                            } else {
                                withAnimation(.spring(response: 0.34, dampingFraction: 0.8)) {
                                    dragOffset = 0
                                }
                            }
                            DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
                                isDragging = false
                            }
                        }
                )
                #endif

                // Title & Metadata
                VStack(spacing: 6) {
                    if let year = active.year {
                        Text(String(year))
                            .font(.caption.weight(.medium))
                            .foregroundStyle(.secondary)
                    }

                    Text(active.isEpisode ? (active.show ?? active.title) : active.title)
                        .font(.system(size: 24, weight: .bold, design: .serif))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                        .multilineTextAlignment(.center)

                    // Metadata Chips Row
                    HStack(spacing: 8) {
                        if let genres = active.genres, let first = genres.first {
                            Text(first)
                                .font(.caption2.weight(.medium))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .background(Color.white.opacity(0.08), in: Capsule())
                        } else if active.isEpisode, let code = active.episodeCode {
                            Text(code)
                                .font(.caption2.weight(.medium))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .background(Color.white.opacity(0.08), in: Capsule())
                        }

                        if active.durationSec > 0 {
                            Text(Format.runtime(active.durationSec))
                                .font(.caption2.weight(.medium))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .background(Color.white.opacity(0.08), in: Capsule())
                        }

                        if let rating = active.rating {
                            HStack(spacing: 3) {
                                Image(systemName: "star.fill")
                                    .font(.system(size: 9))
                                    .foregroundStyle(Color.yellow)
                                Text(rating.label)
                                    .font(.caption2.weight(.bold))
                                    .foregroundStyle(.primary)
                            }
                            .padding(.horizontal, 8)
                            .padding(.vertical, 3)
                            .background(Color.yellow.opacity(0.12), in: Capsule())
                        }
                    }

                    // Pagination Dots
                    if count > 1 {
                        HStack(spacing: 6) {
                            ForEach(0..<min(count, 8), id: \.self) { idx in
                                if idx == selectedIndex {
                                    Capsule()
                                        .fill(Color.orange)
                                        .frame(width: 18, height: 5)
                                } else {
                                    Circle()
                                        .fill(Color.white.opacity(0.25))
                                        .frame(width: 5, height: 5)
                                        .onTapGesture {
                                            withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) {
                                                selectedIndex = idx
                                            }
                                            #if os(iOS)
                                            UISelectionFeedbackGenerator().selectionChanged()
                                            #endif
                                        }
                                }
                            }
                        }
                        .padding(.top, 4)
                    }
                }
                .padding(.horizontal, 20)
                .frame(maxWidth: .infinity, alignment: .center)
            }
            .frame(maxWidth: .infinity, alignment: .center)
        }
    }

    private func deckCard(_ item: Item, isCenter: Bool) -> some View {
        ZStack(alignment: .bottomTrailing) {
            if isCenter {
                NavigationLink(value: Route.item(item.id)) {
                    cardPoster(item, isCenter: true)
                }
                .buttonStyle(.plain)
                .disabled(isDragging)
            } else {
                cardPoster(item, isCenter: false)
            }

            // Bottom gradient on center card for contrast
            if isCenter {
                LinearGradient(
                    colors: [.clear, .black.opacity(0.65)],
                    startPoint: .center,
                    endPoint: .bottom
                )
                .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
                .allowsHitTesting(false)

                // Circular Play Pill on Center Card
                Button {
                    let start = item.resumeSec > 0 ? item.resumeSec : 0
                    model.play(PlayRequest(itemId: item.id, startSec: start), in: playbackScene)
                } label: {
                    ZStack {
                        Circle()
                            .fill(
                                LinearGradient(
                                    colors: [Color.orange, Color.red.opacity(0.85)],
                                    startPoint: .topLeading,
                                    endPoint: .bottomTrailing
                                )
                            )
                            .frame(width: 44, height: 44)
                            .shadow(color: Color.orange.opacity(0.55), radius: 8, y: 4)

                        Image(systemName: "play.fill")
                            .font(.system(size: 16, weight: .bold))
                            .foregroundStyle(.white)
                            .offset(x: 1)
                    }
                }
                .buttonStyle(.plain)
                .padding(14)
                .hoverEffect()
                .disabled(isDragging)
            }
        }
    }

    private func cardPoster(_ item: Item, isCenter: Bool) -> some View {
        RemoteImage(path: item.poster, placeholder: "film")
            .aspectRatio(2 / 3, contentMode: .fill)
            .frame(width: cardWidth, height: cardHeight)
            .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .strokeBorder(Color.white.opacity(isCenter ? 0.2 : 0.08), lineWidth: 1)
            )
            .shadow(color: .black.opacity(isCenter ? 0.8 : 0.5), radius: isCenter ? 20 : 10, y: isCenter ? 10 : 5)
    }
}

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

                    if !featuredItems.isEmpty {
                        OrganicFannedDeckView(items: featuredItems)
                            .frame(maxWidth: .infinity, alignment: .center)
                            .padding(.top, 6)
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

    private var availableLetters: [String] {
        let letters = Set(filteredMovies.map { alphabetLetter(of: $0.sortTitle ?? $0.title) })
        return letters.sorted { a, b in
            if a == "#" { return true }
            if b == "#" { return false }
            return a < b
        }
    }

    private var firstMovieIdByLetter: [String: Int64] {
        var map: [String: Int64] = [:]
        for movie in filteredMovies {
            let letter = alphabetLetter(of: movie.sortTitle ?? movie.title)
            if map[letter] == nil {
                map[letter] = movie.id
            }
        }
        return map
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ZStack(alignment: .trailing) {
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
                                .id(movie.id)
                            }
                        }
                        .padding()
                        .padding(.trailing, sort == .title && availableLetters.count > 1 ? 16 : 0)
                        #if os(iOS)
                        .padding(.bottom, 74)
                        #endif
                    }

                    #if os(iOS)
                    if sort == .title && availableLetters.count > 1 {
                        AlphabetJumpRail(letters: availableLetters) { letter in
                            if let id = firstMovieIdByLetter[letter] {
                                proxy.scrollTo(id, anchor: .top)
                            }
                        }
                        .padding(.trailing, 2)
                    }
                    #endif
                }
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
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarLeading) {
                    PlyBrand(size: 28)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Text("Films")
                        .font(.title3.bold())
                        .foregroundStyle(.primary)
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

    private var availableLetters: [String] {
        let letters = Set(filteredShows.map { alphabetLetter(of: $0.sortTitle ?? $0.show) })
        return letters.sorted { a, b in
            if a == "#" { return true }
            if b == "#" { return false }
            return a < b
        }
    }

    private var firstShowNameByLetter: [String: String] {
        var map: [String: String] = [:]
        for show in filteredShows {
            let letter = alphabetLetter(of: show.sortTitle ?? show.show)
            if map[letter] == nil {
                map[letter] = show.show
            }
        }
        return map
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ZStack(alignment: .trailing) {
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
                                .id(show.show)
                            }
                        }
                        .padding()
                        .padding(.trailing, sort == .title && availableLetters.count > 1 ? 16 : 0)
                        #if os(iOS)
                        .padding(.bottom, 74)
                        #endif
                    }

                    #if os(iOS)
                    if sort == .title && availableLetters.count > 1 {
                        AlphabetJumpRail(letters: availableLetters) { letter in
                            if let name = firstShowNameByLetter[letter] {
                                proxy.scrollTo(name, anchor: .top)
                            }
                        }
                        .padding(.trailing, 2)
                    }
                    #endif
                }
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
            .toolbar {
                #if os(iOS)
                ToolbarItem(placement: .topBarLeading) {
                    PlyBrand(size: 28)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Text("Series")
                        .font(.title3.bold())
                        .foregroundStyle(.primary)
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
