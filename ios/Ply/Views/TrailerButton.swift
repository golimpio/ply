import SwiftUI

/// The Trailer button of a film's or a show's page (docs/M13-TRAILERS.md, "Clients"). A trailer is a
/// file of the library, played by ply's own player as a film is. A title with none on disk has the
/// server download one when the page asks: the button spins, and is asked again every few seconds,
/// until the trailer is there (it plays) or the download failed (no button).
struct TrailerButton: View {
    let titleId: Int64

    @Environment(Connection.self) private var connection
    @Environment(AppModel.self) private var model
    @Environment(PlaybackScene.self) private var playbackScene
    @State private var trailers: TitleTrailers?

    /// How often the page asks again while the server downloads.
    private static let poll: Duration = .seconds(3)

    var body: some View {
        Group {
            switch trailers?.buttonState ?? .none {
            case .play(let list) where list.count > 1:
                Menu {
                    ForEach(list) { t in
                        Button(t.name) { play(t) }
                    }
                } label: {
                    DetailActionLabel("Trailer", systemImage: "film")
                }
                .plyDetailActionStyle()
            case .play(let list):
                DetailActionButton(title: "Trailer", systemImage: "film") { play(list[0]) }
            case .downloading:
                Button {} label: {
                    DetailActionLabel(title: "Trailer") { ProgressView() }
                }
                .plyDetailActionStyle()
                .disabled(true)
                .accessibilityLabel("Downloading trailer")
            case .none:
                EmptyView()
            }
        }
        .task(id: "\(titleId) \(connection.library.generation)") { await load() }
    }

    private func load() async {
        while !Task.isCancelled {
            trailers = try? await connection.api.trailers(titleId: titleId)
            guard trailers?.buttonState == .downloading else { return }
            try? await Task.sleep(for: Self.poll)
        }
    }

    private func play(_ t: LocalTrailer) {
        model.play(PlayRequest(itemId: t.itemId, startSec: 0), in: playbackScene)
    }
}
