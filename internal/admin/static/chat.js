// Cherry staff UI: perpetual infinite scroll (before=) and real-time keyset polling (after=) for the Moderation -> Chat
// log page. Uses DOM creation and textContent only (strict CSP, zero innerHTML).
(function () {
  'use strict';

  var feed = document.querySelector('[data-chat-feed]');
  if (!feed || !window.fetch) {
    return;
  }

  var tbody = feed.querySelector('tbody');
  var statusEl = feed.querySelector('[data-chat-status]');
  var toggleBtn = feed.querySelector('[data-chat-toggle]');
  var pendingBtn = feed.querySelector('[data-chat-pending]');
  var jumpLink = feed.querySelector('[data-chat-jump]');
  var moreWrap = feed.querySelector('[data-chat-more]');
  var loadOlderBtn = feed.querySelector('[data-chat-load-older]');
  var moreStatusEl = feed.querySelector('[data-chat-more-status]');
  if (!tbody) {
    return;
  }

  var live = feed.getAttribute('data-live') === '1';
  var newestId = parseInt(feed.getAttribute('data-newest-id') || '0', 10) || 0;
  var oldestId = parseInt(feed.getAttribute('data-oldest-id') || '0', 10) || 0;
  var hasMore = feed.getAttribute('data-has-more') === '1';
  var query = feed.getAttribute('data-query') || '';
  var newestHref = feed.getAttribute('data-newest-href') || '/chat';

  var maxDomRows = 500;
  var maxPending = 200;
  var pollIntervalMs = 2000;
  var catchUpMs = 250;

  var paused = false;
  var expired = false;
  var polling = false;
  var pollTimer = null;
  var loadingOlder = false;
  var olderError = false;
  var pending = [];
  var pendingOverflow = false;

  var seen = new Set();
  tbody.querySelectorAll('tr[data-id]').forEach(function (tr) {
    var id = parseInt(tr.getAttribute('data-id') || '0', 10);
    if (id > 0) {
      seen.add(id);
    }
  });

  function chatURL(param, cursor) {
    var url = '/chat.json?' + param + '=' + encodeURIComponent(String(cursor));
    if (query) {
      url += '&' + query;
    }
    return url;
  }

  function safeHref(href) {
    return href && href.charAt(0) === '/' ? href : '';
  }

  function safeKind(kind) {
    return /^[a-z]+$/.test(kind || '') ? kind : 'unknown';
  }

  function appendPlayer(parent, name, aid, href) {
    var link = safeHref(href);
    if (link) {
      var a = document.createElement('a');
      a.setAttribute('href', link);
      a.textContent = name || '';
      parent.appendChild(a);
    } else {
      parent.appendChild(document.createTextNode(name || ''));
    }
    parent.appendChild(document.createTextNode(' '));
    var aidSpan = document.createElement('span');
    aidSpan.className = 'muted small';
    aidSpan.textContent = aid || '';
    parent.appendChild(aidSpan);
  }

  function buildRow(row, isNew) {
    var tr = document.createElement('tr');
    tr.setAttribute('data-id', String(row.id));
    var kind = safeKind(row.kind);
    if (kind === 'whisper') {
      tr.classList.add('chat-whisper');
    }
    if (isNew) {
      tr.classList.add('chat-new');
    }

    var tdTime = document.createElement('td');
    tdTime.textContent = row.time || '';
    tr.appendChild(tdTime);

    var tdChan = document.createElement('td');
    var ctxLink = document.createElement('a');
    ctxLink.setAttribute('href', safeHref(row.context_href) || '/chat');
    ctxLink.setAttribute('title', 'Show this channel around this time');
    var badge = document.createElement('span');
    badge.className = 'chat-kind chat-kind-' + kind;
    badge.textContent = row.kind || '';
    ctxLink.appendChild(badge);
    ctxLink.appendChild(document.createTextNode(' ' + (row.channel || '')));
    tdChan.appendChild(ctxLink);
    tr.appendChild(tdChan);

    var tdSender = document.createElement('td');
    appendPlayer(tdSender, row.name, row.aid, row.player_href);
    if (row.target_aid) {
      tdSender.appendChild(document.createTextNode(' '));
      var arrow = document.createElement('span');
      arrow.className = 'chat-arrow';
      arrow.setAttribute('aria-label', 'to');
      arrow.textContent = '\u2192';
      tdSender.appendChild(arrow);
      tdSender.appendChild(document.createTextNode(' '));
      appendPlayer(tdSender, row.target_name, row.target_aid, row.target_href);
    }
    tr.appendChild(tdSender);

    var tdText = document.createElement('td');
    tdText.className = 'wrap';
    tdText.textContent = row.text || '';
    tr.appendChild(tdText);

    return tr;
  }

  function rowIDOf(tr) {
    return tr ? parseInt(tr.getAttribute('data-id') || '0', 10) || 0 : 0;
  }

  function trimBottomIfNeeded() {
    var trimmed = false;
    while (tbody.rows.length > maxDomRows) {
      var last = tbody.lastElementChild;
      if (!last) {
        break;
      }
      var removedID = rowIDOf(last);
      if (removedID > 0) {
        seen.delete(removedID);
      }
      last.remove();
      trimmed = true;
    }
    if (trimmed) {
      var tail = tbody.lastElementChild;
      var tailID = rowIDOf(tail);
      if (tailID > 0) {
        oldestId = tailID;
      }
      hasMore = true;
      updateMoreUI();
    }
  }

  function prependRows(rows) {
    if (!rows.length) {
      return;
    }
    var empty = tbody.querySelector('tr.empty');
    if (empty) {
      empty.remove();
    }
    // Rows from ?after= arrive in ascending ID order; inserting each at top places newest first.
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      if (!r || !r.id) {
        continue;
      }
      if (tbody.querySelector('tr[data-id="' + r.id + '"]')) {
        continue;
      }
      seen.add(r.id);
      tbody.insertBefore(buildRow(r, true), tbody.firstChild);
    }
    if (oldestId === 0) {
      oldestId = rowIDOf(tbody.lastElementChild);
    }
    trimBottomIfNeeded();
  }

  function updatePendingButton() {
    if (!pendingBtn) {
      return;
    }
    if (pending.length === 0 && !pendingOverflow) {
      pendingBtn.hidden = true;
      pendingBtn.textContent = '';
      return;
    }
    pendingBtn.hidden = false;
    if (pendingOverflow) {
      pendingBtn.textContent = maxPending + '+ new messages \u2191';
      return;
    }
    pendingBtn.textContent = pending.length + (pending.length === 1 ? ' new message \u2191' : ' new messages \u2191');
  }

  function flushPending(scrollTop) {
    if (pendingOverflow) {
      window.location.href = newestHref;
      return;
    }
    if (!pending.length) {
      return;
    }
    var batch = pending;
    pending = [];
    updatePendingButton();
    prependRows(batch);
    if (scrollTop) {
      window.scrollTo({ top: 0, behavior: 'smooth' });
    }
  }

  function updateStatus() {
    if (!statusEl) {
      return;
    }
    if (expired) {
      statusEl.className = 'chat-status is-expired';
      statusEl.textContent = '';
      statusEl.appendChild(document.createTextNode('Session expired \u2014 '));
      var a = document.createElement('a');
      a.setAttribute('href', '/auth/login?next=' + encodeURIComponent(newestHref));
      a.textContent = 'sign in again';
      statusEl.appendChild(a);
      if (toggleBtn) {
        toggleBtn.hidden = true;
      }
      return;
    }
    if (!live) {
      statusEl.className = 'chat-status is-history';
      statusEl.textContent = 'Viewing history';
      if (toggleBtn) {
        toggleBtn.hidden = true;
      }
      if (jumpLink) {
        jumpLink.hidden = false;
      }
      return;
    }
    if (jumpLink) {
      jumpLink.hidden = true;
    }
    if (paused) {
      statusEl.className = 'chat-status is-paused';
      statusEl.textContent = 'Paused';
      if (toggleBtn) {
        toggleBtn.hidden = false;
        toggleBtn.textContent = 'Resume';
      }
    } else {
      statusEl.className = 'chat-status is-live';
      statusEl.textContent = 'Live';
      if (toggleBtn) {
        toggleBtn.hidden = false;
        toggleBtn.textContent = 'Pause';
      }
    }
  }

  function updateMoreUI() {
    if (!moreWrap) {
      return;
    }
    if (!hasMore) {
      if (loadOlderBtn) {
        loadOlderBtn.hidden = true;
      }
      if (moreStatusEl) {
        moreStatusEl.textContent = tbody.querySelector('tr[data-id]') ? 'End of matching messages.' : '';
      }
      return;
    }
    if (loadingOlder) {
      if (loadOlderBtn) {
        loadOlderBtn.hidden = true;
      }
      if (moreStatusEl) {
        moreStatusEl.textContent = 'Loading older messages...';
      }
      return;
    }
    if (olderError) {
      if (loadOlderBtn) {
        loadOlderBtn.hidden = false;
        loadOlderBtn.textContent = 'Retry loading older messages';
      }
      if (moreStatusEl) {
        moreStatusEl.textContent = 'Could not load older messages.';
      }
      return;
    }
    if (loadOlderBtn) {
      loadOlderBtn.hidden = false;
      loadOlderBtn.textContent = 'Load older messages';
    }
    if (moreStatusEl) {
      moreStatusEl.textContent = '';
    }
  }

  function schedulePoll(delay) {
    if (pollTimer) {
      window.clearTimeout(pollTimer);
      pollTimer = null;
    }
    if (!live || paused || expired || document.hidden) {
      return;
    }
    pollTimer = window.setTimeout(pollOnce, delay);
  }

  function pollOnce() {
    if (!live || paused || expired || document.hidden || polling) {
      return;
    }
    polling = true;
    fetch(chatURL('after', newestId), { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) {
        if (r.status === 401) {
          expired = true;
          polling = false;
          updateStatus();
          return null;
        }
        if (!r.ok) {
          throw new Error('HTTP ' + r.status);
        }
        return r.json();
      })
      .then(function (data) {
        if (!data) {
          return;
        }
        if (typeof data.latest_id === 'number' && data.latest_id > newestId) {
          newestId = data.latest_id;
        }
        var incoming = [];
        (data.rows || []).forEach(function (row) {
          if (row && row.id && !seen.has(row.id)) {
            seen.add(row.id);
            incoming.push(row);
          }
        });
        if (incoming.length > 0) {
          if (window.scrollY < 160) {
            prependRows(incoming);
          } else {
            for (var i = 0; i < incoming.length; i++) {
              pending.push(incoming[i]);
            }
            if (pending.length > maxPending) {
              var dropped = pending.splice(0, pending.length - maxPending);
              for (var d = 0; d < dropped.length; d++) {
                if (dropped[d] && dropped[d].id) {
                  seen.delete(dropped[d].id);
                }
              }
              pendingOverflow = true;
            }
            updatePendingButton();
          }
        }
        polling = false;
        schedulePoll(data.has_more ? catchUpMs : pollIntervalMs);
      })
      .catch(function () {
        polling = false;
        schedulePoll(pollIntervalMs);
      });
  }

  function loadOlder() {
    if (!hasMore || loadingOlder || expired || oldestId <= 0) {
      return;
    }
    loadingOlder = true;
    olderError = false;
    updateMoreUI();
    var prevOldest = oldestId;
    fetch(chatURL('before', oldestId), { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) {
        if (r.status === 401) {
          expired = true;
          loadingOlder = false;
          updateStatus();
          updateMoreUI();
          return null;
        }
        if (!r.ok) {
          throw new Error('HTTP ' + r.status);
        }
        return r.json();
      })
      .then(function (data) {
        if (!data) {
          return;
        }
        var rows = data.rows || [];
        if (!rows.length) {
          hasMore = false;
        } else {
          for (var i = 0; i < rows.length; i++) {
            var r = rows[i];
            if (r && r.id && !seen.has(r.id)) {
              seen.add(r.id);
              tbody.appendChild(buildRow(r, false));
            }
          }
          var lastReturned = rows[rows.length - 1].id || 0;
          if (lastReturned > 0 && lastReturned < prevOldest) {
            oldestId = lastReturned;
            hasMore = !!data.has_more;
          } else {
            hasMore = false;
          }
        }
        loadingOlder = false;
        updateMoreUI();
      })
      .catch(function () {
        loadingOlder = false;
        olderError = true;
        updateMoreUI();
      });
  }

  if (toggleBtn) {
    toggleBtn.addEventListener('click', function () {
      if (!live || expired) {
        return;
      }
      paused = !paused;
      updateStatus();
      if (paused) {
        if (pollTimer) {
          window.clearTimeout(pollTimer);
          pollTimer = null;
        }
      } else {
        pollOnce();
      }
    });
  }

  if (pendingBtn) {
    pendingBtn.addEventListener('click', function () {
      flushPending(true);
    });
  }

  window.addEventListener('scroll', function () {
    if ((pending.length > 0 || pendingOverflow) && window.scrollY < 40) {
      flushPending(false);
    }
  }, { passive: true });

  if (loadOlderBtn) {
    loadOlderBtn.addEventListener('click', loadOlder);
  }

  if (moreWrap && 'IntersectionObserver' in window) {
    var observer = new window.IntersectionObserver(function (entries) {
      for (var i = 0; i < entries.length; i++) {
        if (entries[i].isIntersecting && hasMore && !loadingOlder && !olderError && !expired) {
          loadOlder();
          break;
        }
      }
    }, { rootMargin: '250px' });
    observer.observe(moreWrap);
  }

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      if (pollTimer) {
        window.clearTimeout(pollTimer);
        pollTimer = null;
      }
    } else if (live && !paused && !expired) {
      pollOnce();
    }
  });

  updateStatus();
  updateMoreUI();
  if (live) {
    schedulePoll(pollIntervalMs);
  }
})();
