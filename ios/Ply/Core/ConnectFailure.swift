import Foundation

/// Why connecting to a server at start failed: it did not answer, or it answered that its API
/// version and this app's do not overlap. The two are shown differently (one has "try again").
nonisolated enum ConnectFailure: Equatable, Sendable {
    case unreachable(String)
    case incompatible(String)

    static func classify(_ error: any Error) -> ConnectFailure {
        if let problem = error as? CompatibilityProblem {
            return .incompatible(problem.problem)
        }
        return .unreachable(error.localizedDescription)
    }
}
