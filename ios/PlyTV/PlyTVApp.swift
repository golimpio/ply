import AVFoundation
import SwiftUI

/// The ply app on Apple TV: the same core, feed, player and pages as iOS, with the tvOS focus
/// navigation. It registers as `tvos` (Device.kind), and Picture in Picture is iOS-only.
@main
struct PlyTVApp: App {
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var scenePhase

    init() {
        configureAppCaches(memoryCapacity: 128 << 20, diskCapacity: 1024 << 20)
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
