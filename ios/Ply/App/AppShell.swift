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

    var title: String {
        switch self {
        case .home: "Home"
        case .films: "Films"
        case .series: "Series"
        case .settings: "Settings"
        case .search: "Search"
        }
    }

    var systemImage: String {
        switch self {
        case .home: "house"
        case .films: "video"
        case .series: "tv"
        case .settings: "switch.2"
        case .search: "magnifyingglass"
        }
    }

    private struct IconCacheKey: Hashable {
        let tab: AppTab
        let labeled: Bool
    }

    private static var iconCache: [IconCacheKey: UIImage] = [:]

    /// Locks the tab icon to the exact outline SF Symbol as a template `UIImage` so UIKit's `_UITabButton`
    /// cannot substitute `.fill` variants on initial render or when toggling `Show tab labels`.
    func templateIcon(labeled: Bool) -> UIImage {
        let key = IconCacheKey(tab: self, labeled: labeled)
        if let cached = Self.iconCache[key] { return cached }
        let config = UIImage.SymbolConfiguration(pointSize: labeled ? 17 : 19, weight: .medium)
        guard let symbol = UIImage(systemName: systemImage, withConfiguration: config)?
            .withTintColor(.black, renderingMode: .alwaysOriginal) else {
            return UIImage()
        }
        let format = UIGraphicsImageRendererFormat.default()
        format.opaque = false
        let rendered = UIGraphicsImageRenderer(size: symbol.size, format: format)
            .image { _ in symbol.draw(in: CGRect(origin: .zero, size: symbol.size)) }
            .withRenderingMode(.alwaysTemplate)
        Self.iconCache[key] = rendered
        return rendered
    }
}

struct MainTabs: View {
    @AppStorage(PlyPreferences.showTabLabelsKey) private var showTabLabels = false
    @State private var selectedTab: AppTab = .home
    @Environment(PlaybackScene.self) private var playbackScene: PlaybackScene?

    /// The player covers the tabs (not while it is stepped aside for the mini-player).
    private var playerShown: Bool { playbackScene?.presentation.onScreen ?? false }

    var body: some View {
        TabView(selection: $selectedTab) {
            tabItem(.home) { HomeView() }
            tabItem(.films) { MoviesView() }
            tabItem(.series) { ShowsView() }
            tabItem(.settings) { SettingsView() }
            // The system's search tab: the running iOS decides where it sits (a detached circle or in the
            // bar), keeps it on screen when the bar minimizes, and owns the search field.
            Tab(value: AppTab.search, role: .search) {
                SearchTabView()
            } label: {
                searchLabel
            }
        }
        .id(showTabLabels)
        .tabViewStyle(.sidebarAdaptable)
        .tabBarMinimizeBehavior(.onScrollDown)
        // Selecting Search focuses its field; dismissing search returns to the tab it was opened from.
        .tabViewSearchActivation(.searchTabSelection)
        .modifier(MiniPlayerAccessory(scene: playbackScene))
        .allowsHitTesting(!playerShown)
        .onChange(of: playerShown) { _, shown in if shown { resignKeyboard() } }
    }

    private func tabItem<Content: View>(
        _ tab: AppTab,
        @ViewBuilder content: () -> Content
    ) -> some TabContent<AppTab> {
        Tab(value: tab) {
            content()
        } label: {
            tabLabel(tab.title, icon: Image(uiImage: tab.templateIcon(labeled: showTabLabels)))
                .environment(\.symbolVariants, .none)
        }
    }

    /// A tab's icon, titled only when Settings › Show tab labels is on (the title is always spoken).
    @ViewBuilder
    private func tabLabel(_ title: String, icon: Image) -> some View {
        if showTabLabels {
            Label { Text(title) } icon: { icon }
        } else {
            icon
                .accessibilityLabel(title)
        }
    }

    /// Search keeps the standard search symbol; only ply's own tabs use `templateIcon`.
    private var searchLabel: some View {
        tabLabel(AppTab.search.title, icon: Image(systemName: AppTab.search.systemImage))
    }

    private func resignKeyboard() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}

/// The mini-player in the tab bar's accessory while the play goes on, on a TV, with its remote hidden
/// (docs/M11-CLIENTS.md, "The mini-player").
private struct MiniPlayerAccessory: ViewModifier {
    let scene: PlaybackScene?

    func body(content: Content) -> some View {
        if #available(iOS 26.1, *) {
            content.tabViewBottomAccessory(isEnabled: scene?.miniPlayer != nil) { miniPlayer }
        } else {
            // Always attached: adding the accessory only while there is a play would rebuild the tab view, and
            // every tab would lose its place. iOS 26.0 hides an accessory with nothing in it.
            content.tabViewBottomAccessory { miniPlayer }
        }
    }

    @ViewBuilder private var miniPlayer: some View {
        if let scene, let remote = scene.miniPlayer {
            MiniPlayer(remote: remote, onExpand: { scene.expand() })
        }
    }
}
#else
struct MainTabs: View {

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
    }
}
#endif
