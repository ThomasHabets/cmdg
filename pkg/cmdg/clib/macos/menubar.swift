import Cocoa

// Compilation: swiftc menubar.swift -o menubar
// Usage: ./menubar <cmdgPath>

class MenubarController: NSObject {
    private var statusItem: NSStatusItem
    private var cmdgPath: String
    private var unreadCount: Int = 0
    
    init(cmdgPath: String) {
        self.cmdgPath = cmdgPath
        self.statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        super.init()
        
        setupMenu()
        updateTitle()
        setupNotifications()
    }
    
    private func setupMenu() {
        let menu = NSMenu()
        
        let openItem = NSMenuItem(title: "Open cmdg", action: #selector(openCmdg), keyEquivalent: "o")
        openItem.target = self
        menu.addItem(openItem)
        
        let composeItem = NSMenuItem(title: "Compose Message", action: #selector(openCompose), keyEquivalent: "n")
        composeItem.target = self
        menu.addItem(composeItem)
        
        menu.addItem(NSMenuItem.separator())
        
        let refreshItem = NSMenuItem(title: "Check for Mail", action: #selector(refreshMail), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)
        
        menu.addItem(NSMenuItem.separator())
        
        let quitItem = NSMenuItem(title: "Quit Menubar Helper", action: #selector(terminate), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
        
        statusItem.menu = menu
    }
    
    private func updateTitle() {
        if let button = statusItem.button {
            // Using a system symbol or a custom string
            let icon = unreadCount > 0 ? "✉️ " : "📩 "
            button.title = icon + (unreadCount > 0 ? "\(unreadCount)" : "")
        }
    }
    
    private func setupNotifications() {
        // Listen for updates from the main cmdg Go process
        DistributedNotificationCenter.default().addObserver(
            self,
            selector: #selector(handleUpdateNotification(_:)),
            name: NSNotification.Name("com.floatpane.cmdg.UpdateUnread"),
            object: nil
        )
    }
    
    @objc private func handleUpdateNotification(_ notification: Notification) {
        if let userInfo = notification.userInfo,
           let countString = userInfo["count"] as? String,
           let count = Int(countString) {
            self.unreadCount = count
            updateTitle()
        }
    }
    
    @objc private func openCmdg() {
        runTerminalCommand(command: "'\(cmdgPath)'")
    }
    
    @objc private func openCompose() {
        runTerminalCommand(command: "'\(cmdgPath)' send")
    }
    
    @objc private func refreshMail() {
        // Trigger a notification that the Go app can listen for, or just run a command
        DistributedNotificationCenter.default().postNotificationName(
            NSNotification.Name("com.floatpane.cmdg.RefreshRequest"),
            object: nil,
            userInfo: nil,
            deliverImmediately: true
        )
    }
    
    private func runTerminalCommand(command: String) {
        let script = """
        tell application "Terminal"
            activate
            if (count of windows) is 0 then
                do script "\(command)"
            else
                do script "\(command)" in window 1
            end if
        end tell
        """
        
        if let appleScript = NSAppleScript(source: script) {
            var error: NSDictionary?
            appleScript.executeAndReturnError(&error)
        }
    }
    
    @objc private func terminate() {
        NSApplication.shared.terminate(nil)
    }
}

let args = ProcessInfo.processInfo.arguments
guard args.count > 1 else {
    print("Usage: ./menubar <cmdgPath>")
    exit(1)
}

let cmdgPath = args[1]

let app = NSApplication.shared
let controller = MenubarController(cmdgPath: cmdgPath)
app.setActivationPolicy(.prohibited) // Run as a background agent
app.run()
