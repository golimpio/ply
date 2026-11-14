import SwiftUI
#if canImport(WebKit)
import WebKit
#endif

/// A movie or an episode: what it is, and how to play it.
struct ItemDetailView: View {
    let id: Int64
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @State private var detail: ItemDetail?
    @State private var error: String?
    @State private var audioIndex: Int?
    @State private var subtitle: SubtitleChoice?
    @State private var receivers: [Receiver] = []
    @State private var trailers: [Trailer] = []
    @State private var selectedTrailer: Trailer?

    private var routableReceivers: [Receiver] { receivers.filter(\.canRoute) }

    var body: some View {
        Group {
            if let detail {
                content(detail)
            } else if let error {
                ContentUnavailableView("Cannot load this item", systemImage: "exclamationmark.triangle", description: Text(error))
            } else {
                ProgressView()
            }
        }
        .plyInlineTitle()
        .task(id: connection.library.generation) { await load() }
        #if os(iOS)
        .task { await loadReceivers() }
        #endif
        .sheet(item: $selectedTrailer) { trailer in
            TrailerSheet(trailer: $selectedTrailer, all: trailers)
        }
    }

    private func content(_ d: ItemDetail) -> some View {
        let item = d.item
        return ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Backdrop(path: item.isEpisode ? item.landscapeImage : item.backdrop ?? d.meta?.backdrop ?? item.art?.backdrop)
                VStack(alignment: .leading, spacing: 8) {
                    if item.isEpisode {
                        NavigationLink(value: Route.show(item.show ?? "")) {
                            Text(Format.facts(item.show, item.episodeCode)).font(.subheadline.weight(.semibold))
                        }
                        Text(item.episodeTitle ?? d.episodeInfo?.name ?? item.title).font(.title.bold())
                    } else {
                        Text(d.meta?.title ?? item.title).font(.title.bold())
                    }
                    Text(Format.facts(item.year.map(String.init), Format.runtime(item.durationSec),
                                      d.meta?.certification, (item.rating ?? d.meta?.rating)?.label))
                        .font(.subheadline).foregroundStyle(.secondary)
                    if let collection = d.meta?.collection, d.meta?.collections?.isEmpty ?? true {
                        NavigationLink(value: Route.collection(collection.id, collection.name)) {
                            Label(collection.name, systemImage: "rectangle.stack").font(.subheadline)
                        }
                    } else {
                        ForEach(d.meta?.collections ?? [], id: \.id) { collection in
                            NavigationLink(value: Route.collection(collection.id, collection.name)) {
                                Label(collection.name, systemImage: "rectangle.stack").font(.subheadline)
                            }
                        }
                    }
                }
                .padding(.horizontal)

                playButtons(item).padding(.horizontal)

                if !item.isEpisode, let titleId = item.titleId {
                    let versions = connection.library.movies.filter { $0.titleId == titleId }
                    if versions.count > 1 {
                        versionsSection(versions, currentId: item.id)
                    }
                }

                tracks(d).padding(.horizontal)
                #if os(iOS)
                airplay(item).padding(.horizontal)
                #endif

                VStack(alignment: .leading, spacing: 8) {
                    if let tagline = d.meta?.tagline, !tagline.isEmpty, !item.isEpisode {
                        Text(tagline).font(.callout.italic()).foregroundStyle(.secondary)
                    }
                    if let overview = d.overview {
                        Text(overview).font(.callout)
                    }
                    if let genres = d.meta?.genres, !genres.isEmpty {
                        Text(genres.map(\.name).joined(separator: ", ")).font(.caption).foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal)

                if let cast = d.meta?.cast, !cast.isEmpty {
                    Shelf("Cast") {
                        ForEach(cast.prefix(20), id: \.self) { member in
                            NavigationLink(value: Route.person(member.personId)) {
                                CastCard(member: member)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }

                if let crew = d.meta?.crew, !crew.isEmpty {
                    Shelf("Crew") {
                        ForEach(crew.prefix(20), id: \.self) { member in
                            NavigationLink(value: Route.person(member.personId)) {
                                CrewCard(member: member)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(.bottom)
        }
    }

    @ViewBuilder
    private func playButtons(_ item: Item) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                if item.resumeSec > 0 {
                    Button { play(from: item.resumeSec) } label: {
                        Label("Resume from \(Format.clock(item.resumeSec))", systemImage: "play.fill")
                    }
                    .buttonStyle(.borderedProminent)
                    Button { play(from: 0) } label: {
                        Label("From the start", systemImage: "arrow.counterclockwise")
                    }
                    .buttonStyle(.bordered)
                } else {
                    Button { play(from: 0) } label: {
                        Label("Play", systemImage: "play.fill").frame(minWidth: 120)
                    }
                    .buttonStyle(.borderedProminent)
                }
                if !trailers.isEmpty {
                    Button { selectedTrailer = trailers.first } label: {
                        Label("Trailer", systemImage: "film")
                    }
                    .buttonStyle(.bordered)
                }
            }
            .controlSize(.large)
            if let fraction = item.fraction {
                ProgressView(value: fraction)
            }
            Button { setWatched(!item.isWatched) } label: {
                Label(item.isWatched ? "Watched" : "Mark as watched",
                      systemImage: item.isWatched ? "checkmark.circle.fill" : "checkmark.circle")
            }
            .buttonStyle(.borderless)
        }
    }

    /// The AirPlay targets the server could route a play to. Choosing one starts the play on the
    /// server's `airplay` profile (MPEG-TS, which older TVs play) with its text subtitles as a
    /// rendition; the person then picks the same TV in the player's AirPlay button.
    @ViewBuilder
    private func airplay(_ item: Item) -> some View {
        if !routableReceivers.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Menu {
                    ForEach(routableReceivers) { receiver in
                        Button {
                            play(from: item.resumeSec, receiver: receiver)
                        } label: {
                            Label(receiver.name, systemImage: "appletv")
                        }
                    }
                } label: {
                    Label("Play on Apple TV", systemImage: "airplayvideo")
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                Text("Starts the play for that TV, subtitles included. Pick the same one in the player's AirPlay button.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func tracks(_ d: ItemDetail) -> some View {
        let audio = d.audioStreams
        let subtitles = d.subtitles.compactMap { s in Self.choice(s).map { (s, $0) } }
        if audio.count > 1 || !subtitles.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                if audio.count > 1 {
                    Picker(selection: $audioIndex) {
                        Text("Default").tag(Int?.none)
                        ForEach(audio, id: \.index) { Text($0.label).tag(Optional($0.index)) }
                    } label: {
                        Label("Audio", systemImage: "speaker.wave.2")
                    }
                }
                if !subtitles.isEmpty {
                    Picker(selection: $subtitle) {
                        Text("Off").tag(SubtitleChoice?.none)
                        ForEach(subtitles, id: \.0.key) { s, choice in
                            Text(s.image ? "\(s.label) (burned in)" : s.label).tag(Optional(choice))
                        }
                    } label: {
                        Label("Subtitles", systemImage: "captions.bubble")
                    }
                }
            }
            .plyPagePicker()
        }
    }

    /// A text track is drawn from its WebVTT; a bitmap one can only be burned in by the server.
    private static func choice(_ s: Subtitle) -> SubtitleChoice? {
        if s.image { return Int(s.key).map(SubtitleChoice.burnIn) }
        return s.url.map { SubtitleChoice.text(key: s.key, url: $0) }
    }

    @ViewBuilder
    private func versionsSection(_ versions: [Item], currentId: Int64) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Versions").font(.headline)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(versions) { v in
                        let label = v.versionLabel.isEmpty ? (v.id == currentId ? "Current" : "Version \(v.id)") : v.versionLabel
                        if v.id == currentId {
                            Text(label)
                                .font(.subheadline.weight(.semibold))
                                .padding(.horizontal, 12)
                                .padding(.vertical, 6)
                                .background(.tint, in: .capsule)
                                .foregroundStyle(.white)
                        } else {
                            NavigationLink(value: Route.item(v.id)) {
                                Text(label)
                                    .font(.subheadline)
                                    .padding(.horizontal, 12)
                                    .padding(.vertical, 6)
                                    .background(.quaternary, in: .capsule)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
        }
        .padding(.horizontal)
    }

    private func play(from start: Double, receiver: Receiver? = nil) {
        model.play(PlayRequest(itemId: id, startSec: start, audioIndex: audioIndex, subtitle: subtitle, receiver: receiver))
    }

    private func setWatched(_ watched: Bool) {
        Task {
            do {
                if let item = detail?.item, !item.isEpisode, let titleId = item.titleId {
                    try await connection.api.setTitleWatched(titleId, watched)
                } else {
                    try await connection.api.setWatched(id, watched)
                }
                connection.library.scheduleReload()
            } catch {
                model.alert = error.localizedDescription
            }
        }
    }

    private func loadReceivers() async {
        receivers = (try? await connection.api.receivers()) ?? []
    }

    private func load() async {
        do {
            let d = try await connection.api.item(id)
            detail = d
            error = nil
            if audioIndex == nil, d.audioStreams.count > 1 {
                audioIndex = d.audioStreams.first { $0.default == true }?.index
            }
            if let titleId = d.meta?.id ?? d.item.titleId {
                trailers = (try? await connection.api.trailers(titleId: titleId)) ?? []
            }
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct CrewCard: View {
    let member: CrewMember

    var body: some View {
        VStack(spacing: 4) {
            RemoteImage(path: member.profile, placeholder: "person.fill")
                .frame(width: 80, height: 80)
                .clipShape(.circle)
            Text(member.name).font(.caption.weight(.medium)).lineLimit(1)
            Text(member.job).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
        }
        .frame(width: 90)
    }
}

#if canImport(WebKit)
struct WebTrailerView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []
        let webView = WKWebView(frame: .zero, configuration: config)
        webView.isOpaque = false
        webView.backgroundColor = .black
        webView.scrollView.isScrollEnabled = false
        webView.load(URLRequest(url: url))
        return webView
    }

    func updateUIView(_ uiView: WKWebView, context: Context) {
        if uiView.url != url {
            uiView.load(URLRequest(url: url))
        }
    }
}
#endif

struct TrailerSheet: View {
    @Binding var trailer: Trailer?
    let all: [Trailer]
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            VStack(spacing: 16) {
                if let t = trailer, let url = t.embedURL {
                    #if canImport(WebKit)
                    WebTrailerView(url: url)
                        .aspectRatio(16 / 9, contentMode: .fit)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                        .padding(.horizontal)
                    #else
                    Text(t.label(in: all)).font(.headline)
                    #endif
                }
                if all.count > 1 {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 8) {
                            ForEach(all) { t in
                                Button {
                                    trailer = t
                                } label: {
                                    if t.id == trailer?.id {
                                        Text(t.label(in: all))
                                            .font(.subheadline)
                                            .padding(.horizontal, 12)
                                            .padding(.vertical, 6)
                                            .background(.tint, in: .capsule)
                                            .foregroundStyle(.white)
                                    } else {
                                        Text(t.label(in: all))
                                            .font(.subheadline)
                                            .padding(.horizontal, 12)
                                            .padding(.vertical, 6)
                                            .background(.quaternary, in: .capsule)
                                            .foregroundStyle(.primary)
                                    }
                                }
                                .buttonStyle(.plain)
                            }
                        }
                        .padding(.horizontal)
                    }
                }
                Spacer()
            }
            .padding(.top)
            .navigationTitle(trailer?.label(in: all) ?? "Trailer")
            .plyInlineTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}

struct CastCard: View {
    let member: CastMember

    var body: some View {
        VStack(spacing: 4) {
            RemoteImage(path: member.profile, placeholder: "person.fill")
                .frame(width: 80, height: 80)
                .clipShape(.circle)
            Text(member.name).font(.caption.weight(.medium)).lineLimit(1)
            if let character = member.character, !character.isEmpty {
                Text(character).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .frame(width: 90)
    }
}
