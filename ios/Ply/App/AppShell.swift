import AVFoundation
import SwiftUI

/// The caches and audio session both apps start with. The poster and still art comes back from the
/// disk cache with the server's validators; sound plays with the ring switch off, as a video app
/// should.
func configureAppCaches(memoryCapacity: Int, diskCapacity: Int) {
    URLCache.shared = URLCache(memoryCapacity: memoryCapacity, diskCapacity: diskCapacity)
    try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .moviePlayback)
}

/// The app's root, whatever the platform: connecting and pairing, then the tabs.
struct RootView: View {
    @Environment(AppModel.self) private var model
    /// This window's player: two windows never share one (docs/M11-CLIENTS.md, "The apps").
    @State private var playbackScene = PlaybackScene(presenter: PlayerPresenter())

    var body: some View {
        @Bindable var model = model
        Group {
            switch model.phase {
            case .starting:
                ProgressView()
            case .choosingServer:
                ConnectView()
            case let .unreachable(saved, message):
                ConnectionProblemView(saved: saved, message: message, kind: .unreachable)
            case let .incompatible(saved, message):
                ConnectionProblemView(saved: saved, message: message, kind: .incompatible)
            case let .pairing(pending):
                PairView(pending: pending)
            case let .ready(connection):
                MainTabs()
                    .environment(connection)
            }
        }
        .environment(playbackScene)
        .onAppear { model.register(playbackScene) }
        .onDisappear { model.unregister(playbackScene) }
        .background(WindowSceneReader { playbackScene.windowScene = $0 }.frame(width: 0, height: 0))
        .alert("ply", isPresented: Binding { model.alert != nil } set: { if !$0 { model.alert = nil } }) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.alert ?? "")
        }
    }
}

/// The window this view is in, handed to its playback scene so that a play is presented in this
/// window and not in whichever window UIKit calls active.
private struct WindowSceneReader: UIViewRepresentable {
    let onScene: @MainActor (UIWindowScene?) -> Void

    func makeUIView(context: Context) -> WindowSceneProbe {
        let view = WindowSceneProbe()
        view.isUserInteractionEnabled = false
        view.onScene = onScene
        return view
    }

    func updateUIView(_ view: WindowSceneProbe, context: Context) {
        view.onScene = onScene
    }
}

private final class WindowSceneProbe: UIView {
    var onScene: (@MainActor (UIWindowScene?) -> Void)?

    override func didMoveToWindow() {
        super.didMoveToWindow()
        onScene?(window?.windowScene)
    }
}

struct MainTabs: View {
    var body: some View {
        TabView {
            HomeView()
                .tabItem { Label("Home", systemImage: "house") }
            MoviesView()
                .tabItem { Label("Movies", systemImage: "film") }
            ShowsView()
                .tabItem { Label("Shows", systemImage: "tv") }
            BrowseView()
                .tabItem { Label("Browse", systemImage: "square.grid.2x2") }
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gear") }
        }
    }
}
