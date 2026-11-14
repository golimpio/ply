import SwiftUI

/// The ply app on iPhone and iPad. The app shell (the root and the tabs) is shared with the TV
/// (AppShell.swift); only this entry point and the platform settings are platform-specific.
@main
struct PlyApp: App {
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var scenePhase

    init() {
        configureAppCaches(memoryCapacity: 64 << 20, diskCapacity: 512 << 20)
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .task { await model.start() }
        }
        .onChange(of: scenePhase) { _, phase in model.scenePhaseChanged(phase) }
    }
}
