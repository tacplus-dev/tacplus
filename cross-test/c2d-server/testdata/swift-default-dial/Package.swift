// swift-tools-version: 6.1
// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// A consumer of the generated Swift package (../model): it dials the generated
// Go daemon with C2DClient's default socket (issue #494).
import PackageDescription

let package = Package(
    name: "C2DDefaultDial",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        .package(name: "C2dserverModel", path: "../model")
    ],
    targets: [
        .executableTarget(
            name: "C2DDefaultDial",
            dependencies: [
                .product(name: "C2dserverModel", package: "C2dserverModel")
            ],
            swiftSettings: [
                .swiftLanguageMode(.v6)
            ]
        )
    ]
)
