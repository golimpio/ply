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
        .background(WindowSceneReader(
            onScene: { playbackScene.windowScene = $0 },
            onGone: { model.unregister(playbackScene) }
        ).frame(width: 0, height: 0))
        .alert("ply", isPresented: Binding { model.alert != nil } set: { if !$0 { model.alert = nil } }) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.alert ?? "")
        }
    }
}

/// The window this view is in, handed to its playback scene so that a play is presented in this
/// window and not in whichever window UIKit calls active. A window closing is reported twice over:
/// the scene disconnecting (the documented signal) and the view leaving its window.
private struct WindowSceneReader: UIViewRepresentable {
    let onScene: @MainActor (UIWindowScene?) -> Void
    let onGone: @MainActor () -> Void

    func makeUIView(context: Context) -> WindowSceneProbe {
        let view = WindowSceneProbe()
        view.isUserInteractionEnabled = false
        view.onScene = onScene
        view.onGone = onGone
        return view
    }

    func updateUIView(_ view: WindowSceneProbe, context: Context) {
        view.onScene = onScene
        view.onGone = onGone
    }
}

private final class WindowSceneProbe: UIView {
    var onScene: (@MainActor (UIWindowScene?) -> Void)?
    var onGone: (@MainActor () -> Void)?

    private weak var watched: UIWindowScene?
    private var disconnectObserver: (any NSObjectProtocol)?

    override func didMoveToWindow() {
        super.didMoveToWindow()
        let scene = window?.windowScene
        onScene?(scene)
        observe(scene)
        if scene == nil { onGone?() }
    }

    override func willMove(toWindow newWindow: UIWindow?) {
        super.willMove(toWindow: newWindow)
        // The window is going: stop listening, whether or not the scene says it disconnected.
        if newWindow == nil { observe(nil) }
    }

    /// A scene that disconnects is not guaranteed to call the view's life-cycle methods, so the
    /// notification is the one that always arrives.
    private func observe(_ scene: UIWindowScene?) {
        guard scene !== watched else { return }
        if let disconnectObserver { NotificationCenter.default.removeObserver(disconnectObserver) }
        disconnectObserver = nil
        watched = scene
        guard let scene else { return }
        disconnectObserver = NotificationCenter.default.addObserver(
            forName: UIScene.didDisconnectNotification, object: scene, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.onGone?() }
        }
    }
}

struct MainTabs: View {
    var body: some View {
        TabView {
            HomeView()
                .tabItem { Label("Programme", systemImage: "house") }
            MoviesView()
                .tabItem { Label("Films", systemImage: "film") }
            ShowsView()
                .tabItem { Label("Series", systemImage: "tv") }
            BrowseView()
                .tabItem { Label("Browse", systemImage: "square.grid.2x2") }
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gear") }
        }
    }
}
