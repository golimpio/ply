import SwiftUI

/// The ply brand mark, in display serif italic.
struct PlyBrand: View {
    var size: CGFloat = 26

    var body: some View {
        Text("Ply")
            .font(.system(size: size, weight: .regular, design: .serif).italic())
            .tracking(-0.5)
            .foregroundStyle(.primary)
            .accessibilityLabel("Ply")
    }
}

/// Where a navigation link goes, in every tab.
enum Route: Hashable {
    case item(Int64)
    case show(String)
    case person(Int64)
    case people
    case genre(Int64, String)
    case collection(Int64, String)
}

/// The poster grid of the library and browse pages: a phone's cards, or the TV's, watched from across
/// a room.
let posterColumns: [GridItem] = {
    #if os(tvOS)
    [GridItem(.adaptive(minimum: 220), spacing: 28, alignment: .top)]
    #else
    [GridItem(.adaptive(minimum: 105), spacing: 12, alignment: .top)]
    #endif
}()

/// One card of a shelf: the poster grid's size, one at a time.
let shelfCardWidth: CGFloat = {
    #if os(tvOS)
    240
    #else
    120
    #endif
}()

/// The width of a 16:9 landscape or upcoming card in a shelf.
let shelfWideCardWidth: CGFloat = {
    #if os(tvOS)
    360
    #else
    240
    #endif
}()

/// The space between the cards of a shelf.
let shelfSpacing: CGFloat = {
    #if os(tvOS)
    28
    #else
    12
    #endif
}()

/// A genre's tile: an adaptive grid of them, with room for a name at 10 feet.
let genreTileWidth: CGFloat = {
    #if os(tvOS)
    220
    #else
    150
    #endif
}()
/// The rail's letter for a title: A to Z (case-insensitive, accents stripped), else # (digits, symbols).
func alphabetLetter(of title: String) -> String {
    let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let first = trimmed.folding(options: [.diacriticInsensitive, .caseInsensitive], locale: .current).first else {
        return "#"
    }
    let upper = String(first).uppercased()
    return (upper >= "A" && upper <= "Z") ? upper : "#"
}

#if os(iOS)
struct ScrollOffsetPreferenceKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = nextValue()
    }
}

/// A Plex-style fast-scrolling vertical slider docked along the right edge of the screen.
/// Appears when scrolling, featuring an edge-docked handle with vertical arrows and a floating white indicator bubble.
struct FastScrollScrubber: View {
    let itemCount: Int
    let currentLabel: String
    let onScrub: (Int) -> Void

    @Binding var isVisible: Bool
    @Binding var progress: CGFloat
    @State private var isDragging: Bool = false
    @State private var lastReportedIndex: Int = -1

    var body: some View {
        GeometryReader { geo in
            let topInset: CGFloat = 80
            let bottomInset: CGFloat = 110
            let trackHeight = max(100, geo.size.height - topInset - bottomInset)
            let clampedProgress = max(0, min(1, progress))
            let currentY = topInset + (clampedProgress * trackHeight)

            HStack(spacing: 8) {
                Spacer()

                if isVisible {
                    // Floating White Indicator Bubble
                    ZStack {
                        Circle()
                            .fill(Color.white)
                            .frame(width: 44, height: 44)
                            .shadow(color: Color.black.opacity(0.35), radius: 8, y: 3)

                        if isDragging && !currentLabel.isEmpty {
                            Text(currentLabel)
                                .font(.system(size: 20, weight: .bold, design: .serif))
                                .foregroundStyle(Color.black)
                        } else {
                            Circle()
                                .fill(Color.black)
                                .frame(width: 7, height: 7)
                        }
                    }
                    .scaleEffect(isDragging ? 1.08 : 0.95)
                    .animation(.spring(response: 0.25, dampingFraction: 0.7), value: isDragging)
                    .transition(.scale.combined(with: .opacity))

                    // Docked Slider Handle on right edge with vertical arrows
                    ZStack {
                        Capsule()
                            .fill(Color.black.opacity(0.72))
                            .background(.ultraThinMaterial, in: Capsule())
                            .overlay(
                                Capsule()
                                    .strokeBorder(Color.white.opacity(0.2), lineWidth: 0.8)
                            )
                            .frame(width: 22, height: 38)
                            .shadow(color: Color.black.opacity(0.4), radius: 6, y: 2)

                        Image(systemName: "chevron.up.chevron.down")
                            .font(.system(size: 10, weight: .bold))
                            .foregroundStyle(Color.white.opacity(0.85))
                    }
                    .frame(width: 32, height: 44, alignment: .trailing)
                    .contentShape(Rectangle())
                }
            }
            .position(x: geo.size.width - 24, y: currentY)
            .opacity(isVisible ? 1 : 0)
            .animation(.easeInOut(duration: 0.25), value: isVisible)
            .gesture(
                DragGesture(minimumDistance: 0)
                    .onChanged { value in
                        isDragging = true
                        isVisible = true
                        let localY = value.location.y - topInset
                        let p = max(0, min(1, localY / trackHeight))
                        progress = p

                        guard itemCount > 0 else { return }
                        let index = Int(p * CGFloat(itemCount - 1))
                        let clamped = max(0, min(itemCount - 1, index))

                        if clamped != lastReportedIndex {
                            lastReportedIndex = clamped
                            UISelectionFeedbackGenerator().selectionChanged()
                            onScrub(clamped)
                        }
                    }
                    .onEnded { _ in
                        isDragging = false
                    }
            )
        }
        .allowsHitTesting(isVisible)
    }
}
#endif

extension View {
    /// The pages a Route opens.
    func plyDestinations() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case let .item(id): ItemDetailView(id: id)
            case let .show(name): ShowView(name: name)
            case let .person(id): PersonView(id: id)
            case .people: PeopleView()
            case let .genre(id, name): GenreView(id: id, name: name)
            case let .collection(id, name): CollectionView(id: id, name: name)
            }
        }
    }

    /// iOS places a page's title inline; tvOS has no navigation bar to place it in.
    @ViewBuilder func plyInlineTitle() -> some View {
        #if os(iOS)
        navigationBarTitleDisplayMode(.inline)
        #else
        self
        #endif
    }

    /// Search and pull-to-refresh are iOS; the TV has a search field of its own (BrowseView).
    @ViewBuilder func plySearchable(_ text: Binding<String>, prompt: String) -> some View {
        #if os(iOS)
        searchable(text: text, prompt: prompt)
        #else
        self
        #endif
    }

    @ViewBuilder func plyRefreshable(_ action: @escaping @MainActor () async -> Void) -> some View {
        #if os(iOS)
        refreshable { await action() }
        #else
        self
        #endif
    }

    /// A picker that opens a page: iOS only, and the default elsewhere (the TV focus engine).
    @ViewBuilder func plyPagePicker() -> some View {
        #if os(iOS)
        pickerStyle(.navigationLink)
        #else
        self
        #endif
    }
}

enum Format {
    /// "1h 41m", "52m".
    static func runtime(_ seconds: Double) -> String {
        let minutes = Int((seconds / 60).rounded())
        return minutes >= 60 ? "\(minutes / 60)h \(minutes % 60)m" : "\(minutes)m"
    }

    /// "1:02:03", "24:13".
    static func clock(_ seconds: Double) -> String {
        let s = Int(seconds)
        let (h, m, sec) = (s / 3600, s / 60 % 60, s % 60)
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%d:%02d", m, sec)
    }

    /// The parts of a line of facts, joined.
    static func facts(_ parts: String?...) -> String {
        parts.compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
    }

    /// "Today", "Tomorrow", "Nov 20", or the date string.
    static func airDate(_ s: String) -> String {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        f.locale = Locale(identifier: "en_US_POSIX")
        guard let date = f.date(from: s) else { return s }
        if Calendar.current.isDateInToday(date) { return "Today" }
        if Calendar.current.isDateInTomorrow(date) { return "Tomorrow" }
        let out = DateFormatter()
        out.dateFormat = "MMM d"
        return out.string(from: date)
    }
}

/// An image the server links to (with its URL token when it needs one), filling its frame.
struct RemoteImage: View {
    @Environment(Connection.self) private var connection
    let path: String?
    var placeholder = "photo"

    var body: some View {
        Rectangle()
            .fill(.quaternary)
            .overlay {
                AsyncImage(url: connection.api.url(path)) { phase in
                    if let image = phase.image {
                        image.resizable().scaledToFill()
                    } else {
                        Image(systemName: placeholder).font(.title2).foregroundStyle(.tertiary)
                    }
                }
            }
            .clipped()
            // The card around it says what it is; the picture itself says nothing.
            .accessibilityHidden(true)
    }
}

struct ProgressBar: View {
    let fraction: Double

    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Rectangle().fill(.black.opacity(0.55))
                Rectangle().fill(.tint).frame(width: g.size.width * fraction)
            }
        }
        .frame(height: 4)
        .accessibilityHidden(true)
    }
}

struct WatchedBadge: View {
    var body: some View {
        Image(systemName: "checkmark.circle.fill")
            .symbolRenderingMode(.palette)
            .foregroundStyle(.white, .tint)
            .font(.title3)
            .padding(6)
            .accessibilityHidden(true)
    }
}

/// A 2:3 poster with its title.
struct PosterCard: View {
    let title: String
    var caption: String?
    let image: String?
    var watched = false
    var fraction: Double?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            RemoteImage(path: image, placeholder: "film")
                .aspectRatio(2 / 3, contentMode: .fit)
                .overlay(alignment: .topTrailing) { if watched { WatchedBadge() } }
                .overlay(alignment: .bottom) { if let fraction { ProgressBar(fraction: fraction) } }
                .clipShape(.rect(cornerRadius: 8))
            Text(title).font(.footnote.weight(.medium)).lineLimit(1)
            if let caption {
                Text(caption).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .contentShape(.rect)
        // The TV's focus effect: a card that lifts and highlights as the focus engine reaches it.
        .hoverEffect()
        // One element to VoiceOver: the title, and in words what the badge and the bar show.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue(spoken)
    }

    /// What the picture's overlays say, in words.
    private var spoken: String {
        var parts: [String] = []
        if let caption, !caption.isEmpty { parts.append(caption) }
        if watched {
            parts.append("Watched")
        } else if fraction != nil {
            parts.append("In progress")
        }
        return parts.joined(separator: ", ")
    }
}

/// A 16:9 card for a file in progress: a movie or an episode.
struct LandscapeCard: View {
    let item: Item

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            RemoteImage(path: item.landscapeImage, placeholder: item.isEpisode ? "tv" : "film")
                .aspectRatio(16 / 9, contentMode: .fit)
                .overlay(alignment: .bottom) { if let f = item.fraction { ProgressBar(fraction: f) } }
                .clipShape(.rect(cornerRadius: 8))
            Text(item.isEpisode ? item.show ?? item.title : item.title)
                .font(.footnote.weight(.medium)).lineLimit(1)
            Text(item.isEpisode ? Format.facts(item.episodeCode, item.episodeTitle) : item.year.map(String.init) ?? " ")
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .contentShape(.rect)
        .hoverEffect()
        // One element to VoiceOver: the title, then the line under it and the watch state.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(item.isEpisode ? item.show ?? item.title : item.title)
        .accessibilityValue(spoken)
    }

    /// The card's second line, and whether it was started or watched, in words.
    private var spoken: String {
        var parts: [String] = []
        if item.isEpisode {
            parts.append(Format.facts(item.episodeCode, item.episodeTitle))
        } else if let year = item.year {
            parts.append(String(year))
        }
        if item.isWatched {
            parts.append("Watched")
        } else if item.fraction != nil {
            parts.append("In progress")
        }
        return parts.joined(separator: ", ")
    }
}

/// A titled row that scrolls sideways.
struct Shelf<Content: View>: View {
    let title: String
    var subtitle: String?
    @ViewBuilder let content: Content

    init(_ title: String, subtitle: String? = nil, @ViewBuilder content: () -> Content) {
        self.title = title
        self.subtitle = subtitle
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.title3.bold())
                if let subtitle, !subtitle.isEmpty {
                    Text(subtitle).font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: shelfSpacing) { content }
                    .padding(.horizontal)
            }
        }
    }
}

/// The library's state over a page that shows it: loading, its error, or empty.
struct LibraryStatus: View {
    let library: LibraryModel
    let isEmpty: Bool

    var body: some View {
        if !library.loaded {
            ProgressView()
        } else if isEmpty, let error = library.error {
            ContentUnavailableView {
                Label("Cannot load the library", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try again") { Task { await library.reload() } }
            }
        } else if isEmpty {
            ContentUnavailableView("Nothing here yet", systemImage: "film.stack",
                                   description: Text("Add folders to the library on ply's web page."))
        }
    }
}

/// A wide picture at the top of a page, fading into it.
struct Backdrop: View {
    let path: String?

    var body: some View {
        RemoteImage(path: path, placeholder: "photo")
            .aspectRatio(16 / 9, contentMode: .fit)
            .overlay(alignment: .bottom) {
                LinearGradient(colors: [.clear, Self.pageBackground], startPoint: .center, endPoint: .bottom)
            }
    }

    /// The colour a page fades into. tvOS has no `systemBackground`; its pages are black.
    private static var pageBackground: Color {
        #if os(tvOS)
        .black
        #else
        Color(.systemBackground)
        #endif
    }
}

/// Says the episodes could not be loaded, with a way to ask again: the Browse search reads them, and
/// so does a show's newest file (the Shows "Recently updated" sort).
struct EpisodesFailure: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        if let error = connection.library.episodesError {
            VStack(alignment: .leading, spacing: 4) {
                Text("The episodes could not be loaded")
                    .font(.subheadline.weight(.semibold))
                Text(error).font(.caption).foregroundStyle(.secondary)
                Button("Try again") { Task { await connection.library.ensureEpisodes() } }
                    .font(.caption)
            }
            .padding(.horizontal)
        }
    }
}
