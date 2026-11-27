// Cherry staff UI: inline item renaming on /items. Intercepts Save and Reset forms so volunteers can rename items
// without reloading the page or losing scroll position. Every form still works cleanly as a plain POST without JS.
(function () {
  'use strict';

  var grid = document.querySelector('[data-items-grid]');
  if (!grid || !window.fetch) {
    return;
  }

  var namedCountEl = document.getElementById('items-named-count');
  var totalCountEl = document.getElementById('items-total-count');
  var namedPctEl = document.getElementById('items-named-pct');
  var progressBarEl = document.getElementById('items-progress-bar');
  var customCountEl = document.getElementById('items-custom-count');

  function updateProgress(named, custom, total) {
    if (typeof named !== 'number' || typeof total !== 'number') {
      return;
    }
    if (namedCountEl) {
      namedCountEl.textContent = String(named);
    }
    if (totalCountEl) {
      totalCountEl.textContent = String(total);
    }
    if (customCountEl && typeof custom === 'number') {
      customCountEl.textContent = custom > 0 ? ' (' + custom + ' new)' : '';
    }
    var pctText = '0%';
    var bar = 0;
    if (total > 0) {
      var p = (named * 100) / total;
      pctText = p > 0 && p < 0.1 ? '<0.1%' : p.toFixed(1) + '%';
      bar = Math.min(100, Math.floor((p + 2.5) / 5) * 5);
      if (named > 0 && bar === 0) {
        bar = 5;
      }
    }
    if (namedPctEl) {
      namedPctEl.textContent = pctText;
    }
    if (progressBarEl) {
      progressBarEl.className = 'bar-fill w-' + bar;
    }
  }

  function setStatus(card, text, cls) {
    var el = card.querySelector('[data-item-status]');
    if (!el) {
      return;
    }
    el.textContent = text;
    el.className = 'item-status small' + (cls ? ' ' + cls : '');
  }

  function applyResult(card, data, label) {
    var input = card.querySelector('.item-name-input');
    var badge = card.querySelector('[data-item-badge]');
    var meta = card.querySelector('[data-item-meta]');
    var resetForm = card.querySelector('.item-reset-form');
    if (input && typeof data.name === 'string') {
      input.value = data.name;
    }
    card.classList.remove('is-named', 'is-builtin');
    if (data.custom) {
      card.classList.add('is-named');
      if (badge) {
        badge.textContent = 'Custom';
        badge.className = 'pill ok';
      }
      if (meta) {
        var text = 'Default: ' + (data.default_name || card.getAttribute('data-default') || '');
        if (data.updated) {
          text += ' \u00b7 ' + data.updated;
        }
        if (data.updated_by) {
          text += ' \u00b7 ' + data.updated_by;
        }
        meta.textContent = text;
      }
      if (resetForm) {
        resetForm.hidden = false;
      }
    } else if (data.builtin) {
      card.classList.add('is-builtin');
      if (badge) {
        badge.textContent = 'Built-in';
        badge.className = 'pill builtin';
      }
      if (meta) {
        meta.textContent = 'Built-in default';
      }
      if (resetForm) {
        resetForm.hidden = true;
      }
    } else {
      if (badge) {
        badge.textContent = 'Default';
        badge.className = 'pill';
      }
      if (meta) {
        meta.textContent = 'Default name';
      }
      if (resetForm) {
        resetForm.hidden = true;
      }
    }
    setStatus(card, label, 'ok');
    updateProgress(data.named_total, data.custom_total, data.catalog_total);
  }

  function focusNextInput(card) {
    var cards = Array.prototype.slice.call(grid.querySelectorAll('[data-item-card]'));
    var idx = cards.indexOf(card);
    if (idx >= 0 && idx + 1 < cards.length) {
      var nextInput = cards[idx + 1].querySelector('.item-name-input');
      if (nextInput) {
        nextInput.focus();
        nextInput.select();
      }
    }
  }

  function postForm(form, card, successLabel, advanceOnSuccess) {
    var btn = form.querySelector('button[type="submit"]');
    var prevLabel = btn ? btn.textContent : '';
    if (btn) {
      btn.disabled = true;
      btn.textContent = 'Saving\u2026';
    }
    setStatus(card, '', '');
    var body = new URLSearchParams(new FormData(form));
    fetch(form.action, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        'Accept': 'application/json',
        'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8'
      },
      body: body.toString()
    })
      .then(function (r) {
        if (r.status === 401) {
          window.location.href = '/auth/login?next=' + encodeURIComponent(window.location.pathname + window.location.search);
          return null;
        }
        return r.json().then(function (data) {
          return { ok: r.ok, data: data };
        });
      })
      .then(function (res) {
        if (!res) {
          return;
        }
        if (!res.ok) {
          setStatus(card, (res.data && res.data.error) || 'Could not save.', 'err');
          return;
        }
        applyResult(card, res.data, successLabel);
        if (advanceOnSuccess) {
          focusNextInput(card);
        }
      })
      .catch(function () {
        setStatus(card, 'Network error. Try again.', 'err');
      })
      .finally(function () {
        if (btn) {
          btn.disabled = false;
          btn.textContent = prevLabel;
        }
      });
  }

  var enterPressed = false;
  grid.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && e.target && e.target.classList && e.target.classList.contains('item-name-input')) {
      enterPressed = true;
    }
  });

  grid.addEventListener('submit', function (e) {
    var form = e.target;
    if (!form || !form.classList) {
      return;
    }
    var card = form.closest('[data-item-card]');
    if (!card) {
      return;
    }
    if (form.classList.contains('item-name-form')) {
      e.preventDefault();
      var advance = enterPressed;
      enterPressed = false;
      postForm(form, card, 'Saved', advance);
    } else if (form.classList.contains('item-reset-form')) {
      e.preventDefault();
      enterPressed = false;
      postForm(form, card, 'Reset to default', false);
    }
  });
})();
