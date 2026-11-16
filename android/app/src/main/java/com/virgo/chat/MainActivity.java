package com.virgo.chat;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.BitmapFactory;
import android.media.AudioAttributes;
import android.media.MediaScannerConnection;
import android.media.Ringtone;
import android.media.RingtoneManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.os.PowerManager;
import android.os.VibrationEffect;
import android.os.Vibrator;
import android.util.Base64;
import android.util.Log;
import android.webkit.JavascriptInterface;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.widget.Toast;
import java.io.File;
import java.io.FileOutputStream;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    private static final String TAG = "VirgoNative";
    private static final String CHANNEL_ID = "virgo_chat_messages";
    private static final int PERMISSION_REQ_CODE = 101;
    public static volatile boolean isAppInForeground = false;
    public static volatile String currentOpenChat = "";

    @Override
    public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        createNotificationChannel();
        setupWebViewAndBridge();
        requestNotificationPermission();
        startVirgoBackgroundService();
        syncUserIdFromWebView();
    }

    @Override
    public void onStart() {
        super.onStart();
        setupWebViewAndBridge();
        startVirgoBackgroundService();
        syncUserIdFromWebView();
    }

    @Override
    public void onResume() {
        super.onResume();
        isAppInForeground = true;
        VirgoForegroundService.isActivityInForeground = true;
        setupWebViewAndBridge();
        requestNotificationPermission();
        startVirgoBackgroundService();
        syncUserIdFromWebView();
        try {
            if (this.bridge != null && this.bridge.getWebView() != null) {
                this.bridge.getWebView().resumeTimers();
                this.bridge.getWebView().evaluateJavascript("if (window.onVirgoResume) window.onVirgoResume();", null);
            }
        } catch (Exception ignored) {}
    }

    @Override
    public void onPause() {
        super.onPause();
        isAppInForeground = false;
        VirgoForegroundService.isActivityInForeground = false;
        try {
            if (this.bridge != null && this.bridge.getWebView() != null) {
                this.bridge.getWebView().resumeTimers();
                this.bridge.getWebView().evaluateJavascript("if (window.onVirgoPause) window.onVirgoPause();", null);
            }
        } catch (Exception ignored) {}
    }

    private void syncUserIdFromWebView() {
        runOnUiThread(() -> {
            try {
                if (this.bridge != null && this.bridge.getWebView() != null) {
                    this.bridge.getWebView().evaluateJavascript(
                        "(function() { try { return localStorage.getItem('virgo_auth_user') || window.myId || ''; } catch(e){ return ''; } })();",
                        value -> {
                            if (value != null && !value.equals("null") && !value.equals("\"\"")) {
                                String clean = value.replace("\"", "").trim().toLowerCase();
                                if (!clean.isEmpty()) {
                                    getSharedPreferences("virgo_prefs", Context.MODE_PRIVATE)
                                        .edit()
                                        .putString("active_user_id", clean)
                                        .apply();
                                    Log.d(TAG, "Successfully synced user ID from WebView: " + clean);
                                    startVirgoBackgroundService();
                                }
                            }
                        }
                    );
                }
            } catch (Exception ignored) {}
        });
    }

    private void startVirgoBackgroundService() {
        try {
            Intent serviceIntent = new Intent(this, VirgoForegroundService.class);
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(serviceIntent);
            } else {
                startService(serviceIntent);
            }
            Log.d(TAG, "VirgoForegroundService started");
        } catch (Exception e) {
            Log.e(TAG, "Failed starting VirgoForegroundService: " + e.getMessage());
        }
    }

    private void setupWebViewAndBridge() {
        runOnUiThread(() -> {
            try {
                if (this.bridge != null && this.bridge.getWebView() != null) {
                    WebView webView = this.bridge.getWebView();
                    WebSettings settings = webView.getSettings();
                    settings.setJavaScriptEnabled(true);
                    settings.setDomStorageEnabled(true);
                    settings.setDatabaseEnabled(true);
                    settings.setMediaPlaybackRequiresUserGesture(false);
                    settings.setJavaScriptCanOpenWindowsAutomatically(true);
                    settings.setSupportMultipleWindows(true);

                    String ua = settings.getUserAgentString();
                    if (ua != null && ua.contains("; wv")) {
                        settings.setUserAgentString(ua.replace("; wv", ""));
                    }

                    webView.addJavascriptInterface(new Object() {
                        @JavascriptInterface
                        public void showNotification(String title, String message, String msgId) {
                            Log.d(TAG, "showNotification requested from JS: " + title + " - " + message + " (id=" + msgId + ")");
                            NotificationHelper.postMessageNotification(MainActivity.this, msgId, title, message);
                        }

                        @JavascriptInterface
                        public void showNotification(String title, String message) {
                            showNotification(title, message, null);
                        }

                        @JavascriptInterface
                        public void requestPermission() {
                            runOnUiThread(() -> requestNotificationPermission());
                        }

                        @JavascriptInterface
                        public boolean areNotificationsEnabled() {
                            NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
                            if (nm == null) return false;
                            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                                return nm.areNotificationsEnabled();
                            }
                            return true;
                        }

                        @JavascriptInterface
                        public void setUserId(String userId) {
                            if (userId != null && !userId.trim().isEmpty()) {
                                getSharedPreferences("virgo_prefs", Context.MODE_PRIVATE)
                                    .edit()
                                    .putString("active_user_id", userId.trim().toLowerCase())
                                    .apply();
                                Log.d(TAG, "Saved active_user_id for background service: " + userId);
                                startVirgoBackgroundService();
                            }
                        }

                        @JavascriptInterface
                        public void setActiveChat(String peerId) {
                            currentOpenChat = peerId != null ? peerId.trim().toLowerCase() : "";
                            Log.d(TAG, "Current active chat updated: " + currentOpenChat);
                        }

                        @JavascriptInterface
                        public boolean isForeground() {
                            return isAppInForeground;
                        }

                        @JavascriptInterface
                        public void downloadAndInstallUpdate(String url) {
                            runOnUiThread(() -> {
                                try {
                                    Intent browserIntent = new Intent(Intent.ACTION_VIEW, Uri.parse(url));
                                    browserIntent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                                    startActivity(browserIntent);
                                } catch (Exception e) {
                                    Log.e(TAG, "Failed opening update url: " + e.getMessage());
                                }
                            });
                        }

                        @JavascriptInterface
                        public void vibrate(long ms) {
                            try {
                                Vibrator v = (Vibrator) getSystemService(Context.VIBRATOR_SERVICE);
                                if (v != null && v.hasVibrator()) {
                                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                                        v.vibrate(VibrationEffect.createOneShot(ms > 0 ? ms : 250, VibrationEffect.DEFAULT_AMPLITUDE));
                                    } else {
                                        v.vibrate(ms > 0 ? ms : 250);
                                    }
                                }
                            } catch (Exception ignored) {}
                        }

                        @JavascriptInterface
                        public void playChime() {
                            try {
                                Uri sound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                                Ringtone r = RingtoneManager.getRingtone(getApplicationContext(), sound);
                                if (r != null) r.play();
                            } catch (Exception ignored) {}
                        }

                        @JavascriptInterface
                        public void requestBatteryOptimization() {
                            runOnUiThread(() -> {
                                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                                    try {
                                        PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
                                        if (pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName())) {
                                            Intent intent = new Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
                                            intent.setData(Uri.parse("package:" + getPackageName()));
                                            startActivity(intent);
                                        }
                                    } catch (Exception ignored) {}
                                }
                            });
                        }

                        @JavascriptInterface
                        public void saveFileToDownloads(String fileName, String base64Data, String mimeType) {
                            runOnUiThread(() -> {
                                try {
                                    byte[] bytes = Base64.decode(base64Data, Base64.DEFAULT);
                                    File dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS);
                                    if (!dir.exists()) dir.mkdirs();
                                    String safeName = (fileName != null && !fileName.trim().isEmpty()) ? fileName.trim() : "virgo_file";
                                    File destFile = new File(dir, safeName);
                                    int count = 1;
                                    while (destFile.exists()) {
                                        String nameNoExt = safeName;
                                        String ext = "";
                                        int dotIdx = safeName.lastIndexOf('.');
                                        if (dotIdx > 0) {
                                            nameNoExt = safeName.substring(0, dotIdx);
                                            ext = safeName.substring(dotIdx);
                                        }
                                        destFile = new File(dir, nameNoExt + "_" + count + ext);
                                        count++;
                                    }
                                    try (FileOutputStream fos = new FileOutputStream(destFile)) {
                                        fos.write(bytes);
                                        fos.flush();
                                    }
                                    String finalMime = (mimeType != null && !mimeType.isEmpty()) ? mimeType : "application/octet-stream";
                                    MediaScannerConnection.scanFile(getApplicationContext(), new String[]{destFile.getAbsolutePath()}, new String[]{finalMime}, null);
                                    Toast.makeText(getApplicationContext(), "Saved to Downloads: " + destFile.getName(), Toast.LENGTH_LONG).show();
                                    Log.d(TAG, "File successfully saved to Downloads: " + destFile.getAbsolutePath());
                                } catch (Exception e) {
                                    Log.e(TAG, "Failed saving file to downloads: " + e.getMessage(), e);
                                    Toast.makeText(getApplicationContext(), "Failed saving file: " + e.getMessage(), Toast.LENGTH_SHORT).show();
                                }
                            });
                        }
                    }, "AndroidNative");
                    Log.d(TAG, "AndroidNative interface successfully attached to WebView");
                }
            } catch (Exception e) {
                Log.e(TAG, "Error in setupWebViewAndBridge: " + e.getMessage(), e);
            }
        });
    }

    private void requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(new String[]{android.Manifest.permission.POST_NOTIFICATIONS}, PERMISSION_REQ_CODE);
            }
        }
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions, int[] grantResults) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults);
        if (requestCode == PERMISSION_REQ_CODE) {
            if (grantResults.length > 0 && grantResults[0] == PackageManager.PERMISSION_GRANTED) {
                Log.d(TAG, "POST_NOTIFICATIONS permission granted by user");
                createNotificationChannel();
                startVirgoBackgroundService();
            }
        }
    }

    private void createNotificationChannel() {
        NotificationHelper.createNotificationChannel(this);
    }

    private void displayNativeNotification(String title, String message) {
        NotificationHelper.postMessageNotification(this, null, title, message);
    }
}
