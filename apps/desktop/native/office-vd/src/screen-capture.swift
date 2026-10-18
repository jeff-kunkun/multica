// screen-capture: live picture of the office virtual display for the desktop app (DENE-2261).
//
//   screen-capture --check-permission        {"type":"permission","granted":bool}; never prompts
//   screen-capture --request-permission      asks macOS once; only after Kun clicks "allow" in the app
//   screen-capture --decide                  reads a topology JSON on stdin, prints the decision (tests)
//   screen-capture [--home <vd home>] [--fps 2] [--width 1280] [--browser com.google.Chrome]
//                                            streams JSON lines on stdout until stdin closes:
//     {"type":"frame","seq":N,"width":W,"height":H,"jpeg":"<base64>"}
//     {"type":"status","state":"streaming"|"blocked","reason":R,"windows":N,"owned":N}
//     {"type":"heartbeat","seq":N}
//
// What it is allowed to show, re-checked every second:
//   - only the display vd-hold created (vendor 505, model 0x5644, serial 0x56443201, id from
//     <vd home>/state.json); yomado's virtual displays share vendor and model, so the serial decides,
//     never the main display, so Kun's own screen can't end up in the picture;
//   - only browser windows lying entirely on that display, and no more of them than the broker has
//     registered as opened by agents (<vd home>/control/owned-targets.json). Anything it can't
//     account for stops the picture ("blocked") instead of leaking into it.
// It only reads pixels: no input, no window moves, no activation, and no permission prompt unless
// --request-permission is passed.
import CoreGraphics
import CoreMedia
import Foundation
import ImageIO
import ScreenCaptureKit
import UniformTypeIdentifiers
import VideoToolbox

let officeVendor: UInt32 = 505
let officeModel: UInt32 = 0x5644
let officeSerial: UInt32 = 0x56443201

// MARK: decision (pure, testable through --decide)

struct WindowInfo: Codable {
    var bundle: String
    var layer: Int
    var x: Double, y: Double, width: Double, height: Double
    var rect: CGRect { CGRect(x: x, y: y, width: width, height: height) }
}

struct Topology: Codable {
    var displayFound: Bool
    var isMain: Bool
    var vendor: UInt32
    var model: UInt32
    var serial: UInt32
    var frame: WindowInfo  // bounds of the virtual display; bundle/layer unused
    var windows: [WindowInfo]
    var owned: Int
    var browser: String
}

struct Decision: Codable, Equatable {
    var ok: Bool
    var reason: String
    var windowIndexes: [Int]
}

func decide(_ t: Topology) -> Decision {
    func blocked(_ r: String) -> Decision { Decision(ok: false, reason: r, windowIndexes: []) }
    if !t.displayFound { return blocked("vd_missing") }
    if t.isMain { return blocked("vd_is_main") }
    if t.vendor != officeVendor || t.model != officeModel || t.serial != officeSerial { return blocked("not_office_display") }
    let vd = t.frame.rect
    var picked: [Int] = []
    for (i, w) in t.windows.enumerated() where w.bundle == t.browser && w.layer == 0 {
        let r = w.rect
        if r.width < 2 || r.height < 2 || !r.intersects(vd) { continue }
        if !vd.contains(r) { return blocked("window_straddles_display") }
        picked.append(i)
    }
    if picked.count > t.owned { return blocked("unowned_window") }
    return Decision(ok: true, reason: picked.isEmpty ? "no_windows" : "ok", windowIndexes: picked)
}

// MARK: output

let out = FileHandle.standardOutput
let outQueue = DispatchQueue(label: "out")
let frameLock = NSLock()
var pendingFrame: Data?
var drainScheduled = false

func emit(_ obj: [String: Any]) {
    guard var data = try? JSONSerialization.data(withJSONObject: obj) else { return }
    data.append(0x0a)
    outQueue.async { out.write(data) }
}

// Latest frame only: while the app is still reading one frame, newer frames replace each other
// in a single slot, so a slow reader sees a fresh picture instead of a growing backlog.
func emitFrame(_ obj: [String: Any]) {
    guard var data = try? JSONSerialization.data(withJSONObject: obj) else { return }
    data.append(0x0a)
    frameLock.lock()
    pendingFrame = data
    let schedule = !drainScheduled
    drainScheduled = true
    frameLock.unlock()
    guard schedule else { return }
    outQueue.async {
        frameLock.lock()
        let next = pendingFrame
        pendingFrame = nil
        drainScheduled = false
        frameLock.unlock()
        if let next = next { out.write(next) }
    }
}

// MARK: arguments

var args = Array(CommandLine.arguments.dropFirst())
func flag(_ name: String) -> Bool {
    if let i = args.firstIndex(of: name) { args.remove(at: i); return true }
    return false
}
func option(_ name: String) -> String? {
    guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
    let v = args[i + 1]; args.removeSubrange(i...(i + 1)); return v
}

if flag("--check-permission") {
    emit(["type": "permission", "granted": CGPreflightScreenCaptureAccess()])
    outQueue.sync {}
    exit(0)
}
if flag("--request-permission") {
    emit(["type": "permission", "granted": CGRequestScreenCaptureAccess()])
    outQueue.sync {}
    exit(0)
}
if flag("--decide") {
    let input = FileHandle.standardInput.readDataToEndOfFile()
    guard let t = try? JSONDecoder().decode(Topology.self, from: input),
          let d = try? JSONEncoder().encode(decide(t)) else {
        FileHandle.standardError.write("bad topology json\n".data(using: .utf8)!)
        exit(2)
    }
    out.write(d); out.write("\n".data(using: .utf8)!)
    exit(0)
}

let home = option("--home") ?? ProcessInfo.processInfo.environment["OFFICE_VD_HOME"]
    ?? (NSHomeDirectory() as NSString).appendingPathComponent(".agents/vd")
let fps = max(1, min(10, Int(option("--fps") ?? "") ?? 2))
let targetWidth = max(320, min(1920, Int(option("--width") ?? "") ?? 1280))
let browser = option("--browser") ?? "com.google.Chrome"

func readJSON(_ path: String) -> [String: Any]? {
    guard let d = FileManager.default.contents(atPath: path) else { return nil }
    return (try? JSONSerialization.jsonObject(with: d)) as? [String: Any]
}

func ownedCount() -> Int {
    let v = readJSON((home as NSString).appendingPathComponent("control/owned-targets.json"))
    return (v?["targets"] as? [Any])?.count ?? 0
}

// MARK: streaming

final class Capture: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    var current: [CGWindowID] = []
    var lastStatus = ""
    var seq = 0
    let frameQueue = DispatchQueue(label: "frames")

    func status(_ state: String, _ reason: String, windows: Int, owned: Int) {
        let key = "\(state):\(reason):\(windows):\(owned)"
        if key == lastStatus { return }
        lastStatus = key
        emit(["type": "status", "state": state, "reason": reason, "windows": windows, "owned": owned])
    }

    func stop() {
        stream?.stopCapture(completionHandler: nil)
        stream = nil
        current = []
    }

    // check runs once a second on the main queue.
    func check() {
        guard CGPreflightScreenCaptureAccess() else {
            stop(); status("blocked", "permission", windows: 0, owned: 0); return
        }
        guard let state = readJSON((home as NSString).appendingPathComponent("state.json")),
              let idNum = state["displayID"] as? NSNumber else {
            stop(); status("blocked", "vd_missing", windows: 0, owned: 0); return
        }
        let displayID = CGDirectDisplayID(idNum.uint32Value)
        SCShareableContent.getExcludingDesktopWindows(true, onScreenWindowsOnly: true) { content, error in
            DispatchQueue.main.async { self.apply(content: content, error: error, displayID: displayID) }
        }
    }

    func apply(content: SCShareableContent?, error: Error?, displayID: CGDirectDisplayID) {
        guard let content = content else {
            stop(); status("blocked", "content_unavailable", windows: 0, owned: 0); return
        }
        // A display ID that is no longer in the active list can answer with another display's values.
        var active = [CGDirectDisplayID](repeating: 0, count: 64)
        var count: UInt32 = 0
        let live = CGGetActiveDisplayList(64, &active, &count) == .success && active.prefix(Int(count)).contains(displayID)
        let display = live ? content.displays.first { $0.displayID == displayID } : nil
        let b = CGDisplayBounds(displayID)
        let windows = content.windows
        let owned = ownedCount()
        let topo = Topology(
            displayFound: display != nil,
            isMain: displayID == CGMainDisplayID(),
            vendor: CGDisplayVendorNumber(displayID),
            model: CGDisplayModelNumber(displayID),
            serial: CGDisplaySerialNumber(displayID),
            frame: WindowInfo(bundle: "", layer: 0, x: b.origin.x, y: b.origin.y, width: b.width, height: b.height),
            windows: windows.map {
                WindowInfo(bundle: $0.owningApplication?.bundleIdentifier ?? "", layer: $0.windowLayer,
                           x: $0.frame.origin.x, y: $0.frame.origin.y, width: $0.frame.width, height: $0.frame.height)
            },
            owned: owned, browser: browser)
        let d = decide(topo)
        guard d.ok, let display = display else {
            stop(); status("blocked", d.reason, windows: 0, owned: owned); return
        }
        let picked = d.windowIndexes.map { windows[$0] }
        let ids = picked.map { $0.windowID }
        if picked.isEmpty {
            stop(); status("streaming", "no_windows", windows: 0, owned: owned); return
        }
        let filter = SCContentFilter(display: display, including: picked)
        if let s = stream {
            if ids != current { s.updateContentFilter(filter, completionHandler: nil); current = ids }
        } else {
            let cfg = SCStreamConfiguration()
            let scale = Double(targetWidth) / Double(display.width)
            cfg.width = targetWidth
            cfg.height = Int((Double(display.height) * scale).rounded())
            cfg.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            cfg.queueDepth = 3
            cfg.showsCursor = false
            cfg.pixelFormat = kCVPixelFormatType_32BGRA
            let s = SCStream(filter: filter, configuration: cfg, delegate: self)
            do {
                try s.addStreamOutput(self, type: .screen, sampleHandlerQueue: frameQueue)
            } catch {
                status("blocked", "stream_failed", windows: ids.count, owned: owned); return
            }
            stream = s; current = ids
            s.startCapture { err in
                if err != nil { DispatchQueue.main.async { self.stop(); self.status("blocked", "stream_failed", windows: ids.count, owned: owned) } }
            }
        }
        status("streaming", "ok", windows: ids.count, owned: owned)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        DispatchQueue.main.async { if self.stream === stream { self.stop(); self.lastStatus = "" } }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sb: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, sb.isValid,
              let attachments = CMSampleBufferGetSampleAttachmentsArray(sb, createIfNecessary: false) as? [[SCStreamFrameInfo: Any]],
              let raw = attachments.first?[.status] as? Int, SCFrameStatus(rawValue: raw) == .complete,
              let pixels = CMSampleBufferGetImageBuffer(sb) else { return }
        var image: CGImage?
        VTCreateCGImageFromCVPixelBuffer(pixels, options: nil, imageOut: &image)
        guard let cg = image else { return }
        let data = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(data, UTType.jpeg.identifier as CFString, 1, nil) else { return }
        CGImageDestinationAddImage(dest, cg, [kCGImageDestinationLossyCompressionQuality: 0.6] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return }
        seq += 1
        emitFrame(["type": "frame", "seq": seq, "width": cg.width, "height": cg.height,
                   "jpeg": (data as Data).base64EncodedString()])
    }
}

// The app owns this process: when it goes away (stdin closes), stop at once.
Thread.detachNewThread {
    _ = FileHandle.standardInput.readDataToEndOfFile()
    exit(0)
}

let capture = Capture()
var beats = 0
let timer = DispatchSource.makeTimerSource(queue: .main)
timer.schedule(deadline: .now(), repeating: 1.0)
timer.setEventHandler {
    capture.check()
    beats += 1
    emit(["type": "heartbeat", "seq": beats])
}
timer.resume()
dispatchMain()
