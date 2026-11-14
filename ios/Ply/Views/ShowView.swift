import SwiftUI

/// A show: its seasons, and the episodes of the one chosen.
struct ShowView: View {
    let name: String
    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @State private var detail: ShowDetail?
    @State private var episodes: [Item] = []
    @State private var season: Int?
    @State private var error: String?
    @State private var trailers: [Trailer] = []
    @State private var selectedTrailer: Trailer?

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
            VStack(alignment: .leading, spacing: 16) {
                Backdrop(path: detail?.backdrop ?? detail?.meta?.backdrop)
                VStack(alignment: .leading, spacing: 8) {
                    Text(name).font(.title.bold())
                    Text(Format.facts(detail?.year.map(String.init), detail?.meta?.certification, detail?.rating?.label))
                        .font(.subheadline).foregroundStyle(.secondary)
                    if let overview = detail?.overview ?? detail?.meta?.overview {
                        Text(overview).font(.callout)
                    }
                    HStack(spacing: 12) {
                        Button {
                            setShowWatched(!allShowWatched)
                        } label: {
                            Label(allShowWatched ? "Show watched" : "Mark show watched",
                                  systemImage: allShowWatched ? "checkmark.circle.fill" : "checkmark.circle")
                        }
                        .buttonStyle(.borderless)

                        if !trailers.isEmpty {
                            Button { selectedTrailer = trailers.first } label: {
                                Label("Trailer", systemImage: "film")
                            }
                            .buttonStyle(.bordered)
                        }
                    }
                    .padding(.top, 4)
                }
                .padding(.horizontal)

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
            .padding(.bottom)
        }
        .overlay {
            if let error, episodes.isEmpty {
                ContentUnavailableView("Cannot load \(name)", systemImage: "exclamationmark.triangle", description: Text(error))
            } else if detail == nil && episodes.isEmpty {
                ProgressView()
            }
        }
        .navigationTitle(name)
        .plyInlineTitle()
        .task(id: connection.library.generation) { await load() }
        .sheet(item: $selectedTrailer) { trailer in
            TrailerSheet(trailer: $selectedTrailer, all: trailers)
        }
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
}
