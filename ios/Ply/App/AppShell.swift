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
enum AppTab: Int, CaseIterable, Identifiable {
    case home
    case films
    case series
    case search
    case settings

    var id: Int { rawValue }

    var tabIcon: PlyTabIcon.Kind {
        switch self {
        case .home: .home
        case .films: .films
        case .series: .series
        case .search: .search
        case .settings: .settings
        }
    }

    var label: String {
        switch self {
        case .home: "Home"
        case .films: "Films"
        case .series: "Series"
        case .search: "Search"
        case .settings: "Settings"
        }
    }
}

/// A floating minimalist glass dock without text labels, featuring crisp minimalist icons and an orange highlight.
struct FloatingTabBar: View {
    @Binding var selectedTab: AppTab
    @Namespace private var animation

    var body: some View {
        HStack(spacing: 0) {
            ForEach(AppTab.allCases) { tab in
                let isSelected = selectedTab == tab
                Button {
                    if selectedTab != tab {
                        withAnimation(.spring(response: 0.35, dampingFraction: 0.78)) {
                            selectedTab = tab
                        }
                    }
                } label: {
                    ZStack {
                        if isSelected {
                            Capsule()
                                .fill(LinearGradient.plyAmber)
                                .matchedGeometryEffect(id: "ActiveTabIndicator", in: animation)
                                .frame(width: 48, height: 38)
                                .shadow(color: Color.orange.opacity(0.45), radius: 8, y: 3)
                        }

                        PlyTabIcon(kind: tab.tabIcon, isFilled: isSelected, size: 20)
                            .foregroundStyle(isSelected ? Color.white : Color.white.opacity(0.46))
                            .scaleEffect(isSelected ? 1.05 : 1.0)
                    }
                    .frame(maxWidth: .infinity)
                    .frame(height: PlyDesign.floatingTabBarItemHeight)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(tab.label)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, PlyDesign.floatingTabBarVerticalPadding)
        .plyGlassCapsule(strokeOpacity: 0.16)
        .shadow(color: Color.black.opacity(0.32), radius: 14, y: 6)
        .frame(maxWidth: 360)
        .sensoryFeedback(.selection, trigger: selectedTab)
    }
}

struct MainTabs: View {
    @State private var selectedTab: AppTab = .home
    @State private var portalState = SpatialPortalState()
    @Environment(PlaybackScene.self) private var playbackScene: PlaybackScene?

    private var playerShown: Bool { playbackScene?.presentation.shown ?? false }

    var body: some View {
        ZStack(alignment: .bottom) {
            // Tab containers - kept alive to preserve scroll and navigation state without UIKit UITabBar
            ZStack {
                tabContainer(HomeView(), tab: .home)
                tabContainer(MoviesView(), tab: .films)
                tabContainer(ShowsView(), tab: .series)
                tabContainer(BrowseView(), tab: .search)
                tabContainer(SettingsView(), tab: .settings)
            }
            .environment(portalState)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .ignoresSafeArea(.keyboard, edges: .bottom)

            FloatingTabBar(selectedTab: $selectedTab)
                .padding(.horizontal, 24)
                .padding(.bottom, PlyDesign.floatingTabBarBottomInset)
                .offset(y: portalState.isPresented ? 120 : 0)
                .opacity(portalState.isPresented ? 0 : 1)
                .allowsHitTesting(!portalState.isPresented)
                .animation(.spring(response: 0.45, dampingFraction: 0.86), value: portalState.isPresented)
        }
        .onChange(of: selectedTab) { _, _ in resignKeyboard() }
        .onChange(of: playerShown) { _, shown in if shown { resignKeyboard() } }
    }

    private func tabContainer<Content: View>(_ view: Content, tab: AppTab) -> some View {
        let isSelected = selectedTab == tab
        return view
            .opacity(isSelected ? 1 : 0)
            .allowsHitTesting(isSelected && !playerShown)
            .disabled(!isSelected || playerShown)
            .accessibilityHidden(!isSelected)
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
