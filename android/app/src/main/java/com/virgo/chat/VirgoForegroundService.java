package com.virgo.chat;

import android.app.AlarmManager;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.ServiceInfo;
import android.graphics.BitmapFactory;
import android.media.AudioAttributes;
import android.media.Ringtone;
import android.media.RingtoneManager;
import android.net.Uri;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import android.os.SystemClock;
import android.os.VibrationEffect;
import android.os.Vibrator;
import android.util.Log;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import org.json.JSONArray;
import org.json.JSONObject;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.net.URLEncoder;

public class VirgoForegroundService extends Service {
    private static final String TAG = "VirgoService";
    private static final String SERVICE_CHANNEL_ID = "virgo_background_channel";
    private static final String MSG_CHANNEL_ID = "virgo_chat_messages";
    private static final int SERVICE_NOTIFICATION_ID = 2001;

    public static volatile boolean isActivityInForeground = false;
    private static volatile boolean isPollingRunning = false;

    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private Thread pollingThread;

    @Override
    public void onCreate() {
        super.onCreate();
        Log.d(TAG, "VirgoForegroundService onCreate");
        createServiceNotificationChannel();
        createMessageNotificationChannel();
        startForegroundServiceWithNotification();
        acquireLocks();
        startPollingLoop();
    }

    private void createServiceNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            try {
                CharSequence name = "Virgo Background Service";
                String description = "Keeps Virgo connected for real-time messages and notifications";
                int importance = NotificationManager.IMPORTANCE_LOW;
                NotificationChannel channel = new NotificationChannel(SERVICE_CHANNEL_ID, name, importance);
                channel.setDescription(description);
                channel.setShowBadge(false);
                channel.setSound(null, null);

                NotificationManager manager = getSystemService(NotificationManager.class);
                if (manager != null) {
                    manager.createNotificationChannel(channel);
                }
            } catch (Exception e) {
                Log.e(TAG, "Error creating service channel: " + e.getMessage());
            }
        }
    }

    private void createMessageNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            try {
                CharSequence name = "Virgo Messages";
                String description = "High-priority notifications for incoming Virgo chat messages";
                int importance = NotificationManager.IMPORTANCE_HIGH;
                NotificationChannel channel = new NotificationChannel(MSG_CHANNEL_ID, name, importance);
                channel.setDescription(description);
                channel.enableVibration(true);
                channel.setVibrationPattern(new long[]{0, 250, 150, 250});
                channel.enableLights(true);
                channel.setLightColor(0xFFF5C542); // Golden accent
                channel.setLockscreenVisibility(NotificationCompat.VISIBILITY_PUBLIC);
                channel.setBypassDnd(true);

                AudioAttributes audioAttributes = new AudioAttributes.Builder()
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .setUsage(AudioAttributes.USAGE_NOTIFICATION_COMMUNICATION_INSTANT)
                    .build();
                Uri defaultSound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                channel.setSound(defaultSound, audioAttributes);

                NotificationManager manager = getSystemService(NotificationManager.class);
                if (manager != null) {
                    manager.createNotificationChannel(channel);
                }
            } catch (Exception e) {
                Log.e(TAG, "Error creating msg channel: " + e.getMessage());
            }
        }
    }

    private void startForegroundServiceWithNotification() {
        try {
            Intent notificationIntent = new Intent(this, MainActivity.class);
            notificationIntent.setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
            PendingIntent pendingIntent = PendingIntent.getActivity(
                this,
                0,
                notificationIntent,
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.M ? PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_UPDATE_CURRENT
            );

            NotificationCompat.Builder builder = new NotificationCompat.Builder(this, SERVICE_CHANNEL_ID)
                .setContentTitle("Virgo Messenger")
                .setContentText("Connected & listening for messages")
                .setSmallIcon(R.drawable.ic_stat_virgo)
                .setContentIntent(pendingIntent)
                .setPriority(NotificationCompat.PRIORITY_LOW)
                .setOngoing(true);

            Notification notification = builder.build();

            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(SERVICE_NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
            } else {
                startForeground(SERVICE_NOTIFICATION_ID, notification);
            }
            Log.d(TAG, "VirgoForegroundService running in foreground");
        } catch (Exception e) {
            Log.e(TAG, "Failed starting foreground service: " + e.getMessage(), e);
        }
    }

    private void acquireLocks() {
        try {
            PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
            if (pm != null && (wakeLock == null || !wakeLock.isHeld())) {
                wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "Virgo:ServiceWakeLock");
                wakeLock.acquire(24 * 60 * 60 * 1000L); // 24 hours
            }

            WifiManager wm = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
            if (wm != null && (wifiLock == null || !wifiLock.isHeld())) {
                wifiLock = wm.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "Virgo:WifiLock");
                wifiLock.acquire();
            }
        } catch (Exception e) {
            Log.e(TAG, "Error acquiring locks: " + e.getMessage());
        }
    }

    private synchronized void startPollingLoop() {
        if (isPollingRunning && pollingThread != null && pollingThread.isAlive()) {
            return;
        }

        isPollingRunning = true;
        pollingThread = new Thread(() -> {
            Log.d(TAG, "🚀 Virgo Native Background Poller thread started");
            while (isPollingRunning) {
                HttpURLConnection conn = null;
                try {
                    SharedPreferences prefs = getSharedPreferences("virgo_prefs", Context.MODE_PRIVATE);
                    String userId = prefs.getString("active_user_id", "");
                    if (userId == null || userId.trim().isEmpty()) {
                        Thread.sleep(1500);
                        continue;
                    }

                    acquireLocks();

                    String targetUrl = "https://virgo-chat.onrender.com/api/notifications/poll?userId=" + URLEncoder.encode(userId.trim(), "UTF-8");
                    URL url = new URL(targetUrl);
                    conn = (HttpURLConnection) url.openConnection();
                    conn.setRequestMethod("GET");
                    conn.setConnectTimeout(10000);
                    conn.setReadTimeout(30000); // 30s timeout matching 25s server hold
                    conn.setRequestProperty("User-Agent", "VirgoNativeClient/2.7.0");

                    int code = conn.getResponseCode();
                    if (code == 200) {
                        BufferedReader reader = new BufferedReader(new InputStreamReader(conn.getInputStream()));
                        StringBuilder sb = new StringBuilder();
                        String line;
                        while ((line = reader.readLine()) != null) {
                            sb.append(line);
                        }
                        reader.close();

                        handlePollPayload(sb.toString());
                    } else {
                        Thread.sleep(2000);
                    }
                } catch (Exception e) {
                    try {
                        Thread.sleep(2000);
                    } catch (Exception ignored) {}
                } finally {
                    if (conn != null) {
                        try {
                            conn.disconnect();
                        } catch (Exception ignored) {}
                    }
                }
            }
            Log.d(TAG, "Virgo Native Background Poller thread ended");
        }, "VirgoNotificationPoller");
        pollingThread.start();
    }

    private boolean isSameUserId(String id1, String id2) {
        if (id1 == null || id2 == null) return false;
        String s1 = id1.trim().toLowerCase();
        String s2 = id2.trim().toLowerCase();
        if (s1.equals(s2)) return true;
        String c1 = s1.replaceAll("[^0-9]", "");
        String c2 = s2.replaceAll("[^0-9]", "");
        if (c1.length() >= 10 && c2.length() >= 10) {
            return c1.substring(c1.length() - 10).equals(c2.substring(c2.length() - 10));
        }
        return false;
    }

    private void handlePollPayload(String jsonStr) {
        if (jsonStr == null || jsonStr.trim().isEmpty()) return;
        try {
            JSONObject root = new JSONObject(jsonStr);
            JSONArray msgs = root.optJSONArray("messages");
            if (msgs != null && msgs.length() > 0) {
                for (int i = 0; i < msgs.length(); i++) {
                    JSONObject m = msgs.getJSONObject(i);
                    String msgId = m.optString("id", "");
                    String from = m.optString("from", "Virgo Contact");
                    String text = m.optString("text", "New message");
                    String type = m.optString("type", "chat");

                    Log.d(TAG, "📩 Native Poller received message [" + msgId + "] from " + from + ": " + text + " (type=" + type + ")");

                    if ("call_request".equals(type) || (text != null && text.contains("Call..."))) {
                        // High-priority urgent incoming call
                        NotificationHelper.postCallNotification(this, msgId, from, text);
                    } else if ("call_ended".equals(type) || "call_rejected".equals(type)) {
                        NotificationHelper.cancelCallNotification(this);
                    } else {
                        // Post heads-up notification if app is in background OR user is viewing a different chat
                        boolean inForegroundWithSameUser = isActivityInForeground && MainActivity.currentOpenChat != null && isSameUserId(MainActivity.currentOpenChat, from);
                        if (!inForegroundWithSameUser) {
                            NotificationHelper.postMessageNotification(this, msgId, from, text);
                        }
                    }
                }
            }
        } catch (Exception e) {
            Log.e(TAG, "Error handling poll payload: " + e.getMessage());
        }
    }

    public void postMessageNotification(String sender, String message) {
        NotificationHelper.postMessageNotification(this, null, sender, message);
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        acquireLocks();
        startPollingLoop();
        return START_STICKY;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        Log.d(TAG, "Virgo onTaskRemoved - scheduling immediate restart");
        try {
            Intent restartServiceIntent = new Intent(getApplicationContext(), VirgoForegroundService.class);
            restartServiceIntent.setPackage(getPackageName());
            PendingIntent restartPendingIntent = PendingIntent.getService(
                getApplicationContext(),
                1,
                restartServiceIntent,
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.M ? PendingIntent.FLAG_ONE_SHOT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_ONE_SHOT
            );
            AlarmManager alarmService = (AlarmManager) getApplicationContext().getSystemService(Context.ALARM_SERVICE);
            if (alarmService != null) {
                alarmService.set(
                    AlarmManager.ELAPSED_REALTIME,
                    SystemClock.elapsedRealtime() + 1000,
                    restartPendingIntent
                );
            }
        } catch (Exception e) {
            Log.e(TAG, "Error in onTaskRemoved restart: " + e.getMessage());
        }
        super.onTaskRemoved(rootIntent);
    }

    @Override
    public void onDestroy() {
        isPollingRunning = false;
        try {
            if (pollingThread != null) {
                pollingThread.interrupt();
            }
            if (wakeLock != null && wakeLock.isHeld()) {
                wakeLock.release();
            }
            if (wifiLock != null && wifiLock.isHeld()) {
                wifiLock.release();
            }
        } catch (Exception ignored) {}
        Log.d(TAG, "VirgoForegroundService onDestroy");
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
