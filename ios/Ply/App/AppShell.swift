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
        .preferredColorScheme(.dark)
        .tint(.plyAmberHighlight)
        .onAppear { model.register(playbackScene) }
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
/// window and not in whichever window UIKit calls active, and told when that window closes. Only the
/// scene disconnecting says that: the view also leaves its window (and SwiftUI calls it gone) whenever a
/// full-screen player covers it, which must not end the play that player is showing.
private struct WindowSceneReader: UIViewRepresentable {
    let onScene: @MainActor (UIWindowScene) -> Void
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

final class WindowSceneProbe: UIView {
    var onScene: (@MainActor (UIWindowScene) -> Void)?
    var onGone: (@MainActor () -> Void)?
    /// Where the disconnect is listened for: a test posts its own, not the test host's real scene's.
    var notifications = NotificationCenter.default

    private weak var watched: UIWindowScene?

    /// Out of a window, the view keeps the scene it was last in: a full-screen player covering it takes
    /// it out, and the window it is still part of can close while the player is up.
    override func didMoveToWindow() {
        super.didMoveToWindow()
        guard let scene = window?.windowScene else { return }
        onScene?(scene)
        observe(scene)
    }

    /// The notification is the one signal that always arrives: a scene that disconnects is not
    /// guaranteed to call the view's life-cycle methods. A selector observer goes with the view.
    private func observe(_ scene: UIWindowScene) {
        guard scene !== watched else { return }
        notifications.removeObserver(self, name: UIScene.didDisconnectNotification, object: nil)
        watched = scene
        notifications.addObserver(self, selector: #selector(sceneDidDisconnect), name: UIScene.didDisconnectNotification, object: scene)
    }

    @objc private func sceneDidDisconnect() {
        onGone?()
    }
}

#if os(iOS)
enum AppTab: Hashable {
    case home
    case films
    case series
    case settings
    case search
}

struct MainTabs: View {
    @State private var selectedTab: AppTab = .home
    @State private var portalState = SpatialPortalState()
    @Environment(PlaybackScene.self) private var playbackScene: PlaybackScene?

    private var playerShown: Bool { playbackScene?.presentation.shown ?? false }

    var body: some View {
        TabView(selection: $selectedTab) {
            Tab("Home", systemImage: "house", value: AppTab.home) {
                HomeView()
            }
            Tab("Films", systemImage: "film", value: AppTab.films) {
                MoviesView()
            }
            Tab("Series", systemImage: "tv", value: AppTab.series) {
                ShowsView()
            }
            Tab("Settings", systemImage: "gearshape", value: AppTab.settings) {
                SettingsView()
            }
            Tab("Search", systemImage: "magnifyingglass", value: AppTab.search, role: .search) {
                BrowseView()
            }
        }
        .tabViewStyle(.sidebarAdaptable)
        .tabBarMinimizeBehavior(.onScrollDown)
        .environment(portalState)
        .allowsHitTesting(!playerShown)
        .onChange(of: selectedTab) { _, _ in resignKeyboard() }
        .onChange(of: playerShown) { _, shown in if shown { resignKeyboard() } }
    }

    private func resignKeyboard() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}
#else
struct MainTabs: View {
    @State private var portalState = SpatialPortalState()

    var body: some View {
        TabView {
            HomeView()
                .tabItem { Label("Home", systemImage: "house") }
            MoviesView()
                .tabItem { Label("Films", systemImage: "film") }
            ShowsView()
                .tabItem { Label("Series", systemImage: "tv") }
            BrowseView()
                .tabItem { Label("Search", systemImage: "magnifyingglass") }
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gear") }
        }
        .environment(portalState)
    }
}
#endif
