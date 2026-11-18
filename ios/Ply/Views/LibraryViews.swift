import SwiftUI

/// The Spatial Canvas & Peeking Deck spotlighting featured, in-progress, and recent items on Home.
struct PeekingCanvasDeckView: View {
    let items: [Item]
    #if os(iOS)
    /// Where the centre poster's zoom into its film starts (`plyHeroDestination`); nil pushes `Route.item`.
    var zoomNamespace: Namespace.ID? = nil
    #endif
    var onActiveItemChange: ((Item) -> Void)? = nil

    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var activeItemId: Int64?
    @State private var playImpactTrigger = 0
    #if os(iOS)
    /// The poster a fan drag started on, while the finger is down; the deck follows the finger from there.
    /// Its id, not its place: Continue Watching can reorder the list mid-drag.
    @State private var dragAnchor: Int64?
    @State private var dragTranslation: CGFloat = 0
    #endif

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
        272
        #endif
    }

    private var cardHeight: CGFloat {
        #if os(tvOS)
        480
        #else
        408
        #endif
    }

    private var cornerRadius: CGFloat { PlyDesign.heroCardCornerRadius }

    var body: some View {
        if !items.isEmpty, let active = activeItem {
            VStack(spacing: PlyDesign.deckMetadataSpacing) {
                #if os(iOS)
                fanDeck(active: active)
                    .frame(height: cardHeight + PlyDesign.deckCardsVerticalPadding)
                #else
                GeometryReader { proxy in
                    let screenWidth = proxy.size.width
                    let sideInset = max(16, (screenWidth - cardWidth) / 2)

                    ScrollView(.horizontal, showsIndicators: false) {
                        LazyHStack(spacing: 16) {
                            ForEach(items) { item in
                                canvasCard(item, isCenter: item.id == active.id)
                                    .id(item.id)
                                    .scrollTransition(.interactive, axis: .horizontal) { content, phase in
                                        let dist = min(1.0, abs(phase.value))
                                        return content
                                            .scaleEffect(1.0 - dist * 0.14)
                                            .offset(y: dist * 16)
                                            .saturation(1.0 - dist * 0.55)
                                            .blur(radius: dist * 2.2)
                                            .opacity(1.0 - dist * 0.42)
                                    }
                            }
                        }
                        .scrollTargetLayout()
                    }
                    .scrollTargetBehavior(.viewAligned)
                    .safeAreaPadding(.horizontal, sideInset)
                    .scrollPosition(id: $activeItemId)
                    .scrollClipDisabled()
                }
                .frame(height: cardHeight + PlyDesign.deckCardsVerticalPadding)
                #endif

                // Position indicator + Editorial Title & Wide-Tracked Metadata Block
                VStack(spacing: PlyDesign.deckMetadataSpacing) {
                    if items.count > 1 {
                        deckPageIndicator(active: active)
                            .background(rowFrameReporter("indicator"))
                            .padding(.bottom, PlyDesign.deckSectionGap - PlyDesign.deckMetadataSpacing)
                    }

                    let microSpecs = Format.facts(
                        active.year.map(String.init),
                        active.genres?.first ?? active.episodeCode,
                        active.durationSec > 0 ? Format.runtime(active.durationSec) : nil
                    )
                    if !microSpecs.isEmpty {
                        Text(microSpecs.uppercased())
                            .plyMicroSpecsStyle(opacity: 0.60)
                            .background(rowFrameReporter("specs"))
                    }

                    Text(active.isEpisode ? (active.show ?? active.title) : active.title)
                        .plyHeroTitleStyle(size: 28)
                        .lineLimit(1)
                        .multilineTextAlignment(.center)
                        .background(rowFrameReporter("title"))

                    // Subtle Glass Telemetry Pills (Rating + Technical Badge)
                    if !active.versionLabel.isEmpty || active.rating != nil {
                        HStack(spacing: 8) {
                            if !active.versionLabel.isEmpty {
                                PlyGlassPillBadge(text: active.versionLabel)
                            }

                            if let rating = active.rating {
                                PlyGlassPillBadge(text: rating.label, systemImage: "star.fill", textOpacity: 1.0)
                            }
                        }
                        .padding(.top, 2)
                        .background(rowFrameReporter("pills"))
                    }
                }
                .animation(.spring(response: 0.38, dampingFraction: 0.86), value: active.id)
                .padding(.horizontal, 20)
                .frame(maxWidth: .infinity, alignment: .center)
            }
            .frame(maxWidth: .infinity, alignment: .center)
            #if os(iOS)
            .sensoryFeedback(.selection, trigger: activeItemId)
            .sensoryFeedback(.impact(weight: .medium, intensity: 0.85), trigger: playImpactTrigger)
            #endif
            .onAppear {
                if activeItemId == nil {
                    activeItemId = items.first?.id
                }
                onActiveItemChange?(active)
            }
            .onChange(of: activeItemId) { _, _ in
                if let current = activeItem {
                    onActiveItemChange?(current)
                }
            }
        }
    }

    #if os(iOS)
    private var activeIndex: Int {
        items.firstIndex { $0.id == activeItemId } ?? 0
    }

    /// One card of drag: the gap between neighbouring posters, so the centre one moves with the finger.
    private var fanSpacing: CGFloat { CGFloat(FanPose.spacing(cardWidth: Double(cardWidth))) }

    /// Where the deck is, in cards: under the finger while dragging, else on the active card.
    private var fanPosition: Double {
        guard let dragAnchor else { return Double(activeIndex) }
        let anchor = items.firstIndex { $0.id == dragAnchor } ?? activeIndex
        return FanDeckMotion.position(
            anchor: anchor, translation: dragTranslation, spacing: fanSpacing, count: items.count)
    }

    /// The posters fanned like a hand of cards, the neighbours dimmed (`FanDeck.swift`).
    private func fanDeck(active: Item) -> some View {
        let position = fanPosition
        return ZStack {
            ForEach(Array(items.enumerated()), id: \.element.id) { index, item in
                canvasCard(item, isCenter: item.id == active.id)
                    .modifier(FanPlacement(
                        distance: Double(index) - position, cardWidth: cardWidth, cornerRadius: cornerRadius))
            }
        }
        .frame(maxWidth: .infinity)
        .contentShape(Rectangle())
        .gesture(HorizontalPan(onChange: fanDragChanged, onEnd: fanDragEnded))
        // VoiceOver's three-finger swipe, which the scroll view this replaced answered.
        .accessibilityScrollAction { edge in
            let delta = edge == .trailing ? 1 : edge == .leading ? -1 : 0
            let next = FanDeckMotion.step(from: activeIndex, by: delta, count: items.count)
            withAnimation(FanDeckMotion.settle(initialVelocity: 0)) { activeItemId = items[next].id }
        }
    }

    /// The card under the centre becomes active as it passes, so the details and the haptic tick follow the finger.
    private func fanDragChanged(_ translation: CGFloat) {
        withAnimation(FanDeckMotion.follow) {
            if dragAnchor == nil { dragAnchor = activeItemId ?? items.first?.id }
            dragTranslation = translation
            let nearest = min(max(Int(fanPosition.rounded()), 0), items.count - 1)
            if items[nearest].id != activeItemId { activeItemId = items[nearest].id }
        }
    }

    private func fanDragEnded(_ translation: CGFloat, _ velocity: CGFloat) {
        guard let dragAnchor else { return }
        let anchor = items.firstIndex { $0.id == dragAnchor } ?? activeIndex
        let position = fanPosition
        let target = FanDeckMotion.target(
            anchor: anchor, translation: translation, velocity: velocity, spacing: fanSpacing, count: items.count)
        let speed = FanDeckMotion.springVelocity(from: position, to: target, velocity: velocity, spacing: fanSpacing)
        withAnimation(FanDeckMotion.settle(initialVelocity: speed)) {
            self.dragAnchor = nil
            dragTranslation = 0
            activeItemId = items[target].id
        }
    }
    #endif

    private func rowFrameReporter(_ id: String) -> some View {
        GeometryReader { geo in
            Color.clear.preference(key: DeckRowFramesKey.self, value: [id: geo.frame(in: .global)])
        }
    }

    @ViewBuilder
    private func deckPageIndicator(active: Item) -> some View {
        HStack(spacing: 6) {
            ForEach(Array(items.prefix(8).enumerated()), id: \.element.id) { _, item in
                if item.id == active.id {
                    Capsule()
                        .fill(LinearGradient.plyAmberHorizontal)
                        .frame(width: 22, height: 5)
                } else {
                    Circle()
                        .fill(Color.white.opacity(0.28))
                        .frame(width: 5.5, height: 5.5)
                        .onTapGesture {
                            withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) {
                                activeItemId = item.id
                            }
                        }
                }
            }
        }
    }

    @ViewBuilder
    private func canvasCard(_ item: Item, isCenter: Bool) -> some View {
        let cardArtwork = RemoteImage(path: item.poster, placeholder: "film")
            .aspectRatio(2 / 3, contentMode: .fill)
            .frame(width: cardWidth, height: cardHeight)
            .clipShape(.rect(cornerRadius: cornerRadius, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                    .strokeBorder(
                        LinearGradient(
                            colors: [.white.opacity(isCenter ? 0.30 : 0.10), .clear],
                            startPoint: .top,
                            endPoint: .bottom
                        ),
                        lineWidth: 1
                    )
            )
            .contentShape(.rect(cornerRadius: cornerRadius, style: .continuous))

        ZStack {
            #if os(iOS)
            if let zoomNamespace {
                // The centre poster opens its film, zooming out of the card; a side poster comes to the centre.
                Group {
                    if isCenter {
                        NavigationLink(value: HeroItemRoute(item: item)) { cardArtwork }
                    } else {
                        Button {
                            withAnimation(.spring(response: 0.38, dampingFraction: 0.84)) {
                                activeItemId = item.id
                            }
                        } label: {
                            cardArtwork
                        }
                    }
                }
                .buttonStyle(.plain)
                .matchedTransitionSource(id: item.id, in: zoomNamespace)
                .accessibilityLabel(item.isEpisode ? (item.show ?? item.title) : item.title)
            } else {
                NavigationLink(value: Route.item(item.id)) { cardArtwork }
                    .buttonStyle(.plain)
            }
            #else
            NavigationLink(value: Route.item(item.id)) { cardArtwork }
                .buttonStyle(.plain)
            #endif

            HeroPlayButton(
                accessibilityLabel: item.resumeSec > 0 ? "Resume" : "Play",
                progress: item.fraction
            ) {
                playImpactTrigger += 1
                let start = item.resumeSec > 0 ? item.resumeSec : 0
                model.play(PlayRequest(itemId: item.id, startSec: start), in: playbackScene)
            }
            .opacity(isCenter ? 1.0 : 0.0)
            .allowsHitTesting(isCenter)
            .accessibilityHidden(!isCenter)
            .animation(.easeInOut(duration: 0.2), value: isCenter)
        }
        .shadow(
            color: Color.black.opacity(isCenter ? 0.44 : 0.22),
            radius: isCenter ? 20 : 10,
            y: isCenter ? 10 : 5
        )
    }
}

/// Preference key reporting global frames for the four metadata rows below the peeking deck poster.
nonisolated struct DeckRowFramesKey: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

/// Backward compatibility alias
typealias OrganicFannedDeckView = PeekingCanvasDeckView

struct HomeView: View {
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene

    @Namespace private var heroZoom
    @State private var activeHeroItem: Item?
    @State private var backdropPalette = Self.launchPalette
    /// The colours the last launch ended on, read once per launch rather than on every rebuild of Home.
    private static let launchPalette = PosterPalette.lastShown()

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

    /// The poster deck: on iOS its centre poster zooms into the film's page.
    private var deck: some View {
        #if os(iOS)
        PeekingCanvasDeckView(items: featuredItems, zoomNamespace: heroZoom) { activeHeroItem = $0 }
        #else
        PeekingCanvasDeckView(items: featuredItems) { activeHeroItem = $0 }
        #endif
    }

    var body: some View {
        let library = connection.library
        let activeArtworkPath = (activeHeroItem ?? featuredItems.first)?.poster
            ?? (activeHeroItem ?? featuredItems.first)?.landscapeImage

        NavigationStack {
            ZStack {
                LivingMeshBackdrop(palette: backdropPalette)

                ScrollView {
                    Self.feedLayout {
                        #if os(tvOS)
                        HStack(spacing: 12) {
                            PlyLogo(size: 36, color: .white)
                            Text("Home")
                                .font(.title2.weight(.medium))
                                .foregroundStyle(.secondary)
                            Spacer()
                        }
                        .padding(.horizontal)
                        .padding(.top, 8)
                        #endif

                        if !featuredItems.isEmpty {
                            deck
                                .frame(maxWidth: .infinity, alignment: .center)
                                .padding(.top, 4)
                        }
                    } shelves: {
                        homeShelves(library)
                    }
                    .padding(.top)
                }
                .plyTabBarBottomClearance()
                .overlay { LibraryStatus(library: library, isEmpty: library.movies.isEmpty && library.shows.isEmpty) }
                .plyRefreshable { await library.reload() }
            }
            .task(id: activeArtworkPath) {
                // Until a poster's colours are in, the backdrop keeps the last launch's (`PosterPalette.lastShown`):
                // no library yet, or artwork that failed to load, must not drop it back to the dark fallback.
                guard let url = connection.api.url(activeArtworkPath) else { return }
                let palette = await PosterPaletteExtractor.shared.palette(for: url)
                // A swipe on to another poster cancels this load: its late colours must not win or be kept.
                guard !Task.isCancelled, palette != .fallback else { return }
                backdropPalette = palette
                palette.remember()
            }
            .task(id: library.generation) {
                await connection.browse.loadHomeGenreRails()
            }
            .plyMainPageLogo(color: .white)
            .plyDestinations()
            #if os(iOS)
            .plyHeroDestination(zoomingFrom: heroZoom)
            #endif
        }
    }

    /// Separates the peeking deck from the first shelf using `PlyDesign.deckSectionGap` so
    /// the gap below the telemetry pills matches the larger gap between the page indicator and specs.
    @ViewBuilder
    static func feedLayout<Deck: View, Shelves: View>(
        @ViewBuilder deck: () -> Deck,
        @ViewBuilder shelves: () -> Shelves
    ) -> some View {
        VStack(alignment: .leading, spacing: PlyDesign.deckSectionGap) {
            deck()
            shelves()
        }
    }

    @ViewBuilder
    private func homeShelves(_ library: LibraryModel) -> some View {
        VStack(alignment: .leading, spacing: PlyDesign.sectionSpacing) {
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

            if library.acclaimedFilms.count >= 2 {
                filmShelf(
                    "World & Auteur Cinema",
                    subtitle: "Godard, Wong Kar-wai, Wenders, Song, Kaurismäki",
                    items: library.acclaimedFilms
                ) { movie in
                    Format.facts(movie.year.map(String.init), movie.rating?.label)
                }
            }

            if library.shortFeatures.count >= 2 {
                filmShelf(
                    "Acclaimed & Under 100 Minutes",
                    subtitle: "Finishes before 22:00",
                    items: library.shortFeatures
                ) { movie in
                    Format.facts(
                        movie.year.map(String.init),
                        movie.durationSec > 0 ? Format.runtime(movie.durationSec) : nil
                    )
                }
            }

            if !library.recentMovies.isEmpty {
                filmShelf(
                    "Recently Added",
                    subtitle: "Latest additions to your archive",
                    items: library.recentMovies
                ) { movie in
                    movie.year.map(String.init)
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

            ForEach(connection.browse.homeGenreRails) { rail in
                Shelf(rail.genre.name, trailing: {
                    NavigationLink(value: Route.genre(rail.genre.id, rail.genre.name)) {
                        Text("All \(rail.genre.count)")
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(.secondary)
                    }
                }) {
                    ForEach(rail.titles.prefix(30)) { card in
                        TitlePoster(card: card, width: shelfCardWidth)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func filmShelf(
        _ title: String,
        subtitle: String? = nil,
        items: [Item],
        caption: @escaping (Item) -> String?
    ) -> some View {
        Shelf(title, subtitle: subtitle) {
            ForEach(items) { movie in
                NavigationLink(value: Route.item(movie.id)) {
                    PosterCard(
                        title: movie.title,
                        caption: caption(movie),
                        image: movie.poster,
                        watched: movie.isWatched,
                        fraction: movie.fraction
                    )
                    .frame(width: shelfCardWidth)
                }
                .buttonStyle(.plain)
            }
        }
    }
}

nonisolated enum MovieSort: String, LibrarySortOption {
    case title = "A to Z"
    case added = "Recently Added"
    case year = "Newest"
    case duration = "Longest"
    case rating = "Rating"

    var id: String { rawValue }
    static let defaultSort: Self = .title
}

struct MoviesView: View {
    @Environment(Connection.self) private var connection
    #if os(tvOS)
    @State private var query = ""
    #endif
    @AppStorage(PlyPreferences.Movies.sortKey) private var sort: MovieSort = .title
    @AppStorage(PlyPreferences.Movies.genreKey) private var selectedGenreRaw = ""
    @AppStorage(PlyPreferences.Movies.decadeKey) private var selectedDecade: DecadeFilter = .all
    @AppStorage(PlyPreferences.Movies.only4KKey) private var only4K = false
    @AppStorage(PlyPreferences.Movies.onlyHDRKey) private var onlyHDR = false
    @AppStorage(PlyPreferences.Movies.onlyUnwatchedKey) private var onlyUnwatched = false
    @State private var scrollOffset: CGFloat = 0

    private var selectedGenre: String? {
        get { selectedGenreRaw.isEmpty ? nil : selectedGenreRaw }
    }

    private var selectedGenreBinding: Binding<String?> {
        Binding(
            get: { selectedGenreRaw.isEmpty ? nil : selectedGenreRaw },
            set: { selectedGenreRaw = $0 ?? "" }
        )
    }

    private var availableGenres: [String] {
        let all = connection.library.movies.flatMap { $0.genres ?? [] }
        return Array(Set(all)).sorted()
    }

    private var activeFilters: [LibraryActiveFilter] {
        var items: [LibraryActiveFilter] = []
        if let genre = selectedGenre {
            items.append(LibraryActiveFilter(id: "genre-\(genre)", title: genre) { selectedGenreRaw = "" })
        }
        if selectedDecade != .all {
            items.append(LibraryActiveFilter(id: "decade", title: selectedDecade.rawValue) { selectedDecade = .all })
        }
        if only4K {
            items.append(LibraryActiveFilter(id: "4k", title: "4K") { only4K = false })
        }
        if onlyHDR {
            items.append(LibraryActiveFilter(id: "hdr", title: "HDR") { onlyHDR = false })
        }
        if onlyUnwatched {
            items.append(LibraryActiveFilter(id: "unwatched", title: "Unwatched") { onlyUnwatched = false })
        }
        return items
    }

    private var hasActiveSelection: Bool {
        sort.hasActiveSelection(filters: activeFilters)
    }

    private func resetControls() {
        sort = .defaultSort
        selectedGenreRaw = ""
        selectedDecade = .all
        only4K = false
        onlyHDR = false
        onlyUnwatched = false
    }

    private var filteredMovies: [Item] {
        var list = connection.library.movies
        #if os(tvOS)
        if !query.isEmpty {
            list = list.filter { $0.title.localizedStandardContains(query) }
        }
        #endif
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
        case .title, .added:
            return alphabetLetter(of: movie.sortTitle ?? movie.title)
        case .year:
            return movie.year.map(String.init) ?? "#"
        case .duration:
            return movie.durationSec > 0 ? Format.runtime(movie.durationSec) : "•"
        case .rating:
            return movie.rating.map { "★\($0.label)" } ?? "•"
        }
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ScrollView {
                    #if os(tvOS)
                    PlySearchField(text: $query, prompt: "Search films")
                        .padding(.horizontal)
                        .padding(.top, 10)
                    #endif

                    LibraryFilterBar(
                        collectionsKind: .films,
                        sort: $sort,
                        activeFilters: activeFilters,
                        onReset: resetControls
                    )

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
                    .padding([.horizontal, .top])
                }
                .plyTrackVerticalScroll($scrollOffset)
                .plyTabBarBottomClearance()
                .plyFastScrollScrubber(
                    itemCount: filteredMovies.count,
                    labelAt: { label(for: filteredMovies[$0]) },
                    titleAt: { filteredMovies[$0].title },
                    onScrollTo: { proxy.scrollTo(filteredMovies[$0].id, anchor: .top) }
                )
            }
            .overlay {
                #if os(tvOS)
                if (!query.isEmpty || !activeFilters.isEmpty) && filteredMovies.isEmpty {
                    ContentUnavailableView.search(text: query.isEmpty ? "filtered films" : query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.movies.isEmpty)
                }
                #else
                if !activeFilters.isEmpty && filteredMovies.isEmpty {
                    ContentUnavailableView.search(text: "filtered films")
                } else {
                    LibraryStatus(library: library, isEmpty: library.movies.isEmpty)
                }
                #endif
            }
            .plyRefreshable { await library.reload() }
            .plyLibraryFilterToolbar(
                collectionsKind: .films,
                scrollOffset: scrollOffset,
                sort: $sort,
                availableGenres: availableGenres,
                selectedGenre: selectedGenreBinding,
                selectedDecade: $selectedDecade,
                attributes: [
                    LibraryFilterAttribute(id: "4k", title: "4K UHD", isOn: $only4K),
                    LibraryFilterAttribute(id: "hdr", title: "HDR", isOn: $onlyHDR),
                    LibraryFilterAttribute(id: "unwatched", title: "Unwatched", isOn: $onlyUnwatched)
                ],
                hasActiveSelection: hasActiveSelection,
                onReset: resetControls
            )
            #if os(iOS)
            .plyMainPageLogo()
            #else
            .navigationTitle("Films")
            #endif
            .plyDestinations()
        }
    }
}

nonisolated enum ShowSort: String, LibrarySortOption {
    case title = "A to Z"
    case added = "Recently Updated"
    case rating = "Rating"
    case episodes = "Most Episodes"

    var id: String { rawValue }
    static let defaultSort: Self = .title
}

struct ShowsView: View {
    @Environment(Connection.self) private var connection
    #if os(tvOS)
    @State private var query = ""
    #endif
    @AppStorage(PlyPreferences.Shows.sortKey) private var sort: ShowSort = .title
    @AppStorage(PlyPreferences.Shows.decadeKey) private var selectedDecade: DecadeFilter = .all
    @AppStorage(PlyPreferences.Shows.onlyUnwatchedKey) private var onlyUnwatched = false
    @State private var scrollOffset: CGFloat = 0

    private var activeFilters: [LibraryActiveFilter] {
        var items: [LibraryActiveFilter] = []
        if selectedDecade != .all {
            items.append(LibraryActiveFilter(id: "decade", title: selectedDecade.rawValue) { selectedDecade = .all })
        }
        if onlyUnwatched {
            items.append(LibraryActiveFilter(id: "unwatched", title: "Unwatched") { onlyUnwatched = false })
        }
        return items
    }

    private var hasActiveSelection: Bool {
        sort.hasActiveSelection(filters: activeFilters)
    }

    private func resetControls() {
        sort = .defaultSort
        selectedDecade = .all
        onlyUnwatched = false
    }

    private var filteredShows: [ShowSummary] {
        var list = connection.library.shows
        #if os(tvOS)
        if !query.isEmpty {
            list = list.filter { $0.show.localizedStandardContains(query) }
        }
        #endif
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
        switch sort {
        case .title, .added:
            return alphabetLetter(of: show.sortTitle ?? show.show)
        case .rating:
            return show.rating.map { "★\($0.label)" } ?? "•"
        case .episodes:
            return "\(show.episodeCount)ep"
        }
    }

    var body: some View {
        let library = connection.library
        NavigationStack {
            ScrollViewReader { proxy in
                ScrollView {
                    #if os(tvOS)
                    PlySearchField(text: $query, prompt: "Search series")
                        .padding(.horizontal)
                        .padding(.top, 10)
                    #endif

                    LibraryFilterBar(
                        collectionsKind: .series,
                        sort: $sort,
                        activeFilters: activeFilters,
                        onReset: resetControls
                    )

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
                    .padding([.horizontal, .top])
                }
                .plyTrackVerticalScroll($scrollOffset)
                .plyTabBarBottomClearance()
                .plyFastScrollScrubber(
                    itemCount: filteredShows.count,
                    labelAt: { label(for: filteredShows[$0]) },
                    titleAt: { filteredShows[$0].show },
                    onScrollTo: { proxy.scrollTo(filteredShows[$0].show, anchor: .top) }
                )
            }
            .overlay {
                #if os(tvOS)
                if (!query.isEmpty || !activeFilters.isEmpty) && filteredShows.isEmpty {
                    ContentUnavailableView.search(text: query.isEmpty ? "filtered series" : query)
                } else {
                    LibraryStatus(library: library, isEmpty: library.shows.isEmpty)
                }
                #else
                if !activeFilters.isEmpty && filteredShows.isEmpty {
                    ContentUnavailableView.search(text: "filtered series")
                } else {
                    LibraryStatus(library: library, isEmpty: library.shows.isEmpty)
                }
                #endif
            }
            .plyRefreshable { await library.reload() }
            // Newest first is a show's newest file, which the episodes list says; fetch it only when
            // that sort is chosen, and again after a reload has forgotten it.
            .task(id: sort) { if sort == .added { await library.ensureEpisodes() } }
            .task(id: library.generation) { if sort == .added { await library.ensureEpisodes() } }
            .plyLibraryFilterToolbar(
                collectionsKind: .series,
                scrollOffset: scrollOffset,
                sort: $sort,
                selectedDecade: $selectedDecade,
                attributes: [
                    LibraryFilterAttribute(id: "unwatched", title: "Unwatched Only", isOn: $onlyUnwatched)
                ],
                hasActiveSelection: hasActiveSelection,
                onReset: resetControls
            )
            #if os(iOS)
            .plyMainPageLogo()
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

nonisolated protocol LibrarySortOption: Hashable, Identifiable, CaseIterable, Sendable where AllCases: RandomAccessCollection {
    var rawValue: String { get }
    static var defaultSort: Self { get }
}

extension LibrarySortOption {
    func hasActiveSelection(filters: [LibraryActiveFilter]) -> Bool {
        self != .defaultSort || !filters.isEmpty
    }
}

struct LibraryActiveFilter: Identifiable {
    let id: String
    let title: String
    let onRemove: () -> Void
}

struct LibraryFilterAttribute: Identifiable {
    let id: String
    let title: String
    let isOn: Binding<Bool>
}

/// Reusable scrollable Collections, sort, active-filter, and reset pill bar shared by Films and Series.
struct LibraryFilterBar<Sort: LibrarySortOption>: View {
    let collectionsKind: CollectionKindFilter
    @Binding var sort: Sort
    let activeFilters: [LibraryActiveFilter]
    let onReset: () -> Void

    private var isSortActive: Bool { sort != Sort.defaultSort }
    private var hasActiveSelection: Bool { isSortActive || !activeFilters.isEmpty }

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            pillsContent
                .padding(.horizontal)
                .padding(.top, 4)
        }
    }

    @ViewBuilder
    var pillsContent: some View {
        filterChipRow {
            #if os(tvOS)
            NavigationLink(value: Route.collections(collectionsKind)) {
                Text("Collections")
                    .plyControlPill()
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(collectionsKind.rawValue) Collections")
            #endif

            Menu {
                Picker("Sort", selection: $sort) {
                    ForEach(Sort.allCases) { Text($0.rawValue).tag($0) }
                }
            } label: {
                Label(sort.rawValue, systemImage: "arrow.up.arrow.down")
                    .plyControlPill(highlighted: isSortActive)
            }
            .buttonStyle(.plain)

            ForEach(activeFilters) { filter in
                ActiveFilterChip(title: filter.title, onRemove: filter.onRemove)
            }

            if hasActiveSelection {
                Button(action: onReset) {
                    GlassCircleIconLabel(
                        systemImage: "arrow.counterclockwise",
                        color: .primary,
                        size: PlyDesign.filterPillHeight,
                        glyphSize: 13,
                        tintOpacity: 0.14
                    )
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Reset Filters")
            }
        }
    }
}

/// Top-right Collections button matching the Filter button's height and scrolling away as the feed scrolls up.
struct LibraryCollectionsToolbarButton: View {
    let kind: CollectionKindFilter
    let scrollOffset: CGFloat

    var body: some View {
        NavigationLink(value: Route.collections(kind)) {
            Text("Collections")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.primary)
                .padding(.horizontal, PlyDesign.contentHorizontalPadding)
                .frame(height: PlyDesign.iconButtonSize)
                #if os(iOS)
                .plyInteractiveGlassCapsule()
                #endif
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(kind.rawValue) Collections")
        .plyScrollAway(scrollOffset: scrollOffset)
    }
}

private struct LibraryFilterToolbarModifier<Sort: LibrarySortOption>: ViewModifier {
    let collectionsKind: CollectionKindFilter
    let scrollOffset: CGFloat
    @Binding var sort: Sort
    let availableGenres: [String]
    let selectedGenre: Binding<String?>?
    @Binding var selectedDecade: DecadeFilter
    let attributes: [LibraryFilterAttribute]
    let hasActiveSelection: Bool
    var externalMenuOpen: Binding<Bool>? = nil
    let onReset: () -> Void

    @State private var internalMenuOpen = false
    @State private var filterButtonGlobalFrame: CGRect = .zero

    private var isMenuOpen: Bool {
        get { externalMenuOpen?.wrappedValue ?? internalMenuOpen }
        nonmutating set {
            if let externalMenuOpen {
                externalMenuOpen.wrappedValue = newValue
            } else {
                internalMenuOpen = newValue
            }
        }
    }

    private let dropdownGap: CGFloat = 8
    private let dropdownWidth: CGFloat = 256

    func body(content: Content) -> some View {
        #if os(iOS)
        content
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    LibraryCollectionsToolbarButton(kind: collectionsKind, scrollOffset: scrollOffset)
                }
                .sharedBackgroundVisibility(.hidden)

                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        withAnimation(.spring(response: 0.28, dampingFraction: 0.86)) {
                            isMenuOpen.toggle()
                        }
                    } label: {
                        FilterButtonLabel(hasActive: hasActiveSelection)
                    }
                    .buttonStyle(.plain)
                    .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: {
                        filterButtonGlobalFrame = $0
                    }
                    .preference(key: FilterMenuFramesKey.self, value: filterButtonGlobalFrame == .zero ? [:] : ["filterButton": filterButtonGlobalFrame])
                    .accessibilityLabel("Filter")
                }
                .sharedBackgroundVisibility(.hidden)
            }
            .overlay(alignment: .topTrailing) {
                if isMenuOpen {
                    dropdownOverlay
                }
            }
            .onDisappear { isMenuOpen = false }
        #else
        content
        #endif
    }

    #if os(iOS)
    @ViewBuilder
    private var dropdownOverlay: some View {
        GeometryReader { overlayGeo in
            let overlayFrame = overlayGeo.frame(in: .global)
            let topOffset: CGFloat = filterButtonGlobalFrame != .zero
                ? max(8, filterButtonGlobalFrame.maxY - overlayFrame.minY + dropdownGap)
                : 56
            let trailingOffset: CGFloat = filterButtonGlobalFrame != .zero
                ? max(PlyDesign.contentHorizontalPadding, overlayFrame.maxX - filterButtonGlobalFrame.maxX)
                : PlyDesign.contentHorizontalPadding
            let availableHeight: CGFloat = max(240, min(480, overlayFrame.height - topOffset - 24))

            ZStack(alignment: .topTrailing) {
                Color.black.opacity(0.001)
                    .contentShape(.rect)
                    .onTapGesture { closeMenu() }
                    .accessibilityAddTraits(.isButton)
                    .accessibilityLabel("Dismiss filter menu")

                LibraryFilterDropdownMenu(
                    sort: $sort,
                    availableGenres: availableGenres,
                    selectedGenre: selectedGenre,
                    selectedDecade: $selectedDecade,
                    attributes: attributes,
                    hasActiveSelection: hasActiveSelection,
                    maxHeight: availableHeight,
                    width: dropdownWidth,
                    onReset: {
                        onReset()
                        closeMenu()
                    }
                )
                .background(GeometryReader { geo in
                    Color.clear.preference(key: FilterMenuFramesKey.self, value: ["dropdown": geo.frame(in: .global)])
                })
                .padding(.top, topOffset)
                .padding(.trailing, trailingOffset)
                .transition(.scale(scale: 0.92, anchor: .topTrailing).combined(with: .opacity))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
        }
        .ignoresSafeArea()
    }

    private func closeMenu() {
        withAnimation(.spring(response: 0.24, dampingFraction: 0.88)) {
            isMenuOpen = false
        }
    }
    #endif
}

#if os(iOS)
/// Preference key reporting global frames for the Filter button, dropdown card, and its section rows.
nonisolated struct FilterMenuFramesKey: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

/// Liquid Glass dropdown menu anchored below the top-trailing Filter circle button.
/// Section order: Sort By (through Rating) -> Attributes -> Genre -> Release Era -> Reset.
struct LibraryFilterDropdownMenu<Sort: LibrarySortOption>: View {
    @Binding var sort: Sort
    let availableGenres: [String]
    let selectedGenre: Binding<String?>?
    @Binding var selectedDecade: DecadeFilter
    let attributes: [LibraryFilterAttribute]
    let hasActiveSelection: Bool
    var maxHeight: CGFloat = 460
    var width: CGFloat = 256
    let onReset: () -> Void

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)

        ScrollView {
            VStack(alignment: .leading, spacing: 2) {
                sectionHeader("Sort By")
                ForEach(Sort.allCases) { option in
                    menuRow(option.rawValue, selected: sort == option) {
                        sort = option
                    }
                }

                if !attributes.isEmpty {
                    sectionDivider
                    sectionHeader("Attributes")
                    ForEach(attributes) { attr in
                        menuRow(attr.title, selected: attr.isOn.wrappedValue) {
                            attr.isOn.wrappedValue.toggle()
                        }
                    }
                }

                if !availableGenres.isEmpty, let selectedGenre {
                    sectionDivider
                    sectionHeader("Genre")
                    menuRow("All Genres", selected: selectedGenre.wrappedValue == nil) {
                        selectedGenre.wrappedValue = nil
                    }
                    ForEach(availableGenres, id: \.self) { genre in
                        menuRow(genre, selected: selectedGenre.wrappedValue == genre) {
                            selectedGenre.wrappedValue = genre
                        }
                    }
                }

                sectionDivider
                sectionHeader("Release Era")
                ForEach(DecadeFilter.allCases) { decade in
                    menuRow(decade.rawValue, selected: selectedDecade == decade) {
                        selectedDecade = decade
                    }
                }

                if hasActiveSelection {
                    sectionDivider
                    Button(role: .destructive, action: onReset) {
                        HStack(spacing: 10) {
                            Image(systemName: "arrow.counterclockwise")
                                .font(.subheadline.weight(.semibold))
                                .frame(width: 18)
                            Text("Reset Filters")
                                .font(.subheadline.weight(.medium))
                            Spacer(minLength: 0)
                        }
                        .foregroundStyle(.red)
                        .padding(.horizontal, 14)
                        .frame(minHeight: 40)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.vertical, 8)
        }
        .scrollIndicators(.hidden)
        .frame(width: width)
        .frame(maxHeight: maxHeight)
        .fixedSize(horizontal: false, vertical: true)
        .background(.ultraThinMaterial, in: shape)
        .background(Color.black.opacity(0.42), in: shape)
        .overlay(shape.strokeBorder(Color.white.opacity(0.16), lineWidth: 0.75))
        .shadow(color: .black.opacity(0.45), radius: 22, y: 10)
    }

    private func sectionHeader(_ title: String) -> some View {
        Text(title)
            .font(.caption.weight(.semibold))
            .textCase(.uppercase)
            .tracking(0.6)
            .foregroundStyle(.secondary)
            .padding(.horizontal, 14)
            .padding(.top, 6)
            .padding(.bottom, 2)
            .background(GeometryReader { geo in
                Color.clear.preference(key: FilterMenuFramesKey.self, value: [title: geo.frame(in: .global)])
            })
    }

    private var sectionDivider: some View {
        Divider()
            .overlay(Color.white.opacity(0.10))
            .padding(.vertical, 4)
    }

    private func menuRow(_ title: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: "checkmark")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Color.plyAccent)
                    .opacity(selected ? 1 : 0)
                    .frame(width: 18)

                Text(title)
                    .font(.subheadline.weight(selected ? .semibold : .regular))
                    .foregroundStyle(.primary)
                    .lineLimit(1)

                Spacer(minLength: 0)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: 38)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .background(GeometryReader { geo in
            Color.clear.preference(key: FilterMenuFramesKey.self, value: [title: geo.frame(in: .global)])
        })
    }
}
#endif

extension View {
    /// Attaches the shared top-right iOS Collections shortcut (scroll-away) and Filter menu across library screens.
    func plyLibraryFilterToolbar<Sort: LibrarySortOption>(
        collectionsKind: CollectionKindFilter,
        scrollOffset: CGFloat,
        sort: Binding<Sort>,
        availableGenres: [String] = [],
        selectedGenre: Binding<String?>? = nil,
        selectedDecade: Binding<DecadeFilter>,
        attributes: [LibraryFilterAttribute],
        hasActiveSelection: Bool,
        isMenuOpen: Binding<Bool>? = nil,
        onReset: @escaping () -> Void
    ) -> some View {
        modifier(LibraryFilterToolbarModifier(
            collectionsKind: collectionsKind,
            scrollOffset: scrollOffset,
            sort: sort,
            availableGenres: availableGenres,
            selectedGenre: selectedGenre,
            selectedDecade: selectedDecade,
            attributes: attributes,
            hasActiveSelection: hasActiveSelection,
            externalMenuOpen: isMenuOpen,
            onReset: onReset
        ))
    }
}

@ViewBuilder
private func filterChipRow<Content: View>(@ViewBuilder content: () -> Content) -> some View {
    #if os(iOS)
    GlassEffectContainer(spacing: 8) {
        HStack(spacing: 8) { content() }
    }
    #else
    HStack(spacing: 8) { content() }
    #endif
}

struct SettingsView: View {
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    #if os(iOS)
    @AppStorage(PlyPreferences.showTabLabelsKey) private var showTabLabels = false
    #endif
    @State private var confirmingDisconnect = false

    var body: some View {
        let info = connection.info
        NavigationStack {
            Form {
                #if os(iOS)
                Section("Appearance") {
                    Toggle("Show tab labels", isOn: $showTabLabels)
                }
                #endif
                Section("Server") {
                    LabeledContent("Name", value: info.name)
                    LabeledContent("Address") {
                        Text(connection.base.absoluteString)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            #if os(iOS)
                            .textSelection(.enabled)
                            #endif
                    }
                    LabeledContent("Version", value: info.version)
                    LabeledContent("API", value: "\(info.api) (this app: \(clientAPIVersion))")
                }
                Section("This device") {
                    LabeledContent("Name", value: Device.registration.name)
                    LabeledContent("Paired", value: connection.paired ? "Yes" : info.authRequired ? "No" : "Not needed")
                    LabeledContent("Device id") {
                        Text(Device.id)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            #if os(iOS)
                            .textSelection(.enabled)
                            #endif
                    }
                }
                Section {
                    Button("Connect to another server") { model.chooseServer() }
                    Button("Forget this server", role: .destructive) { confirmingDisconnect = true }
                } header: {
                    Text("Connection")
                } footer: {
                    Text("Forgetting the server removes this device's token; the owner can also revoke it in Settings › Devices.")
                }
            }
            .plyTabBarBottomClearance()
            .plyMainPageLogo(color: .primary)
            .navigationTitle("Settings")
            .plyInlineTitle()
            .confirmationDialog(
                "Forget \(info.name)?",
                isPresented: $confirmingDisconnect,
                titleVisibility: .visible
            ) {
                Button("Forget server", role: .destructive) { model.disconnect() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This removes the pairing token stored on this device. You will need a new pairing code to reconnect if authentication is required.")
            }
        }
    }
}
nonisolated enum DecadeFilter: String, CaseIterable, Identifiable, Sendable {
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

/// An active filter indicator chip with a quick dismissal '✕' button using the shared pill style.
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
            .plyControlPill(highlighted: true)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Remove \(title) filter")
    }
}

/// Circular 44×44 glass label for the top-trailing Filter button in library navigation bars.
struct FilterButtonLabel: View {
    let hasActive: Bool

    var body: some View {
        GlassCircleIconLabel(
            systemImage: "line.3.horizontal.decrease",
            color: hasActive ? .plyAccent : .white,
            tintOpacity: 0
        )
        .accessibilityLabel("Filter")
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
        .hoverEffect()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(upcoming.show)
        .accessibilityValue("\(episodeText), \(Format.airDate(upcoming.nextAirDate))")
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
