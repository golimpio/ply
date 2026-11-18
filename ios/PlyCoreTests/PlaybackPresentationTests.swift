import Testing

// A window's hold on the play. It is what keeps one window's dismissal from taking another window's
// player down, so it is pinned here rather than left to the simulator (docs/M11-CLIENTS.md, "The apps").

@Suite struct PlaybackPresentationTests {
    @Test func aWindowIsShownFromTheFirstPresentAndALaterPlayKeepsItShown() {
        var window = PlaybackPresentation()
        #expect(window.shown == false)

        window.present()
        #expect(window.shown)

        window.present()
        #expect(window.shown, "a play replacing another stays in the same window")

        window.dismiss()
        #expect(window.shown == false)

        window.dismiss()
        #expect(window.shown == false, "dismissing a window with nothing on it is harmless")
    }

    @Test func aCollapsedPlayStaysHeldByItsWindowButLeavesTheScreen() {
        var window = PlaybackPresentation()
        window.collapse()
        #expect(window.collapsed == false, "a window without a play has nothing to collapse")

        window.present()
        #expect(window.onScreen)
        window.collapse()
        #expect(window.shown, "the window still holds the play, for the mini-player")
        #expect(window.collapsed)
        #expect(window.onScreen == false, "the tabs under it take touches again")

        window.expand()
        #expect(window.onScreen)

        window.collapse()
        window.present()
        #expect(window.onScreen, "a new play comes up on the screen")

        window.collapse()
        window.dismiss()
        #expect(window.shown == false)
        #expect(window.collapsed == false, "an ended play leaves no mini-player")
    }

    @Test func twoWindowsKeepTheirOwnState() {
        var first = PlaybackPresentation()
        var second = PlaybackPresentation()

        first.present()
        #expect(first.shown)
        #expect(second.shown == false, "the window without a player shows nothing")

        second.present()
        first.dismiss()
        #expect(second.shown, "the first window taking its play down leaves the second alone")
        #expect(first.shown == false)
    }
}
