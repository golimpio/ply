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

// MARK: - search

/// Shared ranked search results across movies, shows, episodes, people, and collections (no genres).
struct SearchResultsContent: View {
    @Environment(Connection.self) private var connection

    let query: String
    var scope: SearchScope = .all
    var onSelectResult: (() -> Void)? = nil

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        let browse = connection.browse
        let library = connection.library

        let movies = (scope == .all || scope == .films)
            ? library.movieSearch.search(trimmed, limit: 20)
            : []
        let shows = (scope == .all || scope == .series)
            ? library.showSearch.search(trimmed, limit: 20)
            : []
        let episodes = (scope == .all || scope == .series)
            ? library.episodeSearch.search(trimmed, limit: 20)
            : []
        let people = (scope == .all || scope == .people)
            ? Fuzzy.search(browse.people, query: trimmed, limit: 10)
            : []
        let collections = (scope == .all || scope == .collections)
            ? Fuzzy.search(browse.collections, query: trimmed)
            : []
        let anything = !movies.isEmpty || !shows.isEmpty || !episodes.isEmpty
            || !people.isEmpty || !collections.isEmpty || browse.peopleError != nil

        VStack(alignment: .leading, spacing: PlyDesign.sectionSpacing) {
            if let error = browse.peopleError, scope == .all || scope == .people {
                Text("People search failed: \(error)")
                    .font(.caption).foregroundStyle(.secondary).padding(.horizontal)
            }
            if !movies.isEmpty {
                section("Films", movies.map { movie in
                    BrowseRow(image: movie.poster, placeholder: "film", title: movie.title,
                              subtitle: Format.facts(movie.year.map(String.init), Format.runtime(movie.durationSec)),
                              route: .item(movie.id), onSelect: onSelectResult)
                })
            }
            if !shows.isEmpty {
                section("Series", shows.map { show in
                    BrowseRow(image: show.poster, placeholder: "tv", title: show.show,
                              subtitle: show.episodeCount == 1 ? "1 episode" : "\(show.episodeCount) episodes",
                              route: .show(show.show), onSelect: onSelectResult)
                })
            }
            if !episodes.isEmpty {
                section("Episodes", episodes.map { episode in
                    BrowseRow(image: episode.landscapeImage, placeholder: "tv",
                              title: episode.episodeTitle ?? episode.title,
                              subtitle: Format.facts(episode.show, episode.episodeCode, episode.year.map(String.init)),
                              route: .item(episode.id), onSelect: onSelectResult)
                })
            }
            if !people.isEmpty {
                section("People", people.map { person in
                    BrowseRow(image: person.profile, placeholder: "person.fill", title: person.name,
                              subtitle: person.knownFor ?? "Person", round: true,
                              route: .person(person.id), onSelect: onSelectResult)
                })
            }
            if !collections.isEmpty {
                section("Collections", collections.map { collection in
                    BrowseRow(image: collection.poster, placeholder: "rectangle.stack", title: collection.name,
                              subtitle: collection.count == 1 ? "1 title" : "\(collection.count) titles",
                              route: .collection(collection.id, collection.name), onSelect: onSelectResult)
                })
            }
            if !anything {
                ContentUnavailableView.search(text: trimmed)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 24)
            }
        }
        .padding(.vertical, 8)
    }

    /// One kind of result under its title, on the rhythm Home's shelves use.
    private func section(_ title: String, _ rows: [BrowseRow]) -> some View {
        VStack(alignment: .leading, spacing: PlyDesign.sectionHeaderSpacing) {
            Text(title).font(.title3.bold()).padding(.horizontal)
                .reportSearchFrame("sectionTitle:\(title)")
            VStack(alignment: .leading, spacing: PlyDesign.resultRowSpacing) {
                ForEach(rows) { $0 }
            }
            .reportSearchFrame("sectionRows:\(title)")
        }
        .reportSearchFrame("section:\(title)")
    }
}

#if os(iOS)
/// The system search tab's page (`Tab(role: .search)` in `MainTabs`): the field is the system's own, so it
/// follows the running iOS, with ply's scope circles beside the logo; the page shows recent searches while
/// the field is empty and the ranked results as you type.
struct SearchTabView: View {
    @Environment(Connection.self) private var connection
    @Environment(\.horizontalSizeClass) private var sizeClass

    @State private var query: String
    @State private var scope: SearchScope = .all
    @State private var recentSearches: [String] = []

    init(initialQuery: String = "") {
        _query = State(initialValue: initialQuery)
    }

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        let browse = connection.browse
        let library = connection.library

        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    if let error = browse.error {
                        InlineErrorBanner(
                            title: "Some search lists could not be loaded",
                            message: error,
                            retry: { Task { await browse.load() } }
                        )
                    }
                    EpisodesFailure()

                    if trimmed.isEmpty {
                        if !recentSearches.isEmpty {
                            recentSearchesSection
                        }
                    } else {
                        SearchResultsContent(query: trimmed, scope: scope, onSelectResult: recordCurrentQuery)
                    }
                }
                .padding(.top, 4)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(SearchCancelButtonHider(isActive: sizeClass == .compact))
            // Not `plyTabBarBottomClearance()`: results scroll under the logo and the scope picker, so the
            // page keeps the scroll-edge blur that helper hides on the other main pages.
            .safeAreaPadding(.bottom, PlyDesign.standardBottomClearance)
            .plyMainPageLogo()
            .toolbar {
                if !trimmed.isEmpty {
                    // Each scope circle is its own glass: no second bar capsule around the row.
                    ToolbarItem(placement: .topBarTrailing) { scopePicker }
                        .sharedBackgroundVisibility(.hidden)
                }
            }
            .plyDestinations()
        }
        .plySearchable($query, prompt: "Titles, series, episodes, people")
        // The logo and the scope picker stay in the bar while searching, as on every other page.
        .searchPresentationToolbarBehavior(.avoidHidingContent)
        .autocorrectionDisabled()
        .textInputAutocapitalization(.never)
        .onSubmit(of: .search, recordCurrentQuery)
        .onAppear { recentSearches = SearchHistory.load() }
        .task(id: library.generation) {
            async let lists: Void = browse.load()
            async let episodes: Void = library.ensureEpisodes()
            _ = await (lists, episodes)
        }
        .task(id: query) {
            await browse.searchPeople(trimmed)
        }
    }

    /// While active (compact width: the iPhone's bottom bar), turns off the search field's cancel button, the
    /// round X at the bar's trailing end: the previous tab's button at its leading end already leaves search.
    /// On iPad the field sits in the top bar with no such button, so the X stays. SwiftUI has no modifier for
    /// it, so this sets UIKit's own switch (`automaticallyShowsCancelButton`) on the search controller
    /// `.searchable` installs in the navigation item of the controller hosting this page, found walking up.
    private struct SearchCancelButtonHider: UIViewControllerRepresentable {
        let isActive: Bool

        func makeUIViewController(context: Context) -> Controller { Controller() }

        func updateUIViewController(_ controller: Controller, context: Context) {
            controller.hidesCancelButton = isActive
            controller.apply()
        }

        final class Controller: UIViewController {
            var hidesCancelButton = false

            override func viewWillAppear(_ animated: Bool) {
                super.viewWillAppear(animated)
                apply()
            }

            override func viewDidLayoutSubviews() {
                super.viewDidLayoutSubviews()
                apply()
            }

            func apply() {
                guard let search = Self.searchController(above: self) else { return }
                search.automaticallyShowsCancelButton = !hidesCancelButton
                if hidesCancelButton, search.searchBar.showsCancelButton {
                    search.searchBar.setShowsCancelButton(false, animated: false)
                }
            }

            /// The nearest search controller in the navigation items from `controller` up its parents.
            static func searchController(above controller: UIViewController) -> UISearchController? {
                sequence(first: controller, next: \.parent).lazy.compactMap(\.navigationItem.searchController).first
            }
        }
    }

    private var recentSearchesSection: some View {
        VStack(alignment: .leading, spacing: PlyDesign.recentSearchSpacing) {
            HStack {
                Text("Recent Searches")
                    .font(.caption.weight(.semibold))
                    .textCase(.uppercase)
                    .tracking(0.8)
                    .foregroundStyle(.secondary)
                    .reportSearchFrame("recentHeaderText")
                Spacer()
                Button("Clear") {
                    SearchHistory.clear()
                    recentSearches = []
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(Color.plyAccent)
                .buttonStyle(.plain)
                .reportSearchFrame("recentClear")
            }
            .plyRecentSearchRow()

            ForEach(recentSearches, id: \.self) { item in
                HStack(spacing: 10) {
                    Button {
                        query = item
                    } label: {
                        HStack(spacing: 10) {
                            Image(systemName: "clock")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Text(item)
                                .font(.subheadline)
                                .foregroundStyle(.primary)
                                .lineLimit(1)
                                .reportSearchFrame("recentRowText:\(item)")
                            Spacer(minLength: 0)
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)

                    Button {
                        recentSearches = SearchHistory.remove(item)
                    } label: {
                        RecentSearchRemoveButton()
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Remove \(item) from recent searches")
                }
                .plyRecentSearchRow()
            }
        }
        .padding(.horizontal, PlyDesign.contentHorizontalPadding)
    }

    /// What the results cover, beside the logo while there is a query: one glass circle per scope, Control
    /// Center style, the chosen one filled in the accent. One glass container, so the circles share their
    /// light and the selection reads as one control.
    private var scopePicker: some View {
        GlassEffectContainer(spacing: PlyDesign.glassCircleGroupSpacing) {
            HStack(spacing: PlyDesign.glassCircleGroupSpacing) {
                ForEach(SearchScope.allCases) { item in
                    let selected = scope == item
                    Button {
                        withAnimation(.snappy) { scope = item }
                    } label: {
                        GlassCircleIconLabel(systemImage: item.systemImage, selected: selected)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(item.rawValue)
                    .accessibilityAddTraits(selected ? .isSelected : [])
                    .accessibilityShowsLargeContentViewer()
                    .reportSearchFrame("scope:\(item.rawValue)")
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Search in")
        .reportSearchFrame("scopePicker")
    }

    private func recordCurrentQuery() {
        guard !trimmed.isEmpty else { return }
        recentSearches = SearchHistory.record(trimmed)
    }
}

extension SearchScope {
    /// The scope picker's symbol; Films and Series use their tabs' own.
    var systemImage: String {
        switch self {
        case .all: "list.dash"
        case .films: AppTab.films.systemImage
        case .series: AppTab.series.systemImage
        case .people: "person"
        case .collections: "square.grid.2x2"
        }
    }
}

/// The delete affordance on one recent-search row: `delete.left` reads as "delete this entry",
/// and its trailing alignment puts the glyph on the block's right margin.
struct RecentSearchRemoveButton: View {
    static let systemImage = "delete.left"

    var body: some View {
        Image(systemName: Self.systemImage)
            .font(.system(size: 15))
            .foregroundStyle(.tertiary)
            .reportSearchFrame("recentRemoveGlyph")
            .frame(
                width: PlyDesign.recentSearchRowHeight,
                height: PlyDesign.recentSearchRowHeight,
                alignment: .trailing
            )
            // The symbol's side bearing would leave its ink short of the edge Clear ends on.
            .offset(x: PlyDesign.recentSearchGlyphNudge)
            .contentShape(.rect)
    }
}

extension View {
    /// One row of the recent-searches block: at least the touch height, plus the block's rhythm padding,
    /// so the "Recent Searches" header and every result share the same vertical box (and a long
    /// Dynamic Type line still grows the row instead of clipping).
    func plyRecentSearchRow() -> some View {
        frame(minHeight: PlyDesign.recentSearchRowHeight)
            .padding(.vertical, PlyDesign.recentSearchRowPadding)
    }
}
#endif

struct BrowseView: View {
    @Environment(Connection.self) private var connection
    @State private var query = ""

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        let browse = connection.browse
        let library = connection.library
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    #if os(tvOS)
                    PlySearchField(text: $query, prompt: "Titles, series, episodes, people, collections")
                        .frame(maxWidth: 360)
                        .padding(.horizontal, 24)
                        .frame(maxWidth: .infinity)
                    #endif

                    if trimmed.isEmpty {
                        browseHome(browse)
                    } else {
                        SearchResultsContent(query: trimmed)
                    }
                }
                .padding(.top, 4)
            }
            .plyTabBarBottomClearance()
            .plySearchable($query, prompt: "Titles, series, episodes, people, collections")
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
            #if os(iOS)
            .plyMainPageLogo()
            #else
            .navigationTitle("Search")
            .plyInlineTitle()
            #endif
            .plyDestinations()
        }
    }

    @ViewBuilder
    private func browseHome(_ browse: BrowseModel) -> some View {
        VStack(alignment: .leading, spacing: 26) {
            if let error = browse.error {
                InlineErrorBanner(
                    title: "Some of the browse lists could not be loaded",
                    message: error,
                    retry: { Task { await browse.load() } }
                )
            }
            EpisodesFailure()
            if browse.loaded, browse.error == nil {
                ContentUnavailableView(
                    "Search your library",
                    systemImage: "magnifyingglass",
                    description: Text("Find films, series, episodes, people, and collections.")
                )
            } else if !browse.loaded {
                ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
            }
        }
    }
}

/// One line of a search result: a picture, a title, a line under it, and where it goes.
struct BrowseRow: View, Identifiable {
    static let posterCornerRadius: CGFloat = 6

    let image: String?
    let placeholder: String
    let title: String
    let subtitle: String?
    var round = false
    let route: Route
    var onSelect: (() -> Void)? = nil

    var id: String { "\(route)-\(title)" }

    var body: some View {
        let imageHeight: CGFloat = round ? 48 : 64
        NavigationLink(value: route) {
            HStack(alignment: round ? .center : .top, spacing: 12) {
                RemoteImage(path: image, placeholder: placeholder)
                    .frame(width: 48, height: imageHeight)
                    .clipShape(RoundedRectangle(cornerRadius: round ? 24 : Self.posterCornerRadius))
                    .reportSearchFrame("rowPoster")
                VStack(alignment: .leading, spacing: 3) {
                    Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                    if let subtitle, !subtitle.isEmpty {
                        Text(subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
                .reportSearchFrame("rowText")
                .padding(.top, round ? 0 : Self.posterCornerRadius)
                Spacer(minLength: 0)
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                    .frame(height: imageHeight)
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
        .simultaneousGesture(TapGesture().onEnded {
            onSelect?()
        })
    }
}

// MARK: - person

struct PersonView: View {
    let id: Int64
    @Environment(Connection.self) private var connection
    @State private var person: Person?
    @State private var filmography: [PersonFilmographyEntry] = []
    @State private var filmographyLoading = false
    @State private var error: String?
    @State private var isBioExpanded = false
    @State private var selectedEntry: PersonFilmographyEntry?
    @State private var outerTopInset: CGFloat = 0

    var body: some View {
        ZStack {
            ScrollView {
                if let person {
                    let media = person.openableMediaCredits
                    let filmSections = CreditSection.sections(media.films)
                    let seriesSections = CreditSection.sections(media.series)
                    let external = person.externalFilmography(from: filmography)

                    VStack(spacing: 28) {
                        header(person)

                        VStack(alignment: .leading, spacing: 24) {
                            if !filmSections.isEmpty {
                                mediaShelf(
                                    title: "In your films",
                                    totalCount: CreditSection.uniqueTitleCount(media.films),
                                    sections: filmSections
                                )
                            }

                            if !seriesSections.isEmpty {
                                mediaShelf(
                                    title: "In your series",
                                    totalCount: CreditSection.uniqueTitleCount(media.series),
                                    sections: seriesSections
                                )
                            }

                            if filmSections.isEmpty && seriesSections.isEmpty {
                                Text("None of their work is in your library right now.")
                                    .font(.callout)
                                    .foregroundStyle(.secondary)
                                    .padding(.horizontal)
                            }

                            if filmographyLoading && external.films.isEmpty && external.series.isEmpty {
                                HStack(spacing: 8) {
                                    ProgressView()
                                    Text("Loading filmography…")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                                .padding(.horizontal)
                            }

                            if !external.films.isEmpty {
                                externalShelf(title: "More films with \(person.name)", entries: external.films)
                            }

                            if !external.series.isEmpty {
                                externalShelf(title: "More series with \(person.name)", entries: external.series)
                            }
                        }
                    }
                }
            }
            #if os(iOS)
            .ignoresSafeArea(edges: .top)
            #endif
            .plyTabBarBottomClearance()
        }
        .onGeometryChange(for: CGFloat.self) { $0.safeAreaInsets.top } action: { outerTopInset = $0 }
        #if os(iOS)
        .navigationTitle("")
        #else
        .navigationTitle(person?.name ?? "Person")
        #endif
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
        .sheet(item: $selectedEntry) { entry in
            CreditPreviewSheet(entry: entry)
        }
    }

    @ViewBuilder
    private func mediaShelf(title: String, totalCount: Int, sections: [CreditSection]) -> some View {
        if sections.count == 1, let only = sections.first {
            Shelf(title, count: totalCount) {
                ForEach(only.entries) { entry in
                    CreditCard(entry: entry)
                }
            }
        } else {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(title).font(.title3.bold())
                    Text("\(totalCount)")
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }
                .padding(.horizontal)

                ForEach(sections) { section in
                    Shelf(section.title, count: section.entries.count) {
                        ForEach(section.entries) { entry in
                            CreditCard(entry: entry)
                        }
                    }
                }
            }
        }
    }

    private func externalShelf(title: String, entries: [PersonFilmographyEntry]) -> some View {
        Shelf(title, count: entries.count) {
            ForEach(entries) { entry in
                Button {
                    selectedEntry = entry
                } label: {
                    FilmographyCard(entry: entry)
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func header(_ person: Person) -> some View {
        VStack(alignment: .center, spacing: 14) {
            // Centralized circular portrait
            ZStack {
                Circle()
                    .fill(Color.orange.opacity(0.18))
                    .frame(width: 130, height: 130)
                    .blur(radius: 24)

                RemoteImage(path: person.profile, placeholder: "person.fill")
                    .aspectRatio(contentMode: .fill)
                    .frame(width: 120, height: 120)
                    .clipShape(Circle())
                    .overlay(
                        Circle().strokeBorder(Color.white.opacity(0.14), lineWidth: 1.5)
                    )
                    .shadow(color: Color.black.opacity(0.45), radius: 14, y: 5)
            }
            #if os(iOS)
            .padding(.top, max(12, outerTopInset - PlyDesign.iconButtonSize + 8))
            #else
            .padding(.top, 8)
            #endif

            // Centralized Name & Metadata
            VStack(spacing: 6) {
                Text(person.name)
                    .font(.system(size: 26, weight: .semibold))
                    .tracking(-0.5)
                    .foregroundStyle(.primary)
                    .multilineTextAlignment(.center)

                let facts = Format.facts(
                    person.lifeSpan,
                    person.placeOfBirth,
                    person.knownFor.map { "Known for \($0.lowercased())" }
                )
                if !facts.isEmpty {
                    Text(facts)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 24)
                }

                #if os(iOS)
                if person.wikipediaURL != nil || person.imdbURL != nil {
                    HStack(spacing: 8) {
                        if let wikiURL = person.wikipediaURL {
                            ExternalLinkPill(title: "Wikipedia", url: wikiURL)
                        }
                        if let imdbURL = person.imdbURL {
                            ExternalLinkPill(title: "IMDb", url: imdbURL)
                        }
                    }
                    .padding(.top, 4)
                }
                #endif
            }

            // Centralized Biography with Read More
            if let biography = person.biography, !biography.isEmpty {
                VStack(spacing: 6) {
                    Text(biography)
                        .font(.callout)
                        .foregroundStyle(.primary.opacity(0.85))
                        .multilineTextAlignment(.center)
                        .lineLimit(isBioExpanded ? nil : 6)
                        .lineSpacing(3)

                    if biography.count > 250 {
                        Button {
                            withAnimation(.spring(response: 0.3, dampingFraction: 0.8)) {
                                isBioExpanded.toggle()
                            }
                        } label: {
                            Text(isBioExpanded ? "Read less" : "Read more")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(Color.plyAccent)
                        }
                        .buttonStyle(.plain)
                        .padding(.top, 2)
                    }
                }
                .padding(.horizontal, 24)
                .padding(.top, 4)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 8)
    }

    private func load() async {
        do {
            // Await the person first so TVDB -> TMDB merges resolve and TMDB metadata is populated
            // before asking for their external filmography.
            let loaded = try await connection.api.person(id)
            self.person = loaded
            error = nil

            filmographyLoading = true
            defer { filmographyLoading = false }
            self.filmography = (try? await connection.api.personFilmography(loaded.id)) ?? []
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct CreditCard: View {
    let entry: CreditSection.Entry

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            TitlePoster(
                card: entry.card,
                caption: Format.facts(entry.card.yearText, entry.card.rating?.label),
                width: shelfCardWidth
            )
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
        .accessibilityElement(children: .combine)
    }
}

struct FilmographyCard: View {
    let entry: PersonFilmographyEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            PosterCard(
                title: entry.title,
                caption: Format.facts(entry.yearText, entry.ratingLabel.map { "★ \($0)" }),
                image: entry.poster
            )
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
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// Preview sheet for an off-library credit, loading synopsis, credits, genres, and cast from TMDB.
struct CreditPreviewSheet: View {
    let entry: PersonFilmographyEntry

    @Environment(Connection.self) private var connection
    @Environment(\.dismiss) private var dismiss
    @State private var preview: TitlePreview?
    @State private var loading = false
    @State private var failed = false

    private var imdbURL: URL? {
        ExternalLinks.imdbTitleURL(entry.imdbId ?? preview?.imdbId)
    }

    private var runtimeText: String? {
        guard let min = preview?.runtimeMin, min > 0 else { return nil }
        return Format.runtime(Double(min * 60))
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    header

                    if loading {
                        HStack(spacing: 8) {
                            ProgressView()
                            Text("Loading details…")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                        .padding(.horizontal)
                    } else if failed {
                        Text("More details couldn’t be loaded right now.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .padding(.horizontal)
                    }

                    if let preview {
                        previewBody(preview)
                    }
                }
                .padding(.vertical)
            }
            .plyNoBlurHeader()
            #if os(iOS)
            .containerBackground(.clear, for: .navigation)
            .navigationTitle("")
            #else
            .navigationTitle(entry.title)
            #endif
            .plyInlineTitle()
            .plyCloseToolbar(accessibilityLabel: "Close preview") {
                dismiss()
            }
            .task(id: entry.id) {
                await loadPreview()
            }
        }
        #if os(iOS)
        .presentationBackground(.ultraThinMaterial)
        #endif
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 16) {
            RemoteImage(path: entry.poster, placeholder: entry.isSeries ? "tv" : "film")
                .aspectRatio(2 / 3, contentMode: .fit)
                .frame(width: 112)
                .clipShape(.rect(cornerRadius: 10))

            VStack(alignment: .leading, spacing: 8) {
                Text(entry.title)
                    .font(.title3.bold())

                if let orig = entry.originalTitle, !orig.isEmpty, orig != entry.title {
                    Text(orig)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }

                let specs = Format.facts(
                    entry.isSeries ? "Series" : "Film",
                    entry.yearText,
                    runtimeText,
                    preview?.certification,
                    entry.episodeCount.map { $0 == 1 ? "1 episode" : "\($0) episodes" },
                    entry.ratingLabel.map { "TMDB \($0)" }
                )
                if !specs.isEmpty {
                    Text(specs)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.secondary)
                }

                if let role = entry.roleDescription {
                    Text("\(entry.character != nil ? "As" : "Job"): \(role)")
                        .font(.subheadline)
                        .foregroundStyle(.primary.opacity(0.9))
                }

                #if os(iOS)
                if imdbURL != nil || entry.tmdbURL != nil {
                    HStack(spacing: 8) {
                        if let imdbURL {
                            ExternalLinkPill(title: "IMDb", url: imdbURL)
                        }
                        if let tmdbURL = entry.tmdbURL {
                            ExternalLinkPill(title: "TMDB", url: tmdbURL)
                        }
                    }
                    .padding(.top, 2)
                }
                #endif
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal)
    }

    @ViewBuilder
    private func previewBody(_ preview: TitlePreview) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let tagline = preview.tagline, !tagline.isEmpty {
                Text(tagline)
                    .font(.callout.italic())
                    .foregroundStyle(.secondary)
            }
            if let overview = preview.overview, !overview.isEmpty {
                Text(overview)
                    .font(.callout)
            }
        }
        .padding(.horizontal)

        let lines = preview.creditLines
        if !lines.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(lines, id: \.label) { line in
                    HStack(alignment: .top, spacing: 8) {
                        Text(line.label)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                            .frame(width: 105, alignment: .leading)
                        Text(line.names.joined(separator: ", "))
                            .font(.subheadline)
                    }
                }
            }
            .padding(.horizontal)
        }

        if !preview.genres.isEmpty {
            WrappingFlowLayout(spacing: 8, lineSpacing: 8) {
                ForEach(preview.genres, id: \.self) { genre in
                    Text(genre)
                        .font(.caption.weight(.medium))
                        .padding(.horizontal, 12)
                        .padding(.vertical, 6)
                        .background(.fill.tertiary, in: .capsule)
                }
            }
            .padding(.horizontal)
        }

        if !preview.cast.isEmpty {
            Shelf("Cast") {
                ForEach(Array(preview.cast.enumerated()), id: \.offset) { _, member in
                    PersonCreditCard(name: member.name, subtitle: member.role, profile: member.profile)
                }
            }
        }
    }

    private func loadPreview() async {
        guard let tmdbId = entry.tmdbId, tmdbId > 0 else { return }
        loading = true
        failed = false
        defer { loading = false }
        do {
            preview = try await connection.api.titlePreview(kind: entry.kind, tmdbId: tmdbId)
        } catch is CancellationError {
        } catch {
            failed = true
        }
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
                .padding([.horizontal, .top])
        }
        .plyTabBarBottomClearance()
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
    var seedCollection: CollectionDetail? = nil
    @Environment(Connection.self) private var connection
    @State private var collection: CollectionDetail?
    @State private var error: String?

    private var activeCollection: CollectionDetail? { collection ?? seedCollection }

    var body: some View {
        let current = activeCollection
        let backdropPath = current?.backdrop ?? current?.titles.first?.backdrop ?? current?.poster ?? current?.titles.first?.poster

        DetailBackdropScaffold(backdropPath: backdropPath) {
            if let current {
                DetailHeroStage(
                    backdropPath: backdropPath,
                    title: current.name,
                    facts: current.titles.count == 1 ? "1 title in your library" : "\(current.titles.count) titles in your library",
                    playAccessibilityLabel: nil
                )

                if let overview = current.overview, !overview.isEmpty {
                    Text(overview)
                        .font(.callout)
                        .padding(.horizontal, PlyDesign.contentHorizontalPadding)
                }

                titleGrid(current.titles)
                    .padding(.horizontal, PlyDesign.contentHorizontalPadding)
            }
        }
        .overlay {
            if let error, current == nil {
                ContentUnavailableView("Cannot load this collection", systemImage: "exclamationmark.triangle",
                                       description: Text(error))
            } else if current == nil {
                ProgressView()
            }
        }
        #if os(tvOS)
        .navigationTitle(name)
        #endif
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

/// Full-grid collections browser opened from Films or Series, matching their poster grid layout.
struct AllCollectionsView: View {
    let kind: CollectionKindFilter
    @Environment(Connection.self) private var connection
    @State private var scrollOffset: CGFloat = 0

    private var filteredCollections: [CollectionCard] {
        connection.browse.collections(for: kind).sorted {
            $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
    }

    var body: some View {
        let browse = connection.browse
        ScrollViewReader { proxy in
            ScrollView {
                LazyVGrid(columns: posterColumns, spacing: 16) {
                    ForEach(filteredCollections) { collection in
                        NavigationLink(value: Route.collection(collection.id, collection.name)) {
                            PosterCard(
                                title: collection.name,
                                caption: collection.count == 1 ? "1 title" : "\(collection.count) titles",
                                image: collection.poster
                            )
                        }
                        .buttonStyle(.plain)
                        .id(collection.id)
                    }
                }
                .padding([.horizontal, .top])
            }
            .plyTrackVerticalScroll($scrollOffset)
            .plyTabBarBottomClearance()
            .plyFastScrollScrubber(
                itemCount: filteredCollections.count,
                labelAt: { alphabetLetter(of: filteredCollections[$0].name) },
                titleAt: { filteredCollections[$0].name },
                onScrollTo: { proxy.scrollTo(filteredCollections[$0].id, anchor: .top) }
            )
        }
        .overlay {
            if browse.loaded && filteredCollections.isEmpty {
                ContentUnavailableView(
                    "No \(kind.rawValue.lowercased()) collections",
                    systemImage: "rectangle.stack",
                    description: Text("Collections with \(kind.rawValue.lowercased()) titles in your library will appear here.")
                )
            } else if !browse.loaded || (browse.resolvingCollectionKinds && filteredCollections.isEmpty) {
                ProgressView()
            }
        }
        .plyScrollAwayInlineTitle("Collections", scrollOffset: scrollOffset)
        .plyRefreshable {
            await browse.load()
            await browse.resolveCollectionKindsIfNeeded()
        }
        .task {
            if !browse.loaded { await browse.load() }
            await browse.resolveCollectionKindsIfNeeded()
        }
    }
}
