// Cherry staff UI: a confirmation before a destructive form is sent (data-confirm), and the dashboard's refresh of its numbers
// from /stats.json every 15 seconds (data-stats). Every page works without this file; the server still checks each POST.
(function () {
  'use strict';

  document.addEventListener('submit', function (e) {
    var msg = e.target && e.target.getAttribute && e.target.getAttribute('data-confirm');
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
    }
  });

  if (!document.querySelector('[data-stats]') || !window.fetch) {
    return;
  }

  function setAll(key, text) {
    document.querySelectorAll('[data-k="' + key + '"]').forEach(function (el) {
      el.textContent = text;
    });
  }

  function show(s) {
    Object.keys(s).forEach(function (key) {
      var v = s[key];
      if (v === null || typeof v === 'object') {
        return;
      }
      setAll(key, key === 'requests_per_min' ? Number(v).toFixed(1) : String(v));
    });
    (s.save_hist || []).forEach(function (b, i) {
      var count = document.getElementById('hist-count-' + i);
      var fill = document.getElementById('hist-fill-' + i);
      if (count) {
        count.textContent = String(b.count);
      }
      if (fill) {
        fill.className = 'bar-fill w-' + b.bar;
      }
    });
  }

  function refresh() {
    fetch('/stats.json', { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) {
        if (r.status === 401) {
          window.location.href = '/auth/login?next=%2F';
          return null;
        }
        return r.ok ? r.json() : null;
      })
      .then(function (s) {
        if (s) {
          show(s);
        }
      })
      .catch(function () {
        // Keep the numbers on screen; the next tick tries again.
      });
  }

  window.setInterval(refresh, 15000);
})();
