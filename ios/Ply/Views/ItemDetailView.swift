import SwiftUI

/// A movie or an episode: what it is, and how to play it.
struct ItemDetailView: View {
    let id: Int64
    var seedItem: Item? = nil

    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    /// The player of this window, not another one's.
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var detail: ItemDetail?
    @State private var error: String?
    @State private var audioIndex: Int?
    @State private var subtitle: SubtitleChoice?
    @State private var receivers: [Receiver] = []

    private var routableReceivers: [Receiver] { receivers.filter(\.canRoute) }

    var body: some View {
        Group {
            if let detail {
                content(item: detail.item, detail: detail)
            } else if let seedItem {
                content(item: seedItem, detail: nil)
            } else if let error {
                DetailBackdropScaffold(backdropPath: nil) { Color.clear }
                    .overlay {
                        ContentUnavailableView("Cannot load this item", systemImage: "exclamationmark.triangle", description: Text(error))
                    }
            } else {
                DetailBackdropScaffold(backdropPath: nil) { Color.clear }
                    .overlay { ProgressView() }
            }
        }
        .task(id: connection.library.generation) { await load() }
        #if os(iOS)
        .task { await loadReceivers() }
        #endif
    }

    private func content(item: Item, detail d: ItemDetail?) -> some View {
        let backdropPath = item.isEpisode ? item.landscapeImage : item.backdrop ?? d?.meta?.backdrop ?? item.art?.backdrop ?? item.poster

        let collections = resolvedCollections(for: d)
        let versions = (!item.isEpisode ? item.titleId : nil).map { titleId in
            connection.library.movies.filter { $0.titleId == titleId }
        } ?? []
        let hasLeadingDetailBeforeTracks = !collections.isEmpty || versions.count > 1 || d == nil

        return DetailBackdropScaffold(
            backdropPath: backdropPath,
            bottomClearance: PlyDesign.defaultDetailBottomClearance
        ) {
            heroStage(item: item, detail: d, backdropPath: backdropPath)

            // Action Buttons Row: [Trailer] and [Mark as watched] side-by-side
            if d != nil {
                actionButtons(
                    item,
                    extraBottomSpacing: hasLeadingDetailBeforeTracks ? PlyDesign.detailRowVerticalPadding : 0
                )
            }

            ForEach(collections, id: \.id) { collection in
                NavigationLink(value: Route.collection(collection.id, collection.name)) {
                    Label(collection.name, systemImage: "rectangle.stack").font(.subheadline)
                }
                .padding(.horizontal)
            }

            if versions.count > 1 {
                versionsSection(versions, currentId: item.id)
            }

            if let d {
                tracks(d)
            }

            VStack(alignment: .leading, spacing: 8) {
                if let tagline = d?.meta?.tagline, !tagline.isEmpty, !item.isEpisode {
                    Text(tagline).font(.callout.italic()).foregroundStyle(.secondary)
                }
                if let overview = d?.overview ?? item.overview {
                    Text(overview).font(.callout)
                }
                if let genres = d?.meta?.genres, !genres.isEmpty {
                    Text(genres.map(\.name).joined(separator: ", ")).font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal)

            DetailCreditShelves(cast: d?.meta?.cast, crew: d?.meta?.crew)
        }
        #if os(iOS)
        .toolbar {
            if d != nil, !routableReceivers.isEmpty {
                ToolbarItem(placement: .topBarTrailing) {
                    airplayMenu(item)
                }
                .sharedBackgroundVisibility(.hidden)
            }
        }
        #endif
    }

    @ViewBuilder
    private func heroStage(item: Item, detail d: ItemDetail?, backdropPath: String?) -> some View {
        let title = item.isEpisode
            ? (item.episodeTitle ?? d?.episodeInfo?.name ?? item.title)
            : (d?.meta?.title ?? item.title)
        let facts = Self.heroFacts(item: item, detail: d)

        DetailHeroStage(
            backdropPath: backdropPath,
            title: title,
            facts: facts,
            playAccessibilityLabel: item.resumeSec > 0 ? "Resume" : "Play",
            onPlay: {
                let start = item.resumeSec > 0 ? item.resumeSec : 0
                play(from: start)
            }
        ) {
            if item.isEpisode, let show = item.show {
                NavigationLink(value: Route.show(show)) {
                    Text(Format.facts(show, item.episodeCode))
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Color.plyAccent)
                }
            }
        }
    }

    /// The line under the title: year, runtime, certification, rating and, for a play in progress, what is
    /// left of it ("2023 · 1h 33m · R · Metacritic 80 · 88m left").
    static func heroFacts(item: Item, detail d: ItemDetail?) -> String {
        Format.facts(
            item.year.map(String.init),
            item.durationSec > 0 ? Format.runtime(item.durationSec) : nil,
            d?.meta?.certification,
            (item.rating ?? d?.meta?.rating)?.label,
            item.resumeSec > 0 ? Format.timeLeft(max(0, item.durationSec - item.resumeSec)) : nil
        )
    }

    private func resolvedCollections(for d: ItemDetail?) -> [CollectionRef] {
        if let collections = d?.meta?.collections, !collections.isEmpty {
            return collections
        }
        if let collection = d?.meta?.collection {
            return [collection]
        }
        return []
    }

    @ViewBuilder
    private func actionButtons(_ item: Item, extraBottomSpacing: CGFloat) -> some View {
        DetailActionRow(extraBottomSpacing: extraBottomSpacing) {
            // A film's own; an episode's title is its show's, whose trailer is on the show's page.
            if item.kind == "movie", let titleId = item.titleId {
                TrailerButton(titleId: titleId)
            }

            DetailActionButton(
                title: item.isWatched ? "Watched" : "Mark as watched",
                systemImage: item.isWatched ? "checkmark.circle.fill" : "checkmark.circle",
                prominent: item.isWatched,
                tint: item.isWatched ? .plyAccent : nil
            ) {
                setWatched(!item.isWatched)
            }
            .detailFrameReporter("watchedButton")

            // A play in progress can start over; carrying on is the hero's play button, and what is left is in
            // the facts line.
            if item.resumeSec > 0 {
                DetailActionButton(title: "Play from start", systemImage: "arrow.counterclockwise") {
                    play(from: 0)
                }
                .detailFrameReporter("restartButton")
            }
        }
    }

    /// Icon-only AirPlay routing menu shown on the top right of item details.
    /// Choosing a receiver starts playback on the server's `airplay` profile (MPEG-TS with burned/rendition
    /// subtitles); the viewer then selects the same TV in the player's AirPlay control.
    /// Shown only when there are receivers to route to (the toolbar item checks `routableReceivers`).
    private func airplayMenu(_ item: Item) -> some View {
        Menu {
            ForEach(routableReceivers) { receiver in
                Button {
                    play(from: item.resumeSec, receiver: receiver)
                } label: {
                    Label(receiver.name, systemImage: "appletv")
                }
            }
        } label: {
            GlassCircleIconLabel(systemImage: "airplayvideo")
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Play on Apple TV")
    }

    private func currentAudioLabel(_ audio: [MediaStream]) -> String {
        if let audioIndex, let match = audio.first(where: { $0.index == audioIndex }) {
            return match.label
        }
        if let defaultAudio = audio.first(where: { $0.default == true }) {
            return defaultAudio.label
        }
        if let first = audio.first {
            return first.label
        }
        return "Default"
    }

    private func currentSubtitleLabel(_ subtitles: [(Subtitle, SubtitleChoice)]) -> String {
        if let subtitle {
            switch subtitle {
            case let .text(key, _):
                if let match = subtitles.first(where: { $0.0.key == key }) {
                    return match.0.label
                }
                return "On"
            case let .burnIn(index):
                if let match = subtitles.first(where: { $0.0.key == String(index) }) {
                    return "\(match.0.label) (burned in)"
                }
                return "Burned in"
            }
        }
        return "Off"
    }

    private func videoSummary(_ d: ItemDetail) -> String? {
        var parts: [String] = []
        let base = d.item.versionLabel
        if !base.isEmpty { parts.append(base) }
        if let codec = d.videoStreams.first?.codec, !codec.isEmpty,
           !base.localizedStandardContains(codec) {
            parts.append(codec.uppercased())
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    @ViewBuilder
    private func tracks(_ d: ItemDetail) -> some View {
        let video = videoSummary(d)
        let audio = d.audioStreams
        let subtitles = d.subtitles.compactMap { s in s.choice.map { (s, $0) } }

        VStack(spacing: 0) {
            if let video {
                DetailRowContent(
                    systemImage: "film",
                    title: "Video",
                    value: video
                )

                Divider()
                    .padding(.leading, 34)
                    .opacity(0.14)
            }

            if audio.count > 1 {
                Menu {
                    Picker("Audio", selection: $audioIndex) {
                        Text("Default").tag(Int?.none)
                        ForEach(audio, id: \.index) { stream in
                            Text(stream.label).tag(Optional(stream.index))
                        }
                    }
                } label: {
                    DetailRowContent(
                        systemImage: "speaker.wave.2",
                        title: "Audio",
                        value: currentAudioLabel(audio),
                        showsPickerIndicator: true
                    )
                }
                .buttonStyle(.plain)
            } else {
                DetailRowContent(
                    systemImage: "speaker.wave.2",
                    title: "Audio",
                    value: currentAudioLabel(audio)
                )
            }

            Divider()
                .padding(.leading, 34)
                .opacity(0.14)

            if !subtitles.isEmpty {
                Menu {
                    Picker("Subtitles", selection: $subtitle) {
                        Text("Off").tag(SubtitleChoice?.none)
                        ForEach(subtitles, id: \.0.key) { s, choice in
                            Text(s.image ? "\(s.label) (burned in)" : s.label).tag(Optional(choice))
                        }
                    }
                } label: {
                    DetailRowContent(
                        systemImage: "captions.bubble",
                        title: "Subtitles",
                        value: currentSubtitleLabel(subtitles),
                        showsPickerIndicator: true
                    )
                }
                .buttonStyle(.plain)
            } else {
                DetailRowContent(
                    systemImage: "captions.bubble",
                    title: "Subtitles",
                    value: "None"
                )
            }
        }
        .padding(.horizontal, PlyDesign.contentHorizontalPadding)
    }

    @ViewBuilder
    private func versionsSection(_ versions: [Item], currentId: Int64) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Versions").font(.headline)
            ScrollView(.horizontal, showsIndicators: false) {
                versionChipsRow(versions, currentId: currentId)
            }
        }
        .padding(.horizontal, PlyDesign.contentHorizontalPadding)
    }

    @ViewBuilder
    private func versionChipsRow(_ versions: [Item], currentId: Int64) -> some View {
        let row = HStack(spacing: 8) {
            ForEach(versions) { v in
                let label = v.versionLabel.isEmpty ? (v.id == currentId ? "Current" : "Version \(v.id)") : v.versionLabel
                if v.id == currentId {
                    Text(label)
                        .font(.subheadline.weight(.semibold))
                        .padding(.horizontal, 12)
                        .padding(.vertical, 6)
                        .foregroundStyle(.white)
                        .plyInteractiveGlassCapsule(tint: .plyAccent)
                        .accessibilityAddTraits(.isSelected)
                } else {
                    NavigationLink(value: Route.item(v.id)) {
                        Text(label)
                            .font(.subheadline)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 6)
                            .foregroundStyle(.primary)
                            .plyInteractiveGlassCapsule()
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        #if os(iOS)
        GlassEffectContainer(spacing: 8) { row }
        #else
        row
        #endif
    }

    private func play(from start: Double, receiver: Receiver? = nil) {
        model.play(PlayRequest(itemId: id, startSec: start, audioIndex: audioIndex, subtitle: subtitle, receiver: receiver),
                   in: playbackScene)
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
            if audioIndex == nil {
                audioIndex = d.audioStreams.first { $0.default == true }?.index ?? d.audioStreams.first?.index
            }
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}
