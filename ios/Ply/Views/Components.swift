import SwiftUI

// MARK: - Brand Palette & Gradients

nonisolated extension Color {
    static let plyAmberHighlight = Color(red: 0.98, green: 0.62, blue: 0.22)
    static let plyAmberDeep = Color(red: 0.92, green: 0.41, blue: 0.11)
    static let plyAccent = plyAmberHighlight
}

nonisolated enum PlyPreferences {
    static let showTabLabelsKey = "ply.showTabLabels"

    enum Movies {
        static let sortKey = "ply.movies.sort"
        static let genreKey = "ply.movies.genre"
        static let decadeKey = "ply.movies.decade"
        static let only4KKey = "ply.movies.only4K"
        static let onlyHDRKey = "ply.movies.onlyHDR"
        static let onlyUnwatchedKey = "ply.movies.onlyUnwatched"
    }

    enum Shows {
        static let sortKey = "ply.shows.sort"
        static let decadeKey = "ply.shows.decade"
        static let onlyUnwatchedKey = "ply.shows.onlyUnwatched"
    }
}

nonisolated extension LinearGradient {
    /// Warm amber-orange gradient shared by the ply brand wordmark, hero play buttons, and accents.
    static var plyAmber: LinearGradient {
        LinearGradient(
            colors: [.plyAmberHighlight, .plyAmberDeep],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }

    static var plyAmberHorizontal: LinearGradient {
        LinearGradient(
            colors: [.plyAmberHighlight, .plyAmberDeep],
            startPoint: .leading,
            endPoint: .trailing
        )
    }
}

/// The official ply brand logo: lowercase geometric wordmark with a rounded amber-orange play triangle inside `p`.
struct PlyLogo: View {
    var size: CGFloat = 28.6
    var color: Color = .primary

    private static let aspectRatio: CGFloat = 698.0 / 488.0

    var body: some View {
        ZStack {
            PlyLogoWordmarkShape()
                .fill(color)

            PlyLogoPlayShape()
                .fill(LinearGradient.plyAmber)
        }
        .frame(width: size * Self.aspectRatio, height: size)
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Ply")
    }
}

nonisolated struct PlyLogoWordmarkShape: Shape {
    func path(in rect: CGRect) -> SwiftUI.Path {
        var path = SwiftUI.Path()
        // p
        path.move(to: CGPoint(x: 0, y: 109))
        path.addQuadCurve(to: CGPoint(x: 12, y: 97), control: CGPoint(x: 0, y: 97))
        path.addLine(to: CGPoint(x: 66, y: 97))
        path.addQuadCurve(to: CGPoint(x: 78, y: 109), control: CGPoint(x: 78, y: 97))
        path.addLine(to: CGPoint(x: 78, y: 115))
        path.addCurve(to: CGPoint(x: 158, y: 93), control1: CGPoint(x: 100, y: 98), control2: CGPoint(x: 128, y: 93))
        path.addCurve(to: CGPoint(x: 292, y: 230.5), control1: CGPoint(x: 234, y: 93), control2: CGPoint(x: 292, y: 151))
        path.addCurve(to: CGPoint(x: 158, y: 368), control1: CGPoint(x: 292, y: 310), control2: CGPoint(x: 234, y: 368))
        path.addCurve(to: CGPoint(x: 78, y: 346), control1: CGPoint(x: 128, y: 368), control2: CGPoint(x: 100, y: 363))
        path.addLine(to: CGPoint(x: 78, y: 475))
        path.addQuadCurve(to: CGPoint(x: 66, y: 487), control: CGPoint(x: 78, y: 487))
        path.addLine(to: CGPoint(x: 12, y: 487))
        path.addQuadCurve(to: CGPoint(x: 0, y: 475), control: CGPoint(x: 0, y: 487))
        path.closeSubpath()

        // l
        path.addRoundedRect(in: CGRect(x: 318, y: 0, width: 78, height: 364), cornerSize: CGSize(width: 12, height: 12))

        // straight-tail y
        path.move(to: CGPoint(x: 422, y: 112))
        path.addQuadCurve(to: CGPoint(x: 433, y: 97), control: CGPoint(x: 418, y: 97))
        path.addLine(to: CGPoint(x: 486, y: 97))
        path.addQuadCurve(to: CGPoint(x: 502, y: 108), control: CGPoint(x: 497, y: 97))
        path.addLine(to: CGPoint(x: 556, y: 241))
        path.addQuadCurve(to: CGPoint(x: 562, y: 241), control: CGPoint(x: 559, y: 247))
        path.addLine(to: CGPoint(x: 616, y: 108))
        path.addQuadCurve(to: CGPoint(x: 632, y: 97), control: CGPoint(x: 621, y: 97))
        path.addLine(to: CGPoint(x: 685, y: 97))
        path.addQuadCurve(to: CGPoint(x: 695, y: 112), control: CGPoint(x: 700, y: 97))
        path.addLine(to: CGPoint(x: 548, y: 475))
        path.addQuadCurve(to: CGPoint(x: 531, y: 487), control: CGPoint(x: 543, y: 487))
        path.addLine(to: CGPoint(x: 478, y: 487))
        path.addQuadCurve(to: CGPoint(x: 469, y: 473), control: CGPoint(x: 464, y: 487))
        path.addLine(to: CGPoint(x: 517, y: 361))
        path.closeSubpath()

        return path.applying(CGAffineTransform(scaleX: rect.width / 698.0, y: rect.height / 488.0))
    }
}

nonisolated struct PlyLogoPlayShape: Shape {
    func path(in rect: CGRect) -> SwiftUI.Path {
        var path = SwiftUI.Path()
        path.move(to: CGPoint(x: 84, y: 230.5))
        path.addArc(tangent1End: CGPoint(x: 84, y: 139.08), tangent2End: CGPoint(x: 256.43, y: 230.5), radius: 18)
        path.addArc(tangent1End: CGPoint(x: 256.43, y: 230.5), tangent2End: CGPoint(x: 84, y: 321.92), radius: 18)
        path.addArc(tangent1End: CGPoint(x: 84, y: 321.92), tangent2End: CGPoint(x: 84, y: 139.08), radius: 18)
        path.closeSubpath()

        return path.applying(CGAffineTransform(scaleX: rect.width / 698.0, y: rect.height / 488.0))
    }
}

/// The ply brand mark (alias to PlyLogo for consistent authentic branding across all views).
typealias PlyBrand = PlyLogo

/// An inline top search bar used on tvOS where navigation-drawer search is unavailable.
struct PlySearchField: View {
    @Binding var text: String
    var prompt: String
    @FocusState private var isFocused: Bool
    @Environment(\.isEnabled) private var isEnabled

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.secondary)

            TextField(prompt, text: $text)
                .font(.subheadline)
                .focused($isFocused)
                .autocorrectionDisabled()
                #if os(iOS)
                .textInputAutocapitalization(.never)
                .submitLabel(.search)
                #endif

            if !text.isEmpty {
                Button {
                    text = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        #if os(iOS)
        .plySubtleCapsule(strokeOpacity: 0.14)
        #else
        .background(.quaternary, in: .rect(cornerRadius: 12))
        #endif
        .onChange(of: isEnabled) { _, enabled in
            if !enabled { isFocused = false }
        }
    }
}

/// Wraps variable-width items onto horizontal rows sized to each item's intrinsic width.
struct WrappingFlowLayout: Layout {
    var spacing: CGFloat = 8
    var lineSpacing: CGFloat = 8

    nonisolated static func computeLayout(
        sizes: [CGSize],
        maxWidth: CGFloat,
        spacing: CGFloat,
        lineSpacing: CGFloat
    ) -> (frames: [CGRect], totalSize: CGSize) {
        var frames: [CGRect] = []
        frames.reserveCapacity(sizes.count)

        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var maxRowWidth: CGFloat = 0

        for size in sizes {
            if x > 0 && x + size.width > maxWidth {
                maxRowWidth = max(maxRowWidth, x - spacing)
                x = 0
                y += rowHeight + lineSpacing
                rowHeight = 0
            }
            frames.append(CGRect(origin: CGPoint(x: x, y: y), size: size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }

        if x > 0 {
            maxRowWidth = max(maxRowWidth, x - spacing)
        }
        let totalHeight = sizes.isEmpty ? 0 : y + rowHeight
        let resolvedWidth = maxWidth.isFinite ? maxWidth : maxRowWidth
        return (frames, CGSize(width: resolvedWidth, height: totalHeight))
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        return Self.computeLayout(
            sizes: sizes,
            maxWidth: proposal.width ?? .infinity,
            spacing: spacing,
            lineSpacing: lineSpacing
        ).totalSize
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let frames = Self.computeLayout(
            sizes: sizes,
            maxWidth: bounds.width,
            spacing: spacing,
            lineSpacing: lineSpacing
        ).frames

        for (subview, frame) in zip(subviews, frames) {
            subview.place(
                at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY),
                proposal: ProposedViewSize(frame.size)
            )
        }
    }
}
enum Route: Hashable {
    case item(Int64)
    case show(String)
    case person(Int64)
    case people
    case genre(Int64, String)
    case collection(Int64, String)
    case collections
}

/// The poster grid of the library and browse pages: a phone's cards, or the TV's, watched from across
/// a room.
let posterColumns: [GridItem] = {
    #if os(tvOS)
    [GridItem(.adaptive(minimum: 220), spacing: 28, alignment: .top)]
    #else
    [GridItem(.adaptive(minimum: 105), spacing: 12, alignment: .top)]
    #endif
}()

/// One card of a shelf: the poster grid's size, one at a time.
let shelfCardWidth: CGFloat = {
    #if os(tvOS)
    240
    #else
    120
    #endif
}()

/// The width of a 16:9 landscape or upcoming card in a shelf.
let shelfWideCardWidth: CGFloat = {
    #if os(tvOS)
    360
    #else
    240
    #endif
}()

/// The space between the cards of a shelf.
let shelfSpacing: CGFloat = {
    #if os(tvOS)
    28
    #else
    12
    #endif
}()

/// A genre's tile: an adaptive grid of them, with room for a name at 10 feet.
let genreTileWidth: CGFloat = {
    #if os(tvOS)
    220
    #else
    150
    #endif
}()
/// The rail's letter for a title: A to Z (case-insensitive, accents stripped), else # (digits, symbols).
func alphabetLetter(of title: String) -> String {
    let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let first = trimmed.folding(options: [.diacriticInsensitive, .caseInsensitive], locale: .current).first else {
        return "#"
    }
    let upper = String(first).uppercased()
    return (upper >= "A" && upper <= "Z") ? upper : "#"
}

/// Normalized scroll geometry used to drive `FastScrollScrubber` across any adaptive grid column count.
nonisolated struct FastScrollMetrics: Equatable, Sendable {
    static let minimumItemCount = 8
    static let defaultTopInset: CGFloat = 80
    static let defaultBottomInset: CGFloat = 110
    static let minTrackHeight: CGFloat = 100
    static let thumbHitSize = CGSize(width: 48, height: 58)

    let offsetY: CGFloat
    let contentHeight: CGFloat
    let containerHeight: CGFloat

    var scrollableHeight: CGFloat { max(0, contentHeight - containerHeight) }
    var isScrollable: Bool { scrollableHeight > 40 }

    var progress: CGFloat {
        guard scrollableHeight > 0 else { return 0 }
        return max(0, min(1, offsetY / scrollableHeight))
    }

    func itemIndex(count: Int) -> Int {
        Self.clampedIndex(progress: progress, count: count)
    }

    static func trackHeight(
        containerHeight: CGFloat,
        topInset: CGFloat = defaultTopInset,
        bottomInset: CGFloat = defaultBottomInset
    ) -> CGFloat {
        max(minTrackHeight, containerHeight - topInset - bottomInset)
    }

    static func thumbCenterY(
        progress: CGFloat,
        containerHeight: CGFloat,
        topInset: CGFloat = defaultTopInset,
        bottomInset: CGFloat = defaultBottomInset
    ) -> CGFloat {
        let clamped = max(0, min(1, progress))
        let track = trackHeight(containerHeight: containerHeight, topInset: topInset, bottomInset: bottomInset)
        return topInset + (clamped * track)
    }

    static func progress(
        forLocationY locationY: CGFloat,
        containerHeight: CGFloat,
        topInset: CGFloat = defaultTopInset,
        bottomInset: CGFloat = defaultBottomInset
    ) -> CGFloat {
        let track = trackHeight(containerHeight: containerHeight, topInset: topInset, bottomInset: bottomInset)
        return max(0, min(1, (locationY - topInset) / track))
    }

    static func clampedIndex(progress: CGFloat, count: Int) -> Int {
        guard count > 1 else { return 0 }
        let clamped = max(0, min(1, progress))
        return max(0, min(count - 1, Int((clamped * CGFloat(count - 1)).rounded())))
    }

    static func shouldRevealOnScroll(
        oldOffsetY: CGFloat,
        newMetrics: FastScrollMetrics,
        itemCount: Int,
        isDragging: Bool
    ) -> Bool {
        itemCount >= minimumItemCount &&
            newMetrics.isScrollable &&
            !isDragging &&
            abs(oldOffsetY - newMetrics.offsetY) > 0.5
    }

    static func positionText(index: Int, count: Int) -> String {
        guard count > 0 else { return "0 / 0" }
        let clamped = max(0, min(count - 1, index))
        return "\( (clamped + 1).formatted() ) / \(count.formatted())"
    }
}

#if os(iOS)
/// A fast-scrolling Liquid Glass thumb pill along the trailing edge with an offset floating loupe.
struct FastScrollScrubber: View {
    private static let trackCoordinateSpace = "PlyFastScrollTrack"

    let itemCount: Int
    let currentIndex: Int
    let currentLabel: String
    let currentTitle: String?
    let onScrub: (Int) -> Void

    @Binding var isVisible: Bool
    @Binding var isDragging: Bool
    @Binding var progress: CGFloat
    @State private var lastReportedIndex: Int = -1
    @State private var hapticToken = 0

    private var activeLabel: String {
        currentLabel.isEmpty ? "•" : currentLabel
    }

    private var thumbPill: some View {
        VStack(spacing: 3) {
            Circle()
                .fill(Color.plyAmberHighlight)
                .frame(width: 5, height: 5)

            Text(activeLabel)
                .font(.system(size: activeLabel.count > 2 ? 10 : 11, weight: .bold, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(.primary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)

            Image(systemName: "chevron.up.chevron.down")
                .font(.system(size: 8, weight: .bold))
                .foregroundStyle(.secondary)
        }
        .frame(minWidth: 28)
        .padding(.horizontal, 6)
        .padding(.vertical, 8)
        .glassEffect(.regular.interactive(), in: .capsule)
        .overlay(
            Capsule()
                .strokeBorder(Color.plyAmberHighlight.opacity(isDragging ? 0.9 : 0.25), lineWidth: isDragging ? 1.5 : 1)
        )
        .shadow(color: Color.black.opacity(0.32), radius: isDragging ? 10 : 6, y: 3)
        .scaleEffect(isDragging ? 1.10 : 1.0)
        .animation(.spring(response: 0.24, dampingFraction: 0.72), value: isDragging)
    }

    private var loupeView: some View {
        HStack(spacing: 12) {
            Text(activeLabel)
                .font(.system(size: activeLabel.count > 2 ? 20 : 28, weight: .bold, design: .serif))
                .monospacedDigit()
                .foregroundStyle(Color(red: 0.08, green: 0.075, blue: 0.07))
                .lineLimit(1)
                .minimumScaleFactor(0.75)

            Rectangle()
                .fill(Color.black.opacity(0.14))
                .frame(width: 1, height: 28)

            VStack(alignment: .leading, spacing: 2) {
                if let currentTitle, !currentTitle.isEmpty {
                    Text(currentTitle)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(Color(red: 0.08, green: 0.075, blue: 0.07))
                        .lineLimit(1)
                        .frame(maxWidth: 132, alignment: .leading)
                }

                Text(FastScrollMetrics.positionText(index: currentIndex, count: itemCount))
                    .font(.system(size: 10, weight: .medium, design: .monospaced))
                    .foregroundStyle(Color.black.opacity(0.58))
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .fill(Color(red: 0.95, green: 0.94, blue: 0.91).opacity(0.95))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .strokeBorder(Color.black.opacity(0.08), lineWidth: 1)
        )
        .shadow(color: Color.black.opacity(0.45), radius: 18, y: 6)
    }

    var body: some View {
        GeometryReader { geo in
            let currentY = FastScrollMetrics.thumbCenterY(progress: progress, containerHeight: geo.size.height)
            let trackH = FastScrollMetrics.trackHeight(containerHeight: geo.size.height)

            ZStack(alignment: .topTrailing) {
                Color.clear
                    .allowsHitTesting(false)

                if isVisible {
                    Capsule()
                        .fill(Color.white.opacity(isDragging ? 0.22 : 0.10))
                        .frame(width: 3, height: trackH)
                        .padding(.top, FastScrollMetrics.defaultTopInset)
                        .padding(.trailing, 18)
                        .allowsHitTesting(false)
                        .transition(.opacity)

                    if isDragging {
                        loupeView
                            .padding(.trailing, 72)
                            .offset(y: currentY - 30)
                            .allowsHitTesting(false)
                            .transition(.scale(scale: 0.9, anchor: .trailing).combined(with: .opacity))
                    }

                    thumbPill
                        .frame(
                            width: FastScrollMetrics.thumbHitSize.width,
                            height: FastScrollMetrics.thumbHitSize.height,
                            alignment: .trailing
                        )
                        .padding(.trailing, 4)
                        .contentShape(Rectangle())
                        .offset(y: currentY - (FastScrollMetrics.thumbHitSize.height / 2))
                        .gesture(
                            DragGesture(minimumDistance: 0, coordinateSpace: .named(Self.trackCoordinateSpace))
                                .onChanged { value in
                                    isDragging = true
                                    isVisible = true
                                    let p = FastScrollMetrics.progress(
                                        forLocationY: value.location.y,
                                        containerHeight: geo.size.height
                                    )
                                    progress = p

                                    guard itemCount > 0 else { return }
                                    let clamped = FastScrollMetrics.clampedIndex(progress: p, count: itemCount)
                                    if clamped != lastReportedIndex {
                                        lastReportedIndex = clamped
                                        onScrub(clamped)
                                    }
                                }
                                .onEnded { _ in
                                    isDragging = false
                                    lastReportedIndex = -1
                                }
                        )
                        .transition(.scale(scale: 0.85, anchor: .trailing).combined(with: .opacity))
                }
            }
            .coordinateSpace(name: Self.trackCoordinateSpace)
            .opacity(isVisible ? 1 : 0)
            .animation(.easeInOut(duration: 0.22), value: isVisible)
            .animation(.spring(response: 0.24, dampingFraction: 0.78), value: isDragging)
            .onChange(of: currentLabel) { oldValue, newValue in
                if isDragging, oldValue != newValue {
                    hapticToken += 1
                }
            }
            .sensoryFeedback(.selection, trigger: hapticToken)
        }
    }
}

private struct FastScrollScrubberModifier: ViewModifier {
    let itemCount: Int
    let labelAt: (Int) -> String
    let titleAt: ((Int) -> String)?
    let onScrollTo: (Int) -> Void

    @State private var isVisible = false
    @State private var isDragging = false
    @State private var progress: CGFloat = 0
    @State private var currentIndex = 0
    @State private var currentLabel = ""
    @State private var currentTitle: String?
    @State private var hideToken = 0

    func body(content: Content) -> some View {
        ZStack(alignment: .trailing) {
            content
                .onScrollGeometryChange(for: FastScrollMetrics.self) { geo in
                    FastScrollMetrics(
                        offsetY: geo.contentOffset.y + geo.contentInsets.top,
                        contentHeight: geo.contentSize.height,
                        containerHeight: geo.containerSize.height
                    )
                } action: { oldValue, newValue in
                    guard FastScrollMetrics.shouldRevealOnScroll(
                        oldOffsetY: oldValue.offsetY,
                        newMetrics: newValue,
                        itemCount: itemCount,
                        isDragging: isDragging
                    ) else { return }

                    let index = newValue.itemIndex(count: itemCount)
                    progress = newValue.progress
                    currentIndex = index
                    currentLabel = labelAt(index)
                    currentTitle = titleAt?(index)
                    if !isVisible {
                        withAnimation(.easeInOut(duration: 0.2)) {
                            isVisible = true
                        }
                    }
                    hideToken += 1
                }

            if itemCount >= FastScrollMetrics.minimumItemCount {
                FastScrollScrubber(
                    itemCount: itemCount,
                    currentIndex: currentIndex,
                    currentLabel: currentLabel,
                    currentTitle: currentTitle,
                    onScrub: { index in
                        currentIndex = index
                        currentLabel = labelAt(index)
                        currentTitle = titleAt?(index)
                        onScrollTo(index)
                        hideToken += 1
                    },
                    isVisible: $isVisible,
                    isDragging: $isDragging,
                    progress: $progress
                )
            }
        }
        .task(id: hideToken) {
            guard hideToken > 0, isVisible, !isDragging else { return }
            try? await Task.sleep(for: .seconds(1.8))
            guard !Task.isCancelled, !isDragging else { return }
            withAnimation(.easeOut(duration: 0.35)) {
                isVisible = false
            }
        }
        .onChange(of: isDragging) { _, dragging in
            if !dragging, isVisible {
                hideToken += 1
            }
        }
    }
}
#endif

extension View {
    /// Attaches the iOS edge scrubber synced via `onScrollGeometryChange` with automatic hide timer cancellation.
    @ViewBuilder func plyFastScrollScrubber(
        itemCount: Int,
        labelAt: @escaping (Int) -> String,
        titleAt: ((Int) -> String)? = nil,
        onScrollTo: @escaping (Int) -> Void
    ) -> some View {
        #if os(iOS)
        modifier(FastScrollScrubberModifier(itemCount: itemCount, labelAt: labelAt, titleAt: titleAt, onScrollTo: onScrollTo))
        #else
        self
        #endif
    }

    /// The pages a Route opens.
    func plyDestinations() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case let .item(id): ItemDetailView(id: id)
            case let .show(name): ShowView(name: name)
            case let .person(id): PersonView(id: id)
            case .people: PeopleView()
            case let .genre(id, name): GenreView(id: id, name: name)
            case let .collection(id, name): CollectionView(id: id, name: name)
            case .collections: AllCollectionsView()
            }
        }
    }

    /// iOS places a page's title inline; tvOS has no navigation bar to place it in.
    @ViewBuilder func plyInlineTitle() -> some View {
        #if os(iOS)
        navigationBarTitleDisplayMode(.inline)
        #else
        self
        #endif
    }

    /// Search and pull-to-refresh are iOS; the TV has an inline `PlySearchField`.
    @ViewBuilder func plySearchable(_ text: Binding<String>, prompt: String, drawer: Bool = false) -> some View {
        #if os(iOS)
        if drawer {
            searchable(text: text, placement: .navigationBarDrawer(displayMode: .automatic), prompt: prompt)
        } else {
            searchable(text: text, prompt: prompt)
        }
        #else
        self
        #endif
    }

    @ViewBuilder func plyRefreshable(_ action: @escaping @MainActor () async -> Void) -> some View {
        #if os(iOS)
        refreshable { await action() }
        #else
        self
        #endif
    }

    /// A picker that opens a page: iOS only, and the default elsewhere (the TV focus engine).
    @ViewBuilder func plyPagePicker() -> some View {
        #if os(iOS)
        pickerStyle(.navigationLink)
        #else
        self
        #endif
    }
}

enum Format {
    private static let isoDateParser: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    private static let shortMonthDay: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "MMM d"
        return f
    }()

    /// "1h 41m", "52m".
    static func runtime(_ seconds: Double) -> String {
        let minutes = Int((seconds / 60).rounded())
        return minutes >= 60 ? "\(minutes / 60)h \(minutes % 60)m" : "\(minutes)m"
    }

    /// "1:02:03", "24:13".
    static func clock(_ seconds: Double) -> String {
        let s = Int(seconds)
        let (h, m, sec) = (s / 3600, s / 60 % 60, s % 60)
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%d:%02d", m, sec)
    }

    /// The parts of a line of facts, joined.
    static func facts(_ parts: String?...) -> String {
        parts.compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
    }

    /// "Today", "Tomorrow", "Nov 20", or the date string.
    static func airDate(_ s: String) -> String {
        guard let date = isoDateParser.date(from: s) else { return s }
        if Calendar.current.isDateInToday(date) { return "Today" }
        if Calendar.current.isDateInTomorrow(date) { return "Tomorrow" }
        return shortMonthDay.string(from: date)
    }
}

/// An image the server links to (with its URL token when it needs one), filling its frame via `ImagePipeline`.
struct RemoteImage: View {
    @Environment(Connection.self) private var connection
    let path: String?
    var placeholder = "photo"

    @State private var loadedImage: UIImage?
    @State private var loadedURL: URL?

    var body: some View {
        let url = connection.api.url(path)
        let displayImage = (loadedURL == url ? loadedImage : nil) ?? ImagePipeline.shared.cachedImage(for: url)

        Rectangle()
            .fill(.quaternary)
            .overlay {
                if let displayImage {
                    Image(uiImage: displayImage)
                        .resizable()
                        .scaledToFill()
                } else {
                    Image(systemName: placeholder).font(.title2).foregroundStyle(.tertiary)
                }
            }
            .clipped()
            .task(id: url) {
                guard let url else {
                    loadedImage = nil
                    loadedURL = nil
                    return
                }
                if let cached = ImagePipeline.shared.cachedImage(for: url) {
                    loadedImage = cached
                    loadedURL = url
                    return
                }
                let fetched = await ImagePipeline.shared.image(for: url)
                guard !Task.isCancelled else { return }
                loadedImage = fetched
                loadedURL = url
            }
            // The card around it says what it is; the picture itself says nothing.
            .accessibilityHidden(true)
    }
}

/// An ambient living chromatic wash behind detail views, driven by the artwork's palette.
struct AmbientBackdropWash: View {
    @Environment(Connection.self) private var connection
    let path: String?
    @State private var palette: PosterPalette = .fallback

    var body: some View {
        LivingMeshBackdrop(palette: palette)
            .task(id: path) {
                palette = await PosterPaletteExtractor.shared.palette(for: connection.api.url(path))
            }
    }
}

struct ProgressBar: View {
    let fraction: Double

    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Rectangle().fill(.black.opacity(0.55))
                Rectangle().fill(.tint).frame(width: g.size.width * fraction)
            }
        }
        .frame(height: 4)
        .accessibilityHidden(true)
    }
}

struct WatchedBadge: View {
    var body: some View {
        Image(systemName: "checkmark.circle.fill")
            .symbolRenderingMode(.palette)
            .foregroundStyle(.white, .tint)
            .font(.title3)
            .padding(6)
            .accessibilityHidden(true)
    }
}

/// A 2:3 poster with its title.
struct PosterCard: View {
    let title: String
    var caption: String?
    let image: String?
    var watched = false
    var fraction: Double?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            RemoteImage(path: image, placeholder: "film")
                .aspectRatio(2 / 3, contentMode: .fit)
                .overlay(alignment: .topTrailing) { if watched { WatchedBadge() } }
                .overlay(alignment: .bottom) { if let fraction { ProgressBar(fraction: fraction) } }
                .clipShape(.rect(cornerRadius: 8))
            Text(title).font(.footnote.weight(.medium)).lineLimit(1)
            if let caption {
                Text(caption).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .contentShape(.rect)
        // The TV's focus effect: a card that lifts and highlights as the focus engine reaches it.
        .hoverEffect()
        // One element to VoiceOver: the title, and in words what the badge and the bar show.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue(spoken)
    }

    /// What the picture's overlays say, in words.
    private var spoken: String {
        var parts: [String] = []
        if let caption, !caption.isEmpty { parts.append(caption) }
        if watched {
            parts.append("Watched")
        } else if fraction != nil {
            parts.append("In progress")
        }
        return parts.joined(separator: ", ")
    }
}

/// A 16:9 card for a file in progress: a movie or an episode.
struct LandscapeCard: View {
    let item: Item

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            RemoteImage(path: item.landscapeImage, placeholder: item.isEpisode ? "tv" : "film")
                .aspectRatio(16 / 9, contentMode: .fit)
                .overlay(alignment: .bottom) { if let f = item.fraction { ProgressBar(fraction: f) } }
                .clipShape(.rect(cornerRadius: 8))
            Text(item.isEpisode ? item.show ?? item.title : item.title)
                .font(.footnote.weight(.medium)).lineLimit(1)
            Text(item.isEpisode ? Format.facts(item.episodeCode, item.episodeTitle) : item.year.map(String.init) ?? " ")
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .contentShape(.rect)
        .hoverEffect()
        // One element to VoiceOver: the title, then the line under it and the watch state.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(item.isEpisode ? item.show ?? item.title : item.title)
        .accessibilityValue(spoken)
    }

    /// The card's second line, and whether it was started or watched, in words.
    private var spoken: String {
        var parts: [String] = []
        if item.isEpisode {
            parts.append(Format.facts(item.episodeCode, item.episodeTitle))
        } else if let year = item.year {
            parts.append(String(year))
        }
        if item.isWatched {
            parts.append("Watched")
        } else if item.fraction != nil {
            parts.append("In progress")
        }
        return parts.joined(separator: ", ")
    }
}

/// A titled row that scrolls sideways, with an optional count and trailing header action.
struct Shelf<Content: View, Trailing: View>: View {
    let title: String
    var subtitle: String?
    var count: Int?
    @ViewBuilder let trailing: Trailing
    @ViewBuilder let content: Content

    init(
        _ title: String,
        subtitle: String? = nil,
        count: Int? = nil,
        @ViewBuilder trailing: () -> Trailing,
        @ViewBuilder content: () -> Content
    ) {
        self.title = title
        self.subtitle = subtitle
        self.count = count
        self.trailing = trailing()
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(title).font(.title3.bold())
                        if let count {
                            Text("\(count)")
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(.secondary)
                                .monospacedDigit()
                        }
                    }
                    if let subtitle, !subtitle.isEmpty {
                        Text(subtitle).font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 8)
                trailing
            }
            .padding(.horizontal)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: shelfSpacing) { content }
                    .padding(.horizontal)
            }
        }
    }
}

extension Shelf where Trailing == EmptyView {
    init(_ title: String, subtitle: String? = nil, count: Int? = nil, @ViewBuilder content: () -> Content) {
        self.init(title, subtitle: subtitle, count: count, trailing: { EmptyView() }, content: content)
    }
}

/// Subtle pill link for external references (Wikipedia, IMDb, TMDB) on iOS.
struct ExternalLinkPill: View {
    let title: String
    let url: URL

    var body: some View {
        #if os(iOS)
        Link(destination: url) {
            HStack(spacing: 4) {
                Text(title)
                Image(systemName: "arrow.up.right")
                    .font(.caption2.weight(.semibold))
            }
            .font(.caption.weight(.medium))
        }
        .buttonStyle(.glass)
        .buttonBorderShape(.capsule)
        #endif
    }
}

/// The library's state over a page that shows it: loading, its error, or empty.
struct LibraryStatus: View {
    let library: LibraryModel
    let isEmpty: Bool

    var body: some View {
        if !library.loaded {
            ProgressView()
        } else if isEmpty, let error = library.error {
            ContentUnavailableView {
                Label("Cannot load the library", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try again") { Task { await library.reload() } }
            }
        } else if isEmpty {
            ContentUnavailableView("Nothing here yet", systemImage: "film.stack",
                                   description: Text("Add folders to the library on ply's web page."))
        }
    }
}

/// A wide picture at the top of a page, fading into it.
struct Backdrop: View {
    let path: String?

    var body: some View {
        RemoteImage(path: path, placeholder: "photo")
            .aspectRatio(16 / 9, contentMode: .fit)
            .overlay(alignment: .bottom) {
                LinearGradient(colors: [.clear, Self.pageBackground], startPoint: .center, endPoint: .bottom)
            }
    }

    /// The colour a page fades into. tvOS has no `systemBackground`; its pages are black.
    private static var pageBackground: Color {
        #if os(tvOS)
        .black
        #else
        Color(.systemBackground)
        #endif
    }
}

/// Reusable inline error banner with a "Try again" action.
struct InlineErrorBanner: View {
    let title: String
    let message: String
    let retry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.subheadline.weight(.semibold))
            Text(message)
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("Try again", action: retry)
                .font(.caption)
        }
        .padding(.horizontal)
    }
}

/// Says the episodes could not be loaded, with a way to ask again: the Browse search reads them, and
/// so does a show's newest file (the Shows "Recently updated" sort).
struct EpisodesFailure: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        if let error = connection.library.episodesError {
            InlineErrorBanner(
                title: "The episodes could not be loaded",
                message: error,
                retry: { Task { await connection.library.ensureEpisodes() } }
            )
        }
    }
}

// MARK: - Glass Surfaces, Play & Close Controls

extension View {
    @ViewBuilder func plyGlassCircle(tintOpacity: Double = 0.18) -> some View {
        #if os(iOS)
        glassEffect(
            tintOpacity > 0
                ? .regular.tint(Color.black.opacity(tintOpacity)).interactive()
                : .regular.interactive(),
            in: .circle
        )
        #else
        self
            .background(.ultraThinMaterial, in: Circle())
            .background(Color.black.opacity(tintOpacity), in: Circle())
            .overlay(Circle().strokeBorder(.white.opacity(0.24), lineWidth: 0.85))
            .shadow(color: .black.opacity(0.35), radius: 10, y: 4)
        #endif
    }

    @ViewBuilder func plyGlassCapsule(
        tintOpacity: Double = 0,
        strokeOpacity: Double = 0.14,
        interactive: Bool = false
    ) -> some View {
        #if os(iOS)
        let base: Glass = tintOpacity > 0 ? .regular.tint(Color.black.opacity(tintOpacity)) : .regular
        glassEffect(interactive ? base.interactive() : base, in: .capsule)
        #else
        self
            .background(.ultraThinMaterial, in: Capsule())
            .background(Color.black.opacity(tintOpacity), in: Capsule())
            .overlay(Capsule().strokeBorder(Color.white.opacity(strokeOpacity), lineWidth: 0.75))
        #endif
    }

    @ViewBuilder func plyInteractiveGlassCapsule(tint: Color? = nil) -> some View {
        #if os(iOS)
        glassEffect(
            tint.map { .regular.tint($0).interactive() } ?? .regular.interactive(),
            in: .capsule
        )
        #else
        plySubtleCapsule()
        #endif
    }

    @ViewBuilder func plyGlassButtonStyle(prominent: Bool = false) -> some View {
        #if os(iOS)
        if prominent {
            buttonStyle(.glassProminent)
        } else {
            buttonStyle(.glass)
        }
        #else
        if prominent {
            buttonStyle(.borderedProminent)
        } else {
            buttonStyle(.bordered)
        }
        #endif
    }

    @ViewBuilder func plySubtleCapsule(
        fillOpacity: Double = 0.08,
        strokeOpacity: Double = 0.12
    ) -> some View {
        self
            .background(Color.white.opacity(fillOpacity), in: Capsule())
            .overlay(Capsule().strokeBorder(Color.white.opacity(strokeOpacity), lineWidth: 0.75))
    }

    @ViewBuilder func plyMicroSpecsStyle(opacity: Double = 0.68) -> some View {
        self
            .font(.system(size: 11, weight: .semibold))
            .tracking(1.6)
            .monospacedDigit()
            .foregroundStyle(.white.opacity(opacity))
    }

    @ViewBuilder func plyHeroTitleStyle(size: CGFloat = 30) -> some View {
        self
            .font(.system(size: size, weight: .semibold))
            .tracking(-0.5)
            .foregroundStyle(.white)
            .lineLimit(2)
    }
}

/// Standard frosted-glass circular play button with the amber-orange mark centered on hero art and cards.
struct HeroPlayButton: View {
    var accessibilityLabel: String = "Play"
    var progress: Double? = nil
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            ZStack {
                Image(systemName: "play.fill")
                    .font(.system(size: PlyDesign.heroPlayGlyphSize, weight: .bold))
                    .foregroundStyle(LinearGradient.plyAmber)
                    .shadow(color: Color.black.opacity(0.28), radius: 3, y: 1)
                    .offset(x: 2)
                    .frame(width: PlyDesign.heroPlaySize, height: PlyDesign.heroPlaySize)
                    .plyGlassCircle(tintOpacity: 0.20)

                if let progress, progress > 0 {
                    Circle()
                        .trim(from: 0, to: min(1, progress))
                        .stroke(
                            Color.plyAmberHighlight,
                            style: StrokeStyle(lineWidth: 2.5, lineCap: .round)
                        )
                        .rotationEffect(.degrees(-90))
                        .frame(width: PlyDesign.heroPlaySize, height: PlyDesign.heroPlaySize)
                }
            }
            .frame(width: PlyDesign.heroPlayTouchSize, height: PlyDesign.heroPlayTouchSize)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .contentShape(Circle())
        .accessibilityLabel(accessibilityLabel)
    }
}

/// Standard-sized 44pt-tall glass overlay icon glyph shared by single circular controls and grouped pills.
struct GlassOverlayIconGlyph: View {
    let systemImage: String
    var color: Color = .white
    var width: CGFloat = PlyDesign.iconButtonSize

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(size: PlyDesign.iconButtonGlyphSize, weight: .semibold))
            .foregroundStyle(color)
            .frame(width: width, height: PlyDesign.iconButtonSize)
            .contentShape(Rectangle())
    }
}

/// Standard-sized 44×44 glass circular icon label for top-right overlay controls.
struct GlassCircleIconLabel: View {
    let systemImage: String
    var color: Color = .white

    var body: some View {
        GlassOverlayIconGlyph(systemImage: systemImage, color: color)
            .plyGlassCircle(tintOpacity: 0.16)
            .contentShape(Circle())
    }
}

/// Standard-sized 44×44 interactive circular glass icon button placed at top-right of overlays.
struct GlassIconButton: View {
    let systemImage: String
    let accessibilityLabel: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            GlassCircleIconLabel(systemImage: systemImage)
        }
        .buttonStyle(.plain)
        .contentShape(Circle())
        .accessibilityLabel(accessibilityLabel)
    }
}

/// Standard-sized 44×44 glass dismiss button with a close (`xmark`) icon.
struct GlassCloseButton: View {
    var accessibilityLabel: String = "Close"
    var action: () -> Void

    var body: some View {
        GlassIconButton(systemImage: "xmark", accessibilityLabel: accessibilityLabel, action: action)
    }
}

/// Preference key reporting `HeroBackdropArtwork`'s global `minY` for layout verification.
nonisolated struct HeroBackdropMinYKey: PreferenceKey {
    static let defaultValue: CGFloat? = nil
    static func reduce(value: inout CGFloat?, nextValue: () -> CGFloat?) {
        value = nextValue() ?? value
    }
}

/// Full-bleed hero artwork with a dark top degrade and a smooth alpha fade into the ambient backdrop.
struct HeroBackdropArtwork: View {
    let path: String?
    let height: CGFloat
    let coordinateSpaceName: String

    /// Soft top vignette so status bar items, back button, and AirPlay/close controls stay readable over bright art.
    nonisolated static let topVignetteStops: [Gradient.Stop] = [
        .init(color: Color.black.opacity(0.66), location: 0.0),
        .init(color: Color.black.opacity(0.34), location: 0.16),
        .init(color: Color.black.opacity(0.08), location: 0.30),
        .init(color: Color.clear, location: 0.42)
    ]

    /// Subtle lower-hero darkening applied inside the alpha mask so white titles stay crisp without leaving a dark seam.
    nonisolated static let lowerLegibilityStops: [Gradient.Stop] = [
        .init(color: Color.clear, location: 0.44),
        .init(color: Color.black.opacity(0.22), location: 0.66),
        .init(color: Color.black.opacity(0.42), location: 1.0)
    ]

    /// Smooth eased alpha mask so the artwork dissolves seamlessly into the `LivingMeshBackdrop` below it.
    nonisolated static let bottomAlphaMaskStops: [Gradient.Stop] = [
        .init(color: Color.white, location: 0.0),
        .init(color: Color.white, location: 0.50),
        .init(color: Color.white.opacity(0.90), location: 0.62),
        .init(color: Color.white.opacity(0.66), location: 0.74),
        .init(color: Color.white.opacity(0.34), location: 0.85),
        .init(color: Color.white.opacity(0.10), location: 0.94),
        .init(color: Color.clear, location: 1.0)
    ]

    var body: some View {
        GeometryReader { geo in
            let minY = geo.frame(in: .named(coordinateSpaceName)).minY
            let extraTop = max(0, minY)

            ZStack {
                RemoteImage(path: path, placeholder: "photo")
                    .aspectRatio(contentMode: .fill)
                    .frame(width: geo.size.width, height: height + extraTop)
                    .clipped()
                    .brightness(-0.08)
                    .overlay(Color.black.opacity(0.20))

                LinearGradient(stops: Self.topVignetteStops, startPoint: .top, endPoint: .bottom)

                LinearGradient(stops: Self.lowerLegibilityStops, startPoint: .top, endPoint: .bottom)
            }
            .mask(
                LinearGradient(stops: Self.bottomAlphaMaskStops, startPoint: .top, endPoint: .bottom)
            )
            .frame(width: geo.size.width, height: height + extraTop)
            .offset(y: -extraTop)
            .preference(key: HeroBackdropMinYKey.self, value: geo.frame(in: .global).minY - extraTop)
        }
        .frame(height: height)
        .frame(maxWidth: .infinity)
        .allowsHitTesting(false)
    }
}

/// Frosted glass pill badge for deck telemetry (codec/resolution specs, ratings).
struct PlyGlassPillBadge: View {
    let text: String
    var systemImage: String? = nil
    var iconColor: Color = .plyAmberHighlight
    var textOpacity: Double = 0.86

    var body: some View {
        HStack(spacing: 4) {
            if let systemImage {
                Image(systemName: systemImage)
                    .font(.system(size: 10))
                    .foregroundStyle(iconColor)
            }
            Text(text)
                .font(systemImage == nil ? .caption.weight(.medium) : .caption.weight(.semibold))
                .foregroundStyle(.white.opacity(textOpacity))
        }
        .padding(.horizontal, PlyDesign.capsuleHorizontalPadding)
        .padding(.vertical, PlyDesign.capsuleVerticalPadding)
        .plyGlassCapsule()
    }
}

/// Reusable Liquid Glass detail action button (`Trailer`, `Mark as watched`, `From start`, etc.).
struct DetailActionButton: View {
    let title: String
    let systemImage: String
    var prominent: Bool = false
    var tint: Color? = nil
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Label(title, systemImage: systemImage)
                .font(.subheadline.weight(.semibold))
        }
        .plyGlassButtonStyle(prominent: prominent)
        .buttonBorderShape(.capsule)
        .tint(tint)
    }
}

/// Standard metadata row chrome shared by interactive pickers and static value rows on detail screens.
struct DetailRowContent: View {
    let systemImage: String
    let title: String
    let value: String
    var showsPickerIndicator: Bool = false

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(Color.plyAccent)
                .frame(width: 22)

            Text(title)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.primary)

            Spacer()

            Text(value)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .lineLimit(1)

            if showsPickerIndicator {
                Image(systemName: "chevron.up.chevron.down")
                    .font(.caption2.bold())
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, 10)
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue(value)
    }
}

/// Shared circular cast/crew credit card.
struct PersonCreditCard: View {
    let name: String
    let subtitle: String?
    let profile: String?

    var body: some View {
        VStack(spacing: 4) {
            RemoteImage(path: profile, placeholder: "person.fill")
                .frame(width: 80, height: 80)
                .clipShape(.circle)
            Text(name).font(.caption.weight(.medium)).lineLimit(1)
            if let subtitle, !subtitle.isEmpty {
                Text(subtitle).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .frame(width: 90)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(name)
        .accessibilityValue(subtitle ?? "")
    }
}

/// Shared Cast and Crew horizontal shelves for film, series, and episode detail views.
struct DetailCreditShelves: View {
    let cast: [CastMember]?
    let crew: [CrewMember]?

    var body: some View {
        if let cast, !cast.isEmpty {
            Shelf("Cast") {
                ForEach(cast.prefix(20), id: \.self) { member in
                    NavigationLink(value: Route.person(member.personId)) {
                        PersonCreditCard(name: member.name, subtitle: member.character, profile: member.profile)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        if let crew, !crew.isEmpty {
            Shelf("Crew") {
                ForEach(crew.prefix(20), id: \.self) { member in
                    NavigationLink(value: Route.person(member.personId)) {
                        PersonCreditCard(name: member.name, subtitle: member.job, profile: member.profile)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }
}

/// Shared edge-to-edge ambient backdrop and scrolling container for film, series, and episode detail views.
struct DetailBackdropScaffold<Content: View>: View {
    let backdropPath: String?
    var coordinateSpaceName: String = PlyDesign.detailCoordinateSpace
    var bottomClearance: CGFloat = PlyDesign.defaultDetailBottomClearance
    @ViewBuilder let content: () -> Content

    var body: some View {
        ZStack {
            AmbientBackdropWash(path: backdropPath)

            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    content()
                }
            }
            .contentMargins(.bottom, bottomClearance, for: .scrollContent)
            #if os(iOS)
            .ignoresSafeArea(edges: .top)
            .scrollEdgeEffectHidden(true, for: .top)
            .toolbarBackgroundVisibility(.hidden, for: .navigationBar)
            #endif
        }
        .coordinateSpace(name: coordinateSpaceName)
        #if os(iOS)
        .ignoresSafeArea(edges: .top)
        .navigationTitle("")
        .toolbarBackgroundVisibility(.hidden, for: .navigationBar)
        #endif
        .plyInlineTitle()
    }
}

/// Shared hero artwork, title/specs, and centered glass play control used across film, series, and episode details.
struct DetailHeroStage<Eyebrow: View>: View {
    let backdropPath: String?
    let title: String
    let facts: String
    var coordinateSpaceName: String = PlyDesign.detailCoordinateSpace
    var playAccessibilityLabel: String? = "Play"
    var onPlay: (() -> Void)? = nil
    var portalNamespace: Namespace.ID? = nil
    var matchedItemId: Int64? = nil
    @ViewBuilder let eyebrow: () -> Eyebrow

    @State private var playImpactTrigger = 0

    private var activeNamespace: Namespace.ID? {
        matchedItemId == nil ? nil : portalNamespace
    }

    private func matchedId(_ prefix: String) -> String {
        matchedItemId.map { "\(prefix)-\($0)" } ?? prefix
    }

    var body: some View {
        ZStack(alignment: .bottomLeading) {
            HeroBackdropArtwork(
                path: backdropPath,
                height: PlyDesign.heroHeight,
                coordinateSpaceName: coordinateSpaceName
            )
            .spatialMatchedGeometry(id: matchedId("spatial-art"), in: activeNamespace)

            VStack(alignment: .leading, spacing: 6) {
                eyebrow()

                Text(title)
                    .plyHeroTitleStyle()
                    .spatialMatchedGeometry(id: matchedId("spatial-title"), in: activeNamespace)

                if !facts.isEmpty {
                    Text(facts.uppercased())
                        .plyMicroSpecsStyle(opacity: 0.75)
                        .lineLimit(1)
                }
            }
            .padding(.horizontal, PlyDesign.contentHorizontalPadding)
            .padding(.bottom, 16)

            if let onPlay, let playAccessibilityLabel {
                HeroPlayButton(accessibilityLabel: playAccessibilityLabel) {
                    playImpactTrigger += 1
                    onPlay()
                }
                .spatialMatchedGeometry(id: matchedId("spatial-play"), in: activeNamespace)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
                .zIndex(10)
            }
        }
        .frame(height: PlyDesign.heroHeight)
        .frame(maxWidth: .infinity)
        #if os(iOS)
        .sensoryFeedback(.impact(weight: .medium, intensity: 0.85), trigger: playImpactTrigger)
        #endif
    }
}

extension DetailHeroStage where Eyebrow == EmptyView {
    init(
        backdropPath: String?,
        title: String,
        facts: String,
        coordinateSpaceName: String = PlyDesign.detailCoordinateSpace,
        playAccessibilityLabel: String? = "Play",
        onPlay: (() -> Void)? = nil,
        portalNamespace: Namespace.ID? = nil,
        matchedItemId: Int64? = nil
    ) {
        self.init(
            backdropPath: backdropPath,
            title: title,
            facts: facts,
            coordinateSpaceName: coordinateSpaceName,
            playAccessibilityLabel: playAccessibilityLabel,
            onPlay: onPlay,
            portalNamespace: portalNamespace,
            matchedItemId: matchedItemId,
            eyebrow: { EmptyView() }
        )
    }
}

/// Standard spacing and chrome metrics shared across the ply iOS and tvOS interfaces.
nonisolated enum PlyDesign {
    static let detailCoordinateSpace = "PlyDetailRoot"
    /// Standard height for the top-left `PlyLogo` in every main page navigation bar on iOS.
    static let headerLogoSize: CGFloat = 28.6
    /// Apple HIG standard touch target for top-bar circular icon buttons (`GlassCloseButton`, `GlassIconButton`).
    static let iconButtonSize: CGFloat = 44
    static let iconButtonGlyphSize: CGFloat = 17
    /// Unified size of all hero glass play buttons across Home, Film, Episode, and Series detail screens.
    static let heroPlaySize: CGFloat = 68
    static let heroPlayTouchSize: CGFloat = 76
    static let heroPlayGlyphSize: CGFloat = 26
    /// Unified height of detail hero stages on iOS (`460pt`) and tvOS (`520pt`).
    static let heroHeight: CGFloat = {
        #if os(tvOS)
        520
        #else
        460
        #endif
    }()
    static let overlayTopOffset: CGFloat = 8
    static let overlayTrailingPadding: CGFloat = 16
    static let overlayButtonSpacing: CGFloat = 12
    /// Trailing clearance in `AVPlayerViewController` so our top-right track pill clears AVKit's 44pt Mute circle with `overlayButtonSpacing`.
    static let playerTrackTrailingOffset: CGFloat = overlayTrailingPadding + iconButtonSize + overlayButtonSpacing
    static let capsuleHorizontalPadding: CGFloat = 12
    static let capsuleVerticalPadding: CGFloat = 6
    static let filterPillHeight: CGFloat = {
        #if os(tvOS)
        40
        #else
        32
        #endif
    }()
    static let contentHorizontalPadding: CGFloat = 16
    static let deckMetadataSpacing: CGFloat = 12
    static let deckCardsVerticalPadding: CGFloat = 16
    static let panelCornerRadius: CGFloat = 14
    static let heroCardCornerRadius: CGFloat = 32
    /// Standard scroll-content bottom breathing room (native `TabView` manages tab bar safe-area insets on iOS 26).
    static let standardBottomClearance: CGFloat = 28
    static let defaultDetailBottomClearance: CGFloat = standardBottomClearance
}

extension View {
    /// Places `PlyLogo` in the exact same top-left toolbar slot across all main tabs on iOS,
    /// without a pill or full-width header background so content scrolls cleanly underneath.
    @ViewBuilder func plyMainPageLogo(color: Color = .white, hidden: Bool = false) -> some View {
        #if os(iOS)
        self
            .navigationTitle("")
            .plyInlineTitle()
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    PlyLogo(size: PlyDesign.headerLogoSize, color: color)
                        .shadow(color: .black.opacity(0.35), radius: 4, y: 1)
                }
                .sharedBackgroundVisibility(.hidden)
            }
            .toolbarBackgroundVisibility(.hidden, for: .navigationBar)
            .toolbar(hidden ? .hidden : .visible, for: .navigationBar)
        #else
        self
        #endif
    }

    /// Places the standard top-right `GlassCloseButton` inside a modal or sheet navigation bar.
    /// Using `.topBarTrailing` keeps close placement consistent with portal overlays and hides the
    /// extra UIKit system pill background on iOS 26 so there is never a nested double circle.
    @ViewBuilder func plyCloseToolbar(
        accessibilityLabel: String = "Close",
        action: @escaping () -> Void
    ) -> some View {
        #if os(iOS)
        toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                GlassCloseButton(accessibilityLabel: accessibilityLabel, action: action)
            }
            .sharedBackgroundVisibility(.hidden)
        }
        #else
        self
        #endif
    }

    /// Applies the standard bottom breathing clearance and disables the iOS 26 top scroll-edge
    /// header fade when floating chrome sits over scrolling content.
    @ViewBuilder func plyTabBarBottomClearance(transparentTopEdge: Bool = false) -> some View {
        #if os(iOS)
        if transparentTopEdge {
            contentMargins(.bottom, PlyDesign.standardBottomClearance, for: .scrollContent)
                .scrollEdgeEffectHidden(true, for: .top)
        } else {
            contentMargins(.bottom, PlyDesign.standardBottomClearance, for: .scrollContent)
        }
        #else
        contentMargins(.bottom, PlyDesign.standardBottomClearance, for: .scrollContent)
        #endif
    }

    /// Unified height, typography, and Liquid Glass capsule styling shared across every sort,
    /// active filter, and reset control in library filter bars.
    @ViewBuilder func plyControlPill(highlighted: Bool = false, iconOnly: Bool = false) -> some View {
        self
            .font(.footnote.weight(.medium))
            .foregroundStyle(highlighted ? .white : .primary)
            .padding(.horizontal, iconOnly ? 0 : PlyDesign.capsuleHorizontalPadding)
            .frame(minWidth: PlyDesign.filterPillHeight, minHeight: PlyDesign.filterPillHeight, maxHeight: PlyDesign.filterPillHeight)
            #if os(iOS)
            .plyInteractiveGlassCapsule(tint: highlighted ? .plyAccent : nil)
            #else
            .background(
                highlighted ? Color.plyAccent.opacity(0.32) : Color.white.opacity(0.12),
                in: .capsule
            )
            .overlay(Capsule().strokeBorder(Color.white.opacity(0.16), lineWidth: 0.75))
            #endif
    }
}
