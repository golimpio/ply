import UIKit
import XCTest
@testable import Ply

/// Home's fanned poster deck: where each card sits, how dim the neighbours are, and where a drag lands.
#if os(iOS)
nonisolated final class FanDeckTests: XCTestCase {
    private let width = 272.0

    func testCentreCardSitsUntouched() {
        let centre = FanPose.at(0, cardWidth: width)
        XCTAssertEqual(centre.x, 0)
        XCTAssertEqual(centre.y, 0)
        XCTAssertEqual(centre.degrees, 0)
        XCTAssertEqual(centre.scale, 1)
        XCTAssertEqual(centre.dim, 0, "The centre poster is never darkened")
        XCTAssertEqual(centre.opacity, 1)
        XCTAssertTrue(centre.isHitTestable)
    }

    func testNeighboursAreDimmedAndFanOutSymmetrically() {
        let left = FanPose.at(-1, cardWidth: width)
        let right = FanPose.at(1, cardWidth: width)
        XCTAssertEqual(right.dim, 0.45, accuracy: 0.001, "The owner chose the Dim variation: neighbours 45% darker")
        XCTAssertEqual(left.dim, right.dim, accuracy: 0.001)
        XCTAssertEqual(left.x, -right.x, accuracy: 0.001)
        XCTAssertEqual(left.degrees, -right.degrees, accuracy: 0.001)
        XCTAssertGreaterThan(right.x, 0, "The next poster fans out to the right")
        XCTAssertGreaterThan(right.y, 0, "Neighbours sit lower than the centre, on the arc")
        XCTAssertLessThan(right.scale, 1)
        XCTAssertLessThan(right.zIndex, FanPose.at(0, cardWidth: width).zIndex, "Neighbours tuck behind the centre")
    }

    func testDeeperCardsAreDarkerUpToTheCap() {
        let second = FanPose.at(2, cardWidth: width)
        XCTAssertGreaterThan(second.dim, FanPose.at(1, cardWidth: width).dim)
        XCTAssertEqual(second.dim, 0.85, accuracy: 0.001, "No card goes fully black")
    }

    func testFarCardsAreHiddenAndCannotTakeTaps() {
        // Positive control: every poster an iPad shows, two either side, takes a tap that brings it to the centre.
        XCTAssertTrue(FanPose.at(1, cardWidth: width).isHitTestable)
        XCTAssertTrue(FanPose.at(-2, cardWidth: width).isHitTestable)
        XCTAssertEqual(FanPose.at(2, cardWidth: width).opacity, 1)
        // Further along the arc a card swings up over the page's header: it must not catch taps meant for it.
        XCTAssertFalse(FanPose.at(2.6, cardWidth: width).isHitTestable)
        let far = FanPose.at(3, cardWidth: width)
        XCTAssertEqual(far.opacity, 0)
        XCTAssertFalse(far.isHitTestable)
    }

    func testOneCardOfDragIsTheGapBetweenPostersSoTheCentreOneStaysUnderTheFinger() {
        let spacing = FanPose.spacing(cardWidth: width)
        XCTAssertEqual(spacing, FanPose.at(1, cardWidth: width).x, accuracy: 0.001)
        // Dragging one gap's width brings the neighbour to exactly where the centre card was.
        let position = FanDeckMotion.position(anchor: 2, translation: -CGFloat(spacing), spacing: CGFloat(spacing), count: 5)
        XCTAssertEqual(FanPose.at(3 - position, cardWidth: width).x, 0, accuracy: 0.001)
    }

    func testDragFollowsTheFingerAndResistsPastTheEnds() {
        XCTAssertEqual(FanDeckMotion.position(anchor: 2, translation: -136, spacing: 272, count: 5), 2.5, accuracy: 0.001)
        XCTAssertEqual(FanDeckMotion.position(anchor: 2, translation: 272, spacing: 272, count: 5), 1, accuracy: 0.001)
        // Past the first card the deck gives a little, not a full card.
        XCTAssertEqual(FanDeckMotion.position(anchor: 0, translation: 272, spacing: 272, count: 5), -0.3, accuracy: 0.001)
        XCTAssertEqual(FanDeckMotion.position(anchor: 4, translation: -272, spacing: 272, count: 5), 4.3, accuracy: 0.001)
    }

    func testReleaseLandsWhereTheFlickWasHeading() {
        // A short, slow drag springs back.
        XCTAssertEqual(FanDeckMotion.target(anchor: 2, translation: -60, velocity: 0, spacing: 272, count: 8), 2)
        // Past half a card it moves on, either way.
        XCTAssertEqual(FanDeckMotion.target(anchor: 2, translation: -150, velocity: 0, spacing: 272, count: 8), 3)
        XCTAssertEqual(FanDeckMotion.target(anchor: 2, translation: 150, velocity: 0, spacing: 272, count: 8), 1)
        // A quick flick carries a short drag on.
        XCTAssertEqual(FanDeckMotion.target(anchor: 2, translation: -40, velocity: -600, spacing: 272, count: 8), 3)
        // A hard flick travels at most three cards, and never off the ends.
        XCTAssertEqual(FanDeckMotion.target(anchor: 2, translation: -200, velocity: -8000, spacing: 272, count: 8), 5)
        XCTAssertEqual(FanDeckMotion.target(anchor: 1, translation: 200, velocity: 8000, spacing: 272, count: 8), 0)
        XCTAssertEqual(FanDeckMotion.target(anchor: 6, translation: -200, velocity: -8000, spacing: 272, count: 8), 7)
    }

    func testOneOrNoPosterNeverLandsOutsideTheList() {
        XCTAssertEqual(FanDeckMotion.target(anchor: 0, translation: -300, velocity: -8000, spacing: 272, count: 1), 0)
        XCTAssertEqual(FanDeckMotion.target(anchor: 0, translation: 300, velocity: 8000, spacing: 272, count: 1), 0)
        XCTAssertEqual(FanDeckMotion.target(anchor: 0, translation: -300, velocity: 0, spacing: 272, count: 0), 0)
    }

    func testSettleKeepsTheFingersSpeed() {
        // Moving left (velocity < 0) towards a higher card: the spring starts moving the same way.
        XCTAssertGreaterThan(FanDeckMotion.springVelocity(from: 2.4, to: 3, velocity: -500, spacing: 272), 0)
        // A flick against the target (released past half, still drifting back) starts against it.
        XCTAssertLessThan(FanDeckMotion.springVelocity(from: 2.6, to: 3, velocity: 300, spacing: 272), 0)
        XCTAssertEqual(FanDeckMotion.springVelocity(from: 3, to: 3, velocity: -500, spacing: 272), 0)
        // Released a hair from the card at speed: the spring's start is capped, not a lurch.
        XCTAssertEqual(FanDeckMotion.springVelocity(from: 2.98, to: 3, velocity: -5000, spacing: 272), 6)
    }

    func testVoiceOverScrollStepsOneCardWithinTheDeck() {
        XCTAssertEqual(FanDeckMotion.step(from: 1, by: 1, count: 4), 2)
        XCTAssertEqual(FanDeckMotion.step(from: 1, by: -1, count: 4), 0)
        XCTAssertEqual(FanDeckMotion.step(from: 3, by: 1, count: 4), 3)
        XCTAssertEqual(FanDeckMotion.step(from: 0, by: -1, count: 4), 0)
    }

    func testOnlySidewaysPansMoveTheDeckSoThePageStillScrolls() {
        XCTAssertTrue(FanDeckMotion.isHorizontal(translation: CGPoint(x: 12, y: 3), velocity: .zero))
        XCTAssertFalse(FanDeckMotion.isHorizontal(translation: CGPoint(x: 3, y: 12), velocity: .zero))
        // Moved past the slop but slowing to a stop: still decided by where the finger went.
        XCTAssertTrue(FanDeckMotion.isHorizontal(translation: CGPoint(x: -14, y: 2), velocity: CGPoint(x: 0, y: 40)))
        // A trackpad scroll that has not moved yet: its speed decides.
        XCTAssertTrue(FanDeckMotion.isHorizontal(translation: .zero, velocity: CGPoint(x: 300, y: 10)))
        XCTAssertFalse(FanDeckMotion.isHorizontal(translation: .zero, velocity: CGPoint(x: 10, y: 300)))
    }

    @MainActor
    func testPanAlsoFollowsTrackpadAndMouseWheelOnIPad() {
        let delegate = HorizontalPan.Coordinator()
        let pan = HorizontalPan.makePan(delegate: delegate)
        XCTAssertEqual(pan.allowedScrollTypesMask, .all, "The scroll view this replaced followed both on iPad")
        XCTAssertTrue(pan.delegate === delegate, "The delegate keeps vertical drags for the page's scroll view")
    }
}
#endif
