#if os(iOS)
import SwiftUI
import UIKit

// Home's fanned poster deck (iPhone and iPad): the posters sit like a hand of cards pivoting
// about a point far below the centre card, and the ones either side are dimmed so the centre
// poster leads. `PeekingCanvasDeckView` draws the cards; this file holds the geometry and the drag.

nonisolated extension PlyDesign {
    /// The fan's pivot sits this many card widths below the centre card.
    static let fanPivotDepth: Double = 3
    /// Degrees between neighbouring cards.
    static let fanAngle: Double = 11
    /// Scale each card loses per step from the centre.
    static let fanScaleStep: Double = 0.05
    /// How far the first neighbours sit below the centre card, beyond the arc itself.
    static let fanDrop: Double = 12
    /// Black laid over a neighbour, per step from the centre, and the most any card gets.
    static let fanDim: Double = 0.45
    static let fanMaxDim: Double = 0.85
    /// Cards further than this from the centre are hidden: out there the arc swings them up over the page.
    static let fanVisibleSpan: Double = 3
    /// Cards closer than this take taps (an iPad shows two either side); further out they float over the page.
    static let fanTappableSpan: Double = 2.5
}

/// Where a card sits in the fan, `distance` cards from the centre (negative: to the left).
/// A fractional distance is a card mid-swipe.
nonisolated struct FanPose: Equatable {
    var x: Double
    var y: Double
    var degrees: Double
    var scale: Double
    var dim: Double
    var opacity: Double
    /// Hidden cards must not catch taps meant for whatever they float over.
    var isHitTestable: Bool
    var zIndex: Double

    static func at(_ distance: Double, cardWidth: Double) -> FanPose {
        let steps = abs(distance)
        let radius = cardWidth * PlyDesign.fanPivotDepth
        let theta = distance * PlyDesign.fanAngle * .pi / 180
        return FanPose(
            x: radius * sin(theta),
            y: radius * (1 - cos(theta)) + PlyDesign.fanDrop * min(steps, 1),
            degrees: distance * PlyDesign.fanAngle,
            scale: 1 - PlyDesign.fanScaleStep * min(steps, PlyDesign.fanVisibleSpan),
            dim: min(PlyDesign.fanMaxDim, PlyDesign.fanDim * steps),
            opacity: min(1, max(0, PlyDesign.fanVisibleSpan - steps)),
            isHitTestable: steps < PlyDesign.fanTappableSpan,
            zIndex: -steps
        )
    }

    /// How far apart neighbouring cards sit on screen: the drag's unit, so the centre poster stays under the finger.
    static func spacing(cardWidth: Double) -> Double {
        cardWidth * PlyDesign.fanPivotDepth * sin(PlyDesign.fanAngle * .pi / 180)
    }
}

/// Places a card in the fan. Animatable on the distance, so a spring sweeps the card along the
/// arc rather than cutting across it.
struct FanPlacement: ViewModifier, Animatable {
    var distance: Double
    let cardWidth: CGFloat
    let cornerRadius: CGFloat

    var animatableData: Double {
        get { distance }
        set { distance = newValue }
    }

    func body(content: Content) -> some View {
        let pose = FanPose.at(distance, cardWidth: Double(cardWidth))
        content
            .overlay {
                RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                    .fill(Color.black.opacity(pose.dim))
                    .allowsHitTesting(false)
            }
            .scaleEffect(pose.scale)
            .rotationEffect(.degrees(pose.degrees))
            .offset(x: pose.x, y: pose.y)
            .opacity(pose.opacity)
            .allowsHitTesting(pose.isHitTestable)
            .zIndex(pose.zIndex)
    }
}

/// The fan's drag: where the deck is under the finger, and which card a release lands on.
nonisolated enum FanDeckMotion {
    /// How much of a drag past the first or last card still moves the deck.
    static let edgeResistance = 0.3
    /// The most cards one flick travels.
    static let maxFlick = 3
    /// `UIScrollView`'s normal deceleration rate, so a flick coasts as far as it would in a scroll view.
    static let decelerationRate = 0.998

    /// The deck's position, in cards, during a drag that started on card `anchor`; `spacing` is one card of travel.
    static func position(anchor: Int, translation: CGFloat, spacing: CGFloat, count: Int) -> Double {
        let raw = Double(anchor) - Double(translation / spacing)
        let last = Double(max(count - 1, 0))
        if raw < 0 { return raw * edgeResistance }
        if raw > last { return last + (raw - last) * edgeResistance }
        return raw
    }

    /// The card a released drag settles on: where the finger's speed would have coasted it.
    static func target(anchor: Int, translation: CGFloat, velocity: CGFloat, spacing: CGFloat, count: Int) -> Int {
        guard count > 0 else { return 0 }
        let coast = Double(velocity) * decelerationRate / (1 - decelerationRate) / 1000
        let landing = Double(anchor) - (Double(translation) + coast) / Double(spacing)
        let card = min(max(Int(landing.rounded()), anchor - maxFlick), anchor + maxFlick)
        return min(max(card, 0), count - 1)
    }

    /// The finger's speed as the spring's initial velocity, which is relative to the distance left.
    static func springVelocity(from position: Double, to target: Int, velocity: CGFloat, spacing: CGFloat) -> Double {
        let remaining = Double(target) - position
        guard abs(remaining) > 0.01 else { return 0 }
        let cardsPerSecond = -Double(velocity / spacing)
        return max(-6, min(6, cardsPerSecond / remaining))
    }

    /// A one-card step for VoiceOver's scroll, kept within the deck.
    static func step(from index: Int, by delta: Int, count: Int) -> Int {
        min(max(index + delta, 0), max(count - 1, 0))
    }

    /// Whether a pan is sideways. Decided on the distance moved, which past the touch slop is never zero;
    /// the speed only breaks a tie (a trackpad scroll can begin before it has moved).
    static func isHorizontal(translation: CGPoint, velocity: CGPoint) -> Bool {
        if abs(translation.x) != abs(translation.y) { return abs(translation.x) > abs(translation.y) }
        return abs(velocity.x) > abs(velocity.y)
    }

    /// Every finger update goes through a short spring rather than straight in, so grabbing the deck while it
    /// is still settling picks it up from where it is drawn instead of snapping it to where it was heading.
    static let follow = Animation.interactiveSpring(response: 0.16, dampingFraction: 0.9)

    /// Carries the finger's speed into the settle so a release never feels like a hand-off.
    static func settle(initialVelocity: Double) -> Animation {
        .interpolatingSpring(duration: 0.55, bounce: 0.14, initialVelocity: initialVelocity)
    }
}

/// A horizontal pan that leaves vertical drags to the page's scroll view.
struct HorizontalPan: UIGestureRecognizerRepresentable {
    var onChange: (_ translation: CGFloat) -> Void
    var onEnd: (_ translation: CGFloat, _ velocity: CGFloat) -> Void

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator { Coordinator() }

    func makeUIGestureRecognizer(context: Context) -> UIPanGestureRecognizer {
        Self.makePan(delegate: context.coordinator)
    }

    /// Also follows a trackpad's two-finger swipe and a mouse wheel, as the scroll view it replaced did on iPad.
    static func makePan(delegate: UIGestureRecognizerDelegate) -> UIPanGestureRecognizer {
        let pan = UIPanGestureRecognizer()
        pan.allowedScrollTypesMask = .all
        pan.delegate = delegate
        return pan
    }

    func handleUIGestureRecognizerAction(_ pan: UIPanGestureRecognizer, context: Context) {
        let translation = pan.translation(in: pan.view).x
        switch pan.state {
        case .began, .changed:
            onChange(translation)
        case .ended, .cancelled, .failed:
            onEnd(translation, pan.state == .ended ? pan.velocity(in: pan.view).x : 0)
        default:
            break
        }
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        func gestureRecognizerShouldBegin(_ recognizer: UIGestureRecognizer) -> Bool {
            guard let pan = recognizer as? UIPanGestureRecognizer else { return true }
            return FanDeckMotion.isHorizontal(
                translation: pan.translation(in: pan.view), velocity: pan.velocity(in: pan.view))
        }
    }
}
#endif
