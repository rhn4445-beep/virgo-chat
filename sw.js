self.addEventListener('install', (event) => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(clients.claim());
});

// Wake up device when Go server sends background push
self.addEventListener('push', (event) => {
  let payload = { title: "Virgo", body: "New message received" };

  if (event.data) {
    try {
      payload = event.data.json();
    } catch (e) {
      payload.body = event.data.text();
    }
  }

  const options = {
    body: payload.body,
    icon: '/icon.png',
    badge: '/icon.png',
    vibrate: [200, 100, 200],
    data: { url: '/' },
    actions: [{ action: 'open', title: 'Open Chat' }]
  };

  event.waitUntil(
    self.registration.showNotification(payload.title, options)
  );
});

// Open app when user taps the notification banner
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if (client.url && 'focus' in client) {
          return client.focus();
        }
      }
      if (clients.openWindow) {
        return clients.openWindow('/');
      }
    })
  );
});

// Clear active notifications when messages are read inside the app
self.addEventListener('message', (event) => {
  if (event.data && (event.data.action === 'clearNotifications' || event.data.action === 'clearAll')) {
    const target = (event.data.sender || '').toLowerCase().trim();
    const prefix = target.includes('@') ? target.split('@')[0] : target;
    self.registration.getNotifications().then((notifications) => {
      notifications.forEach((n) => {
        if (!target) {
          n.close();
        } else {
          const title = (n.title || '').toLowerCase();
          const body = (n.body || '').toLowerCase();
          if (title.includes(target) || body.includes(target) || (prefix && (title.includes(prefix) || body.includes(prefix)))) {
            n.close();
          } else {
            n.close();
          }
        }
      });
    });
  }
});