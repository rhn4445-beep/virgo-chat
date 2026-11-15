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
import android.os.VibrationEffect;
import android.os.Vibrator;
import android.util.Log;
import androidx.core.app.NotificationCompat;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

public class NotificationHelper {
    private static final String TAG = "VirgoNotifHelper";
    public static final String MSG_CHANNEL_ID = "virgo_chat_messages";

    // Deduplication cache: keeps last 100 message IDs with timestamps
    private static final Map<String, Long> notifiedMessages = Collections.synchronizedMap(
        new LinkedHashMap<String, Long>(100, 0.75f, true) {
            @Override
            protected boolean removeEldestEntry(Map.Entry<String, Long> eldest) {
                return size() > 100;
            }
        }
    );

    public static synchronized boolean shouldNotifyMessage(String msgId) {
        if (msgId == null || msgId.trim().isEmpty()) {
            return true;
        }
        long now = System.currentTimeMillis();
        Long lastTime = notifiedMessages.get(msgId);
        if (lastTime != null && (now - lastTime) < 30000) { // 30s debounce per unique message ID
            Log.d(TAG, "Suppressed duplicate notification for msgId: " + msgId);
            return false;
        }
        notifiedMessages.put(msgId, now);
        return true;
    }

    public static void createNotificationChannel(Context context) {
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
                channel.setLightColor(0xFFF5C542);
                channel.setLockscreenVisibility(NotificationCompat.VISIBILITY_PUBLIC);
                channel.setBypassDnd(true);

                AudioAttributes audioAttributes = new AudioAttributes.Builder()
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .setUsage(AudioAttributes.USAGE_NOTIFICATION_COMMUNICATION_INSTANT)
                    .build();
                Uri defaultSound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                channel.setSound(defaultSound, audioAttributes);

                NotificationManager manager = context.getSystemService(NotificationManager.class);
                if (manager != null) {
                    manager.createNotificationChannel(channel);
                }
            } catch (Exception e) {
                Log.e(TAG, "Error creating message notification channel: " + e.getMessage());
            }
        }
    }

    public static void postMessageNotification(Context context, String msgId, String sender, String message) {
        if (!shouldNotifyMessage(msgId)) {
            return;
        }

        try {
            createNotificationChannel(context);

            Intent intent = new Intent(context, MainActivity.class);
            intent.setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
            PendingIntent pendingIntent = PendingIntent.getActivity(
                context,
                0,
                intent,
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.M ? PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_UPDATE_CURRENT
            );

            // Device physical vibration
            try {
                Vibrator vibrator = (Vibrator) context.getSystemService(Context.VIBRATOR_SERVICE);
                if (vibrator != null && vibrator.hasVibrator()) {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                        vibrator.vibrate(VibrationEffect.createWaveform(new long[]{0, 250, 150, 250}, -1));
                    } else {
                        vibrator.vibrate(new long[]{0, 250, 150, 250}, -1);
                    }
                }
            } catch (Exception ignored) {}

            // Notification ringtone
            try {
                Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                Ringtone ringtone = RingtoneManager.getRingtone(context.getApplicationContext(), soundUri);
                if (ringtone != null) ringtone.play();
            } catch (Exception ignored) {}

            Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);

            NotificationCompat.Builder builder = new NotificationCompat.Builder(context, MSG_CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_stat_virgo)
                .setContentTitle(sender)
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
                builder.setLargeIcon(BitmapFactory.decodeResource(context.getResources(), R.mipmap.ic_launcher));
            } catch (Throwable ignored) {}

            // Deterministic notification ID so updates or race conditions update in-place instead of stacking duplicates
            int notifId = (msgId != null && !msgId.trim().isEmpty())
                ? Math.abs(msgId.hashCode() % 100000000)
                : Math.abs(sender.hashCode() % 100000000);

            NotificationManager notificationManager = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (notificationManager != null) {
                notificationManager.notify(notifId, builder.build());
                Log.d(TAG, "✅ Posted single notification [id=" + notifId + ", msgId=" + msgId + ", from=" + sender + "]");
            }
        } catch (Throwable t) {
            Log.e(TAG, "Error posting notification: " + t.getMessage(), t);
        }
    }
}
