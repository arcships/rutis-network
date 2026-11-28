// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "RutisRCP",
    platforms: [.iOS(.v15), .macOS(.v12)],
    products: [.library(name: "RutisRCP", targets: ["RutisRCP"])],
    targets: [.target(name: "RutisRCP"), .testTarget(name: "RutisRCPTests", dependencies: ["RutisRCP"])]
)
