import SwiftUI

/// Where a navigation link goes, in every tab.
enum Route: Hashable {
    case item(Int64)
    case show(String)
    case person(Int64)
    case genre(Int64, String)
    case collection(Int64, String)
}

/// The poster grid of the library and browse pages.
let posterColumns = [GridItem(.adaptive(minimum: 105), spacing: 12, alignment: .top)]

extension View {
    /// The pages a Route opens.
    func plyDestinations() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case let .item(id): ItemDetailView(id: id)
            case let .show(name): ShowView(name: name)
            case let .person(id): PersonView(id: id)
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
    }
}

struct WatchedBadge: View {
    var body: some View {
        Image(systemName: "checkmark.circle.fill")
            .symbolRenderingMode(.palette)
            .foregroundStyle(.white, .tint)
            .font(.title3)
            .padding(6)
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
    }
}

/// A titled row that scrolls sideways.
struct Shelf<Content: View>: View {
    let title: String
    @ViewBuilder let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(.title3.bold()).padding(.horizontal)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 12) { content }
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
