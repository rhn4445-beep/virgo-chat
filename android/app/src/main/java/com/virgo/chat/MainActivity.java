package com.virgo.chat;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.graphics.BitmapFactory;
import android.media.AudioAttributes;
import android.media.Ringtone;
import android.media.RingtoneManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.PowerManager;
import android.os.VibrationEffect;
import android.os.Vibrator;
import android.util.Log;
import android.webkit.JavascriptInterface;
import android.webkit.WebSettings;
import android.webkit.WebView;
import androidx.core.app.NotificationCompat;
import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    private static final String TAG = "VirgoNative";
    private static final String CHANNEL_ID = "virgo_chat_messages";
    private static final int PERMISSION_REQ_CODE = 101;
    public static volatile boolean isAppInForeground = false;

    @Override
    public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        createNotificationChannel();
        setupWebViewAndBridge();
        requestNotificationPermission();
        startVirgoBackgroundService();
    }

    @Override
    public void onStart() {
        super.onStart();
        setupWebViewAndBridge();
        startVirgoBackgroundService();
    }

    @Override
    public void onResume() {
        super.onResume();
        isAppInForeground = true;
        setupWebViewAndBridge();
        requestNotificationPermission();
        startVirgoBackgroundService();
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
        try {
            if (this.bridge != null && this.bridge.getWebView() != null) {
                this.bridge.getWebView().resumeTimers();
                this.bridge.getWebView().evaluateJavascript("if (window.onVirgoPause) window.onVirgoPause();", null);
            }
        } catch (Exception ignored) {}
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

                    webView.addJavascriptInterface(new Object() {
                        @JavascriptInterface
                        public void showNotification(String title, String message) {
                            Log.d(TAG, "showNotification requested from JS: " + title + " - " + message);
                            displayNativeNotification(title, message);
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
            if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != android.content.pm.PackageManager.PERMISSION_GRANTED) {
                requestPermissions(new String[]{android.Manifest.permission.POST_NOTIFICATIONS}, PERMISSION_REQ_CODE);
            }
        }
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            try {
                CharSequence name = "Virgo Messages";
                String description = "High-priority notifications for incoming Virgo chat messages";
                int importance = NotificationManager.IMPORTANCE_HIGH;
                NotificationChannel channel = new NotificationChannel(CHANNEL_ID, name, importance);
                channel.setDescription(description);
                channel.enableVibration(true);
                channel.setVibrationPattern(new long[]{0, 250, 150, 250});
                channel.enableLights(true);
                channel.setLightColor(0xFFF5C542); // Golden celestial accent
                channel.setLockscreenVisibility(NotificationCompat.VISIBILITY_PUBLIC);
                channel.setBypassDnd(true);

                AudioAttributes audioAttributes = new AudioAttributes.Builder()
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .setUsage(AudioAttributes.USAGE_NOTIFICATION_COMMUNICATION_INSTANT)
                    .build();
                Uri defaultSound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                channel.setSound(defaultSound, audioAttributes);

                NotificationManager notificationManager = getSystemService(NotificationManager.class);
                if (notificationManager != null) {
                    notificationManager.createNotificationChannel(channel);
                }
            } catch (Exception e) {
                Log.e(TAG, "Error creating notification channel: " + e.getMessage(), e);
            }
        }
    }

    private void displayNativeNotification(String title, String message) {
        try {
            createNotificationChannel();

            Intent intent = new Intent(this, MainActivity.class);
            intent.setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
            PendingIntent pendingIntent = PendingIntent.getActivity(
                this, 
                0, 
                intent, 
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.M ? PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_UPDATE_CURRENT
            );

            // Play instant physical vibration on the device
            try {
                Vibrator vibrator = (Vibrator) getSystemService(Context.VIBRATOR_SERVICE);
                if (vibrator != null && vibrator.hasVibrator()) {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                        vibrator.vibrate(VibrationEffect.createWaveform(new long[]{0, 250, 150, 250}, -1));
                    } else {
                        vibrator.vibrate(new long[]{0, 250, 150, 250}, -1);
                    }
                }
            } catch (Exception ignored) {}

            // Play instant notification ringtone
            try {
                Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                Ringtone ringtone = RingtoneManager.getRingtone(getApplicationContext(), soundUri);
                if (ringtone != null) {
                    ringtone.play();
                }
            } catch (Exception ignored) {}

            Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);

            NotificationCompat.Builder builder = new NotificationCompat.Builder(this, CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_stat_virgo)
                .setContentTitle(title)
                .setContentText(message)
                .setStyle(new NotificationCompat.BigTextStyle().bigText(message))
                .setCategory(NotificationCompat.CATEGORY_MESSAGE)
                .setPriority(NotificationCompat.PRIORITY_MAX)
                .setDefaults(NotificationCompat.DEFAULT_ALL)
                .setSound(soundUri)
                .setVibrate(new long[]{0, 250, 150, 250})
                .setColor(0xFFF5C542)
                .setAutoCancel(true)
                .setContentIntent(pendingIntent);

            try {
                builder.setLargeIcon(BitmapFactory.decodeResource(getResources(), R.mipmap.ic_launcher));
            } catch (Throwable ignored) {}

            NotificationManager notificationManager = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
            if (notificationManager != null) {
                int notifId = (int) (System.currentTimeMillis() % 100000000);
                notificationManager.notify(notifId, builder.build());
                Log.d(TAG, "Notification successfully posted: id=" + notifId);
            }
        } catch (Throwable t) {
            Log.e(TAG, "Fatal error displaying notification: " + t.getMessage(), t);
        }
    }
}
