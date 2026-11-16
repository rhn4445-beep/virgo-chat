package com.virgo.chat;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Canvas;
import android.graphics.drawable.BitmapDrawable;
import android.graphics.drawable.Drawable;
import android.media.AudioAttributes;
import android.media.Ringtone;
import android.media.RingtoneManager;
import android.net.Uri;
import android.os.Build;
import android.os.VibrationEffect;
import android.os.Vibrator;
import android.service.notification.StatusBarNotification;
import android.util.Log;
import androidx.core.app.NotificationCompat;
import androidx.core.content.ContextCompat;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

public class NotificationHelper {
    private static final String TAG = "VirgoNotifHelper";
    public static final String MSG_CHANNEL_ID = "virgo_chat_messages";
    public static final String CALL_CHANNEL_ID = "virgo_incoming_calls";
    public static final int CALL_NOTIFICATION_ID = 8888;
    private static volatile Ringtone activeCallRingtone = null;

    // Deduplication cache: keeps last 100 message IDs with timestamps
    private static final Map<String, Long> notifiedMessages = Collections.synchronizedMap(
        new LinkedHashMap<String, Long>(100, 0.75f, true) {
            @Override
            protected boolean removeEldestEntry(Map.Entry<String, Long> eldest) {
                return size() > 100;
            }
        }
    );

    // Active notification IDs per sender key for instant dismissal upon read
    private static final Map<String, Set<Integer>> activeSenderNotifications = new ConcurrentHashMap<>();

    public static String normalizeSenderKey(String raw) {
        if (raw == null) return "";
        String s = raw.trim().toLowerCase();
        if (s.contains("@")) return s;
        String digits = s.replaceAll("[^0-9]", "");
        if (digits.length() >= 10) {
            if (digits.length() == 12 && digits.startsWith("91")) return digits.substring(2);
            if (digits.length() == 11 && digits.startsWith("0")) return digits.substring(1);
            if (digits.length() == 10) return digits;
        }
        return s;
    }

    public static Bitmap getBitmapFromDrawable(Context context, int drawableId) {
        if (context == null) return null;
        try {
            Drawable drawable = ContextCompat.getDrawable(context, drawableId);
            if (drawable == null) {
                return null;
            }
            if (drawable instanceof BitmapDrawable) {
                return ((BitmapDrawable) drawable).getBitmap();
            }
            int width = drawable.getIntrinsicWidth() > 0 ? drawable.getIntrinsicWidth() : 192;
            int height = drawable.getIntrinsicHeight() > 0 ? drawable.getIntrinsicHeight() : 192;
            Bitmap bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888);
            Canvas canvas = new Canvas(bitmap);
            drawable.setBounds(0, 0, canvas.getWidth(), canvas.getHeight());
            drawable.draw(canvas);
            return bitmap;
        } catch (Throwable t) {
            Log.e(TAG, "Error converting drawable to bitmap: " + t.getMessage());
            try {
                return BitmapFactory.decodeResource(context.getResources(), R.drawable.ic_virgo_logo);
            } catch (Throwable ignored) {
                return null;
            }
        }
    }

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

            // Determine deterministic ID per sender
            String normSender = normalizeSenderKey(sender);
            int senderNotifId = Math.abs(normSender.hashCode() % 100000000);
            int notifId = (msgId != null && !msgId.trim().isEmpty())
                ? Math.abs(msgId.hashCode() % 100000000)
                : senderNotifId;

            // Track IDs for this sender so they can be dismissed when the chat is opened
            Set<Integer> idSet = activeSenderNotifications.computeIfAbsent(normSender, k -> Collections.newSetFromMap(new ConcurrentHashMap<>()));
            idSet.add(notifId);
            idSet.add(senderNotifId);

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

            // Display official Virgo celestial lotus logo
            Bitmap appLogo = getBitmapFromDrawable(context, R.mipmap.ic_launcher);
            if (appLogo == null) {
                appLogo = getBitmapFromDrawable(context, R.drawable.ic_virgo_logo);
            }
            if (appLogo != null) {
                builder.setLargeIcon(appLogo);
            }

            NotificationManager notificationManager = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (notificationManager != null) {
                notificationManager.notify(notifId, builder.build());
                Log.d(TAG, "✅ Posted notification [id=" + notifId + ", from=" + sender + "] with app logo");
            }
        } catch (Throwable t) {
            Log.e(TAG, "Error posting notification: " + t.getMessage(), t);
        }
    }

    public static void cancelNotificationsForSender(Context context, String sender) {
        if (context == null || sender == null) return;
        try {
            NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (nm == null) return;

            String norm = normalizeSenderKey(sender);
            String raw = sender.trim().toLowerCase();

            // 1. Cancel deterministic sender IDs
            nm.cancel(Math.abs(norm.hashCode() % 100000000));
            nm.cancel(Math.abs(raw.hashCode() % 100000000));

            // 2. Cancel all tracked message IDs for this sender
            Set<Integer> ids = activeSenderNotifications.remove(norm);
            if (ids != null) {
                for (Integer id : ids) {
                    if (id != null) nm.cancel(id);
                }
            }
            Set<Integer> rawIds = activeSenderNotifications.remove(raw);
            if (rawIds != null) {
                for (Integer id : rawIds) {
                    if (id != null) nm.cancel(id);
                }
            }
            Log.d(TAG, "🗑️ Dismissed all notifications in status bar for: " + sender);
        } catch (Throwable t) {
            Log.e(TAG, "Error cancelling notifications for sender: " + t.getMessage(), t);
        }
    }

    public static void cancelNotificationById(Context context, String msgId) {
        if (context == null || msgId == null || msgId.trim().isEmpty()) return;
        try {
            NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (nm != null) {
                int id = Math.abs(msgId.hashCode() % 100000000);
                nm.cancel(id);
                Log.d(TAG, "🗑️ Dismissed notification by ID: " + id);
            }
        } catch (Throwable t) {
            Log.e(TAG, "Error cancelling notification by ID: " + t.getMessage(), t);
        }
    }

    public static void cancelAllMessageNotifications(Context context) {
        if (context == null) return;
        try {
            NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (nm == null) return;

            // Cancel all tracked IDs
            for (Set<Integer> ids : activeSenderNotifications.values()) {
                if (ids != null) {
                    for (Integer id : ids) {
                        if (id != null) nm.cancel(id);
                    }
                }
            }
            activeSenderNotifications.clear();

            // On Android 6+ (API 23+), sweep active notifications and clear any non-service notifications
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                StatusBarNotification[] active = nm.getActiveNotifications();
                if (active != null) {
                    for (StatusBarNotification sbn : active) {
                        // Do NOT cancel the persistent background service notification (ID 2001)
                        if (sbn.getId() != 2001) {
                            nm.cancel(sbn.getId());
                        }
                    }
                }
            }
            Log.d(TAG, "🗑️ Cancelled all message notifications in status bar");
        } catch (Throwable t) {
            Log.e(TAG, "Error cancelling all notifications: " + t.getMessage(), t);
        }
    }

    public static void createCallNotificationChannel(Context context) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            try {
                CharSequence name = "Virgo Incoming Calls";
                String description = "Urgent high-priority incoming voice and video calls";
                int importance = NotificationManager.IMPORTANCE_HIGH;
                NotificationChannel channel = new NotificationChannel(CALL_CHANNEL_ID, name, importance);
                channel.setDescription(description);
                channel.enableVibration(true);
                channel.setVibrationPattern(new long[]{0, 1000, 500, 1000, 500});
                channel.enableLights(true);
                channel.setLightColor(0xFFF5C542);
                channel.setLockscreenVisibility(NotificationCompat.VISIBILITY_PUBLIC);
                channel.setBypassDnd(true);

                AudioAttributes audioAttributes = new AudioAttributes.Builder()
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .setUsage(AudioAttributes.USAGE_NOTIFICATION_RINGTONE)
                    .build();
                Uri defaultSound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);
                if (defaultSound == null) {
                    defaultSound = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                }
                channel.setSound(defaultSound, audioAttributes);

                NotificationManager manager = context.getSystemService(NotificationManager.class);
                if (manager != null) {
                    manager.createNotificationChannel(channel);
                }
            } catch (Exception e) {
                Log.e(TAG, "Error creating call notification channel: " + e.getMessage());
            }
        }
    }

    public static synchronized void postCallNotification(Context context, String callId, String caller, String callText) {
        try {
            createCallNotificationChannel(context);

            Intent intent = new Intent(context, MainActivity.class);
            intent.setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_NEW_TASK);
            intent.putExtra("action", "incoming_call");
            intent.putExtra("callId", callId);
            intent.putExtra("caller", caller);

            PendingIntent pendingIntent = PendingIntent.getActivity(
                context,
                CALL_NOTIFICATION_ID,
                intent,
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.M ? PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE : PendingIntent.FLAG_UPDATE_CURRENT
            );

            // Play ringtone
            try {
                if (activeCallRingtone != null && activeCallRingtone.isPlaying()) {
                    activeCallRingtone.stop();
                }
                Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);
                if (soundUri == null) soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION);
                activeCallRingtone = RingtoneManager.getRingtone(context.getApplicationContext(), soundUri);
                if (activeCallRingtone != null) {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                        activeCallRingtone.setLooping(true);
                    }
                    activeCallRingtone.play();
                }
            } catch (Exception ignored) {}

            // Vibrate
            try {
                Vibrator v = (Vibrator) context.getSystemService(Context.VIBRATOR_SERVICE);
                if (v != null && v.hasVibrator()) {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                        v.vibrate(VibrationEffect.createWaveform(new long[]{0, 1000, 500, 1000, 500}, 0));
                    } else {
                        v.vibrate(new long[]{0, 1000, 500, 1000, 500}, 0);
                    }
                }
            } catch (Exception ignored) {}

            Uri soundUri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);

            NotificationCompat.Builder builder = new NotificationCompat.Builder(context, CALL_CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_stat_virgo)
                .setContentTitle(caller != null ? caller : "Incoming Virgo Call")
                .setContentText(callText != null ? callText : "Incoming Call...")
                .setCategory(NotificationCompat.CATEGORY_CALL)
                .setPriority(NotificationCompat.PRIORITY_MAX)
                .setOngoing(true)
                .setAutoCancel(true)
                .setSound(soundUri)
                .setVibrate(new long[]{0, 1000, 500, 1000, 500})
                .setColor(0xFFF5C542)
                .setFullScreenIntent(pendingIntent, true)
                .setContentIntent(pendingIntent);

            Bitmap appLogo = getBitmapFromDrawable(context, R.mipmap.ic_launcher);
            if (appLogo == null) {
                appLogo = getBitmapFromDrawable(context, R.drawable.ic_virgo_logo);
            }
            if (appLogo != null) {
                builder.setLargeIcon(appLogo);
            }

            NotificationManager notificationManager = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (notificationManager != null) {
                notificationManager.notify(CALL_NOTIFICATION_ID, builder.build());
                Log.d(TAG, "📞 Posted urgent incoming call notification for caller: " + caller);
            }
        } catch (Throwable t) {
            Log.e(TAG, "Error posting call notification: " + t.getMessage(), t);
        }
    }

    public static synchronized void cancelCallNotification(Context context) {
        try {
            if (activeCallRingtone != null && activeCallRingtone.isPlaying()) {
                activeCallRingtone.stop();
            }
            activeCallRingtone = null;
        } catch (Exception ignored) {}

        try {
            Vibrator v = (Vibrator) context.getSystemService(Context.VIBRATOR_SERVICE);
            if (v != null) {
                v.cancel();
            }
        } catch (Exception ignored) {}

        try {
            NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (nm != null) {
                nm.cancel(CALL_NOTIFICATION_ID);
                Log.d(TAG, "📞 Cancelled call notification ID " + CALL_NOTIFICATION_ID);
            }
        } catch (Exception ignored) {}
    }
}
