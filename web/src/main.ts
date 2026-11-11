import { mount } from 'svelte';
import './app.css';
import './lib/focusring.svelte';
import App from './App.svelte';

mount(App, { target: document.getElementById('app')! });

// App-shell caching (public/sw.js). Browsers only allow service workers in a
// secure context: localhost or HTTPS, not a plain-http LAN address.
if (import.meta.env.PROD && 'serviceWorker' in navigator && window.isSecureContext) {
  navigator.serviceWorker.register('/sw.js').catch(() => {
    // optional: the UI works the same without it
  });
}
