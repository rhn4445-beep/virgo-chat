import sys
import os
import subprocess
import atexit
from PyQt6.QtCore import QUrl, pyqtSlot, QObject
from PyQt6.QtWidgets import QApplication, QMainWindow
from PyQt6.QtWebEngineWidgets import QWebEngineView
from PyQt6.QtWebEngineCore import QWebEngineProfile, QWebEnginePage
from PyQt6.QtWebChannel import QWebChannel
from win11toast import toast

# Helper to locate files both during development and inside PyInstaller bundle
def resource_path(relative_path):
    if hasattr(sys, '_MEIPASS'):
        return os.path.join(sys._MEIPASS, relative_path)
    return os.path.join(os.path.abspath("."), relative_path)

gateway_process = None

def start_backend():
    global gateway_process
    exe_path = resource_path("gateway.exe")
    if os.path.exists(exe_path):
        # CREATE_NO_WINDOW (0x08000000) prevents a separate black console window from popping up
        gateway_process = subprocess.Popen(
            [exe_path],
            creationflags=0x08000000,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL
        )

def stop_backend():
    global gateway_process
    if gateway_process and gateway_process.poll() is None:
        gateway_process.terminate()
        try:
            gateway_process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            gateway_process.kill()

atexit.register(stop_backend)

class Bridge(QObject):
    def __init__(self, main_window):
        super().__init__()
        self.main_window = main_window

    @pyqtSlot(str, str, str)
    def notify(self, msg_id, sender, text):
        def handle_toast_click(args):
            self.main_window.showNormal()
            self.main_window.activateWindow()
            self.main_window.raise_()
            if sender:
                self.main_window.browser.page().runJavaScript(
                    f"if (typeof selectContact === 'function') selectContact('{sender}');"
                )

        _ = toast(
            title=f"Virgo: {sender}",
            body=text,
            on_click=handle_toast_click
        )

class VirgoDesktop(QMainWindow):
    def __init__(self):
        super().__init__()
        self.setWindowTitle("Virgo")
        self.resize(1100, 750)

        self.browser = QWebEngineView(self)
        self.setCentralWidget(self.browser)

        self.profile = QWebEngineProfile.defaultProfile()
        self.web_page = QWebEnginePage(self.profile, self)

        # Grant audio microphone recording permission automatically
        def handle_permission_requested(security_origin, feature):
            if feature == QWebEnginePage.PermissionType.MediaAudioCapture:
                self.web_page.setFeaturePermission(
                    security_origin,
                    feature,
                    QWebEnginePage.PermissionPolicy.PermissionGrantedByUser
                )

        self.web_page.featurePermissionRequested.connect(handle_permission_requested)
        self.browser.setPage(self.web_page)

        # WebChannel bridge for native Windows toasts & deep links
        self.channel = QWebChannel()
        self.bridge = Bridge(self)
        self.channel.registerObject("pyBridge", self.bridge)
        self.browser.page().setWebChannel(self.channel)

        # Load web UI via localhost gateway
        self.browser.setUrl(QUrl("http://127.0.0.1:8080/index.html"))

    def closeEvent(self, event):
        stop_backend()
        super().closeEvent(event)

if __name__ == "__main__":
    start_backend()
    app = QApplication(sys.argv)
    window = VirgoDesktop()
    window.show()
    sys.exit(app.exec())