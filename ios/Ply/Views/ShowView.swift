import SwiftUI

/// A show: its seasons, and the episodes of the one chosen.
struct ShowView: View {
    let name: String
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var detail: ShowDetail?
    @State private var episodes: [Item] = []
    @State private var season: Int?
    @State private var error: String?
    @State private var trailers: [Trailer] = []
    @State private var selectedTrailer: Trailer?

    private var heroHeight: CGFloat {
        #if os(tvOS)
        500
        #else
        390
        #endif
    }

    private var nextEpisodeToPlay: Item? {
        episodes.first(where: { !$0.isWatched }) ?? episodes.first
    }

    /// The seasons that have files, the specials (0) last.
    private var seasons: [Int] {
        Set(episodes.compactMap(\.season)).sorted { ($0 == 0 ? Int.max : $0) < ($1 == 0 ? Int.max : $1) }
    }

    private var allShowWatched: Bool {
        !episodes.isEmpty && episodes.allSatisfy(\.isWatched)
    }

    private var seasonEpisodes: [Item] {
        episodes.filter { $0.season == season }
    }

    private var allSeasonWatched: Bool {
        !seasonEpisodes.isEmpty && seasonEpisodes.allSatisfy(\.isWatched)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                heroStage(detail)

                HStack(spacing: 12) {
                    if !trailers.isEmpty {
                        Button { selectedTrailer = trailers.first } label: {
                            Label("Trailer", systemImage: "film")
                                .font(.subheadline.weight(.semibold))
                        }
                        .buttonStyle(.bordered)
                    }

                    Button {
                        setShowWatched(!allShowWatched)
                    } label: {
                        Label(allShowWatched ? "Show watched" : "Mark show watched",
                              systemImage: allShowWatched ? "checkmark.circle.fill" : "checkmark.circle")
                            .font(.subheadline.weight(.semibold))
                    }
                    .buttonStyle(.bordered)
                    .tint(allShowWatched ? .orange : .white)

                    Spacer()
                }
                .padding(.horizontal)

                if let overview = detail?.overview ?? detail?.meta?.overview {
                    Text(overview).font(.callout).padding(.horizontal)
                }

                HStack {
                    if seasons.count > 1 {
                        Picker("Season", selection: $season) {
                            ForEach(seasons, id: \.self) { n in
                                Text(seasonName(n)).tag(Optional(n))
                            }
                        }
                        .pickerStyle(.menu)
                    } else if let n = seasons.first {
                        Text(seasonName(n)).font(.headline)
                    }
                    Spacer()
                    if season != nil, !seasonEpisodes.isEmpty {
                        Button {
                            setSeasonWatched(!allSeasonWatched)
                        } label: {
                            Label(allSeasonWatched ? "Season watched" : "Mark season watched",
                                  systemImage: allSeasonWatched ? "checkmark.circle.fill" : "checkmark.circle")
                                .font(.footnote)
                        }
                        .buttonStyle(.borderless)
                    }
                }
                .padding(.horizontal)

                LazyVStack(spacing: 14) {
                    ForEach(seasonEpisodes) { episode in
                        NavigationLink(value: Route.item(episode.id)) {
                            EpisodeRow(episode: episode)
                        }
                        // The badge and the bar are hidden in the row, and the link is the element
                        // VoiceOver reads, so the watch state goes on the link.
                        .accessibilityValue(EpisodeRow.spoken(episode))
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal)

                if let cast = detail?.meta?.cast, !cast.isEmpty {
                    Shelf("Cast") {
                        ForEach(cast.prefix(20), id: \.self) { member in
                            NavigationLink(value: Route.person(member.personId)) {
                                CastCard(member: member)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }

                if let crew = detail?.meta?.crew, !crew.isEmpty {
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
            .padding(.bottom, 32)
        }
        #if os(iOS)
        .ignoresSafeArea(edges: .top)
        .toolbarBackground(.hidden, for: .navigationBar)
        #endif
        .overlay {
            if let error, episodes.isEmpty {
                ContentUnavailableView("Cannot load \(name)", systemImage: "exclamationmark.triangle", description: Text(error))
            } else if detail == nil && episodes.isEmpty {
                ProgressView()
            }
        }
        #if os(iOS)
        .navigationTitle("")
        .plyInlineTitle()
        #else
        .navigationTitle(name)
        #endif
        .task(id: connection.library.generation) { await load() }
        .sheet(item: $selectedTrailer) { trailer in
            TrailerSheet(trailer: $selectedTrailer, all: trailers)
        }
    }

    @ViewBuilder
    private func heroStage(_ d: ShowDetail?) -> some View {
        let backdropPath = d?.backdrop ?? d?.meta?.backdrop

        ZStack(alignment: .bottomLeading) {
            // Full-bleed Artwork from line 0
            RemoteImage(path: backdropPath, placeholder: "photo")
                .aspectRatio(contentMode: .fill)
                .frame(maxWidth: .infinity)
                .frame(height: heroHeight)
                .clipped()

            // Top Degrade / Vignette
            LinearGradient(
                colors: [
                    Color.black.opacity(0.85),
                    Color.black.opacity(0.4),
                    Color.clear
                ],
                startPoint: .top,
                endPoint: .center
            )

            // Center Cinematic Play Button (plays next unwatched episode)
            if let nextEp = nextEpisodeToPlay {
                Button {
                    let start = nextEp.resumeSec > 0 ? nextEp.resumeSec : 0
                    model.play(PlayRequest(itemId: nextEp.id, startSec: start), in: playbackScene)
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
                            .frame(width: 64, height: 64)
                            .shadow(color: Color.orange.opacity(0.55), radius: 14, y: 6)

                        Image(systemName: "play.fill")
                            .font(.system(size: 24, weight: .bold))
                            .foregroundStyle(.white)
                            .offset(x: 2)
                    }
                }
                .buttonStyle(.plain)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
                .padding(.top, 24)
            }

            // Bottom Degrade (darkening into pure black, finishing after title & facts)
            LinearGradient(
                colors: [
                    Color.clear,
                    Color.black.opacity(0.25),
                    Color.black.opacity(0.75),
                    Color.black.opacity(0.96),
                    Color.black
                ],
                startPoint: .center,
                endPoint: .bottom
            )

            // Title & Facts inside bottom of hero
            VStack(alignment: .leading, spacing: 6) {
                Text(name)
                    .font(.system(size: 28, weight: .bold, design: .serif))
                    .foregroundStyle(.white)
                    .lineLimit(2)

                let facts = Format.facts(
                    d?.year.map(String.init),
                    d?.meta?.certification,
                    d?.rating?.label
                )
                if !facts.isEmpty {
                    Text(facts)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.white.opacity(0.75))
                        .lineLimit(1)
                }
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 16)
        }
        .frame(height: heroHeight)
        .frame(maxWidth: .infinity)
    }

    private func setShowWatched(_ watched: Bool) {
        Task {
            do {
                try await connection.api.setShowWatched(name, watched)
                connection.library.scheduleReload()
            } catch {
                model.alert = error.localizedDescription
            }
        }
    }

    private func setSeasonWatched(_ watched: Bool) {
        guard let s = season else { return }
        Task {
            do {
                try await connection.api.setSeasonWatched(name, season: s, watched)
                connection.library.scheduleReload()
            } catch {
                model.alert = error.localizedDescription
            }
        }
    }

    private func seasonName(_ n: Int) -> String {
        detail?.seasons?.first { $0.season == n }?.name ?? (n == 0 ? "Specials" : "Season \(n)")
    }

    private func load() async {
        do {
            async let detail = connection.api.show(name)
            async let episodes = connection.api.episodes(show: name)
            let list = try await episodes.sorted { ($0.season ?? 0, $0.episode ?? 0) < ($1.season ?? 0, $1.episode ?? 0) }
            let d = try await detail
            self.detail = d
            self.episodes = list
            error = nil
            if let metaId = d.meta?.id {
                trailers = (try? await connection.api.trailers(titleId: metaId)) ?? []
            }
            if season == nil || !seasons.contains(season!) {
                // The first season with something left to watch.
                season = seasons.first { n in list.contains { $0.season == n && !$0.isWatched } } ?? seasons.first
            }
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct EpisodeRow: View {
    let episode: Item

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            RemoteImage(path: episode.landscapeImage, placeholder: "tv")
                .aspectRatio(16 / 9, contentMode: .fit)
                .frame(width: 150)
                .overlay(alignment: .topTrailing) { if episode.isWatched { WatchedBadge() } }
                .overlay(alignment: .bottom) { if let f = episode.fraction { ProgressBar(fraction: f) } }
                .clipShape(.rect(cornerRadius: 6))
            VStack(alignment: .leading, spacing: 3) {
                Text(Format.facts(episode.episode.map { "\($0)" }, episode.episodeTitle ?? episode.title))
                    .font(.subheadline.weight(.semibold)).lineLimit(2)
                Text(Format.runtime(episode.durationSec)).font(.caption).foregroundStyle(.secondary)
                if let overview = episode.overview {
                    Text(overview).font(.caption).foregroundStyle(.secondary).lineLimit(3)
                }
            }
            Spacer(minLength: 0)
        }
        .contentShape(.rect)
    }

    /// The row's watch state in words: its badge and bar are hidden from VoiceOver, and the link
    /// around the row carries what they showed.
    static func spoken(_ episode: Item) -> String {
        if episode.isWatched { return "Watched" }
        if episode.fraction != nil { return "In progress" }
        return ""
    }
}
