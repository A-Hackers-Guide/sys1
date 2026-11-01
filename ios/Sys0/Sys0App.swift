import SwiftUI
import UIKit
import Sys0mobile

// sys0 for iPhone and iPad. iOS suspends apps in the background, so this
// lends the device's spare storage to a sys1 pool only while the app is
// open and the device is charging, keeping the screen awake while it does.

@main
struct Sys0App: App {
    @StateObject private var model = Model()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            ContentView().environmentObject(model)
        }
        .onChange(of: phase) { p in model.foreground = (p == .active) }
    }
}

@MainActor
final class Model: ObservableObject {
    let dataDir: String
    @Published var joined = false
    @Published var lending = false
    @Published var status = ""
    @Published var error = ""
    @Published var enabled = UserDefaults.standard.object(forKey: "enabled") as? Bool ?? true {
        didSet { UserDefaults.standard.set(enabled, forKey: "enabled"); evaluate() }
    }
    var foreground = true { didSet { evaluate() } }

    init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let dir = base.appendingPathComponent("sys0", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        dataDir = dir.path
        UIDevice.current.isBatteryMonitoringEnabled = true
        NotificationCenter.default.addObserver(forName: UIDevice.batteryStateDidChangeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.evaluate() }
        }
        joined = Sys0mobileJoined(dataDir)
        evaluate()
        // For automated tests: `--sys1-join <code>` joins on launch. Only
        // developer tools can pass launch arguments to an app.
        let args = ProcessInfo.processInfo.arguments
        if let i = args.firstIndex(of: "--sys1-join"), i + 1 < args.count, !joined {
            join(code: args[i + 1], mine: false, storageGB: 1)
        }
    }

    var charging: Bool {
        let s = UIDevice.current.batteryState
        return s == .charging || s == .full
    }

    // evaluate lends only while joined, switched on, open and charging.
    func evaluate() {
        let want = joined && enabled && foreground && charging
        if want && !Sys0mobileRunning() {
            var err: NSError?
            if !Sys0mobileStart(dataDir, &err) { self.error = err?.localizedDescription ?? "Couldn't start" }
        } else if !want && Sys0mobileRunning() {
            DispatchQueue.global().async { Sys0mobileStop() }
        }
        lending = want
        UIApplication.shared.isIdleTimerDisabled = want // the app must stay open to lend
        status = Sys0mobileStatus(dataDir)
    }

    func join(code: String, mine: Bool, storageGB: Int) {
        error = ""
        let dir = dataDir, name = UIDevice.current.name
        DispatchQueue.global().async {
            var err: NSError?
            let ok = Sys0mobileJoin(dir, code.trimmingCharacters(in: .whitespacesAndNewlines), name, mine, storageGB, &err)
            let message = ok ? "" : (err?.localizedDescription ?? "Couldn't join")
            DispatchQueue.main.async {
                self.error = message
                self.joined = Sys0mobileJoined(dir)
                self.evaluate()
            }
        }
    }

    func leave() {
        var err: NSError?
        _ = Sys0mobileLeave(dataDir, &err)
        joined = false
        evaluate()
    }
}

struct ContentView: View {
    @EnvironmentObject var model: Model
    @State private var code = ""
    @State private var mine = false
    @State private var storageGB = 5

    var body: some View {
        NavigationView {
            Form {
                if model.joined {
                    Section {
                        Toggle("Lend spare storage", isOn: $model.enabled)
                        Text(model.lending ? "Lending now. Keep sys0 open; the screen stays on while charging." :
                                (model.charging ? "Paused." : "Plug in to lend. iOS only lets sys0 work while it's open."))
                            .font(.footnote).foregroundColor(.secondary)
                    }
                    Section("Status") {
                        Text(model.status).font(.system(.footnote, design: .monospaced))
                    }
                    Section {
                        Button("Leave the pool", role: .destructive) { model.leave() }
                    }
                } else {
                    Section {
                        Text("Lend this device's spare storage to a sys1 pool: encrypted pieces of backups and app data that you can't read, and nobody can without the pool's key. It works only while sys0 is open and the device is charging.")
                            .font(.footnote)
                    }
                    Section("Join code") {
                        TextField("sys1-join:…", text: $code)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                            .font(.system(.footnote, design: .monospaced))
                        Text("On your node's dashboard, open Add a machine and copy the join code.")
                            .font(.footnote).foregroundColor(.secondary)
                    }
                    Section("Whose device is this?") {
                        Picker("Owner", selection: $mine) {
                            Text("Shared with someone's pool").tag(false)
                            Text("Mine").tag(true)
                        }.pickerStyle(.segmented)
                        Stepper("Storage to lend: \(storageGB) GB", value: $storageGB, in: 1...200)
                    }
                    Section {
                        Button("Join the pool") { model.join(code: code, mine: mine, storageGB: storageGB) }
                            .disabled(code.isEmpty)
                        if !model.error.isEmpty {
                            Text(model.error).foregroundColor(.red).font(.footnote)
                        }
                    }
                }
            }
            .navigationTitle("sys0")
        }
        .navigationViewStyle(.stack)
    }
}
