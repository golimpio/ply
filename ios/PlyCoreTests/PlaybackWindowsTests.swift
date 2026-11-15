import Foundation
import Testing

// Which window a play nobody started goes in, and what its ack says. Both are decisions the iPad's
// several windows depend on, so they are pinned here rather than left to the simulator.

private final class Window: PlaybackWindow {}

@Suite struct PlaybackWindowsTests {
    @Test func theNewestWindowStillThereIsTheOneAPlayGoesIn() {
        var windows = PlaybackWindows()
        let first = Window()
        let second = Window()
        windows.add(first)
        windows.add(second)
        #expect(windows.newest === second)
        #expect(windows.count == 2)
    }

    @Test func aWindowClosedAtTheEndDoesNotHideTheOneBeforeIt() {
        var windows = PlaybackWindows()
        let first = Window()
        windows.add(first)
        do {
            let second = Window()
            windows.add(second)
            #expect(windows.newest === second)
        } // the second window goes out of scope: its entry is dead but still at the end
        #expect(windows.newest === first, "a dead entry does not answer for a live window")
        #expect(windows.count == 1)
    }

    @Test func aWindowIsListedOnceAndRemovingItForgetsIt() {
        var windows = PlaybackWindows()
        let window = Window()
        windows.add(window)
        windows.add(window)
        #expect(windows.count == 1, "a window that appears twice is still one window")
        #expect(windows.contains(window))

        windows.remove(window)
        #expect(windows.contains(window) == false)
        #expect(windows.newest == nil)
        #expect(windows.count == 0)
    }

    @Test func removingAWindowThatIsNotThereIsHarmless() {
        var windows = PlaybackWindows()
        let there = Window()
        windows.add(there)
        windows.remove(Window())
        #expect(windows.newest === there)
    }
}

@Suite struct PlayCommandResultTests {
    @Test func onlyAPlayThatStartedIsAnsweredAsSuccess() {
        #expect(PlayCommandResult.after(hasServer: true, hadWindow: true, started: true) == .started)
        #expect(PlayCommandResult.after(hasServer: true, hadWindow: true, started: false) == .notStarted)
        #expect(PlayCommandResult.after(hasServer: true, hadWindow: false, started: false) == .noWindow)
        #expect(PlayCommandResult.after(hasServer: false, hadWindow: false, started: false) == .noServer)
        #expect(PlayCommandResult.after(hasServer: false, hadWindow: true, started: true) == .started,
                "a play that started is a success even if the connection went away after it")
    }

    @Test func eachFailureSaysWhatWasWrong() {
        #expect(PlayCommandResult.started.failure == nil)
        #expect(PlayCommandResult.noServer.failure == "No server to play from")
        #expect(PlayCommandResult.noWindow.failure == "No window to play in")
        #expect(PlayCommandResult.notStarted.failure == "Playback could not start")
    }
}
