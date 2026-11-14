import SwiftUI

/// A show: its seasons, and the episodes of the one chosen.
struct ShowView: View {
    let name: String
    @Environment(Connection.self) private var connection
    @State private var detail: ShowDetail?
    @State private var episodes: [Item] = []
    @State private var season: Int?
    @State private var error: String?

    /// The seasons that have files, the specials (0) last.
    private var seasons: [Int] {
        Set(episodes.compactMap(\.season)).sorted { ($0 == 0 ? Int.max : $0) < ($1 == 0 ? Int.max : $1) }
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
                }
                .padding(.horizontal)

                if seasons.count > 1 {
                    Picker("Season", selection: $season) {
                        ForEach(seasons, id: \.self) { n in
                            Text(seasonName(n)).tag(Optional(n))
                        }
                    }
                    .pickerStyle(.menu)
                    .padding(.horizontal)
                } else if let n = seasons.first {
                    Text(seasonName(n)).font(.headline).padding(.horizontal)
                }

                LazyVStack(spacing: 14) {
                    ForEach(episodes.filter { $0.season == season }) { episode in
                        NavigationLink(value: Route.item(episode.id)) {
                            EpisodeRow(episode: episode)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal)
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
    }

    private func seasonName(_ n: Int) -> String {
        detail?.seasons?.first { $0.season == n }?.name ?? (n == 0 ? "Specials" : "Season \(n)")
    }

    private func load() async {
        do {
            async let detail = connection.api.show(name)
            async let episodes = connection.api.episodes(show: name)
            let list = try await episodes.sorted { ($0.season ?? 0, $0.episode ?? 0) < ($1.season ?? 0, $1.episode ?? 0) }
            self.detail = try await detail
            self.episodes = list
            error = nil
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
