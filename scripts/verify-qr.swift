// Developer-only independent QR compatibility check using Apple's Vision decoder.
// Usage: swift scripts/verify-qr.swift /path/to/mono.png /path/to/mono.json
import Foundation
import Vision
import ImageIO

guard CommandLine.arguments.count == 3 else { fatalError("expected image and JSON paths") }
let expected = try JSONDecoder().decode([String].self, from: Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[2])))
let request = VNDetectBarcodesRequest()
request.symbologies = [.qr]
let handler = VNImageRequestHandler(url: URL(fileURLWithPath: CommandLine.arguments[1]), options: [:])
try handler.perform([request])
let actual = request.results?.compactMap { $0.payloadStringValue } ?? []
guard Set(actual) == Set(expected) else { fatalError("QR mismatch: decoded \(actual.count), expected \(expected.count)") }
print("Vision decoded both beacon and 512-byte data QR exactly")
