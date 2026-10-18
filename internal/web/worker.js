// freshgo service worker: shows the notification of a push message while the
// interface is closed, and brings the interface up when it is clicked.
'use strict';

self.addEventListener('push', (event) => {
	let message = {};
	try {
		message = event.data ? event.data.json() : {};
	} catch {
		// A message that is not JSON still has to show something: the
		// browser takes back the permission from a worker that does not.
	}
	event.waitUntil(
		self.registration.showNotification(message.title || 'freshgo', {
			body: message.body || '',
			icon: new URL('static/icon-192.png', self.registration.scope).href,
			// One notification at a time: a newer message replaces the one
			// the reader has not looked at.
			tag: 'new-entries',
			renotify: true,
		}),
	);
});

self.addEventListener('notificationclick', (event) => {
	event.notification.close();
	event.waitUntil(
		(async () => {
			const scope = self.registration.scope;
			const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
			const open = windows.find((client) => client.url.startsWith(scope));
			if (open) {
				return open.focus();
			}
			return self.clients.openWindow(scope);
		})(),
	);
});
