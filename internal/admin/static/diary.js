// Cherry staff UI: perpetual infinite scroll (before_time/before_no/before_kind) and real-time keyset polling
// (after_post/after_comment) for Moderation -> Diary entries. Uses DOM creation and textContent only (strict CSP, zero innerHTML).
(function () {
  'use strict';

  var feed = document.querySelector('[data-diary-feed]');
  if (!feed || !window.fetch) {
    return;
  }

  var listEl = feed.querySelector('[data-diary-list]');
  var summaryEl = feed.querySelector('[data-diary-summary]');
  var statusEl = feed.querySelector('[data-diary-status]');
  var toggleBtn = feed.querySelector('[data-diary-toggle]');
  var pendingBtn = feed.querySelector('[data-diary-pending]');
  var jumpLink = feed.querySelector('[data-diary-jump]');
  var moreWrap = feed.querySelector('[data-diary-more]');
  var loadOlderBtn = feed.querySelector('[data-diary-load-older]');
  var moreStatusEl = feed.querySelector('[data-diary-more-status]');
  if (!listEl) {
    return;
  }

  var live = feed.getAttribute('data-live') === '1';
  var newestPost = parseInt(feed.getAttribute('data-newest-post') || '0', 10) || 0;
  var newestComment = parseInt(feed.getAttribute('data-newest-comment') || '0', 10) || 0;
  var oldestTime = parseInt(feed.getAttribute('data-oldest-time') || '0', 10) || 0;
  var oldestNo = parseInt(feed.getAttribute('data-oldest-no') || '0', 10) || 0;
  var oldestKind = feed.getAttribute('data-oldest-kind') || '';
  var hasMore = feed.getAttribute('data-has-more') === '1';
  var query = feed.getAttribute('data-query') || '';
  var liveHref = feed.getAttribute('data-live-href') || '/diary';
  var csrf = feed.getAttribute('data-csrf') || '';

  var maxDomCards = 500;
  var maxPending = 200;
  var pollIntervalMs = 3000;
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
  var rendered = new Set();
  listEl.querySelectorAll('article.diary-card[data-kind][data-no]').forEach(function (card) {
    var k = card.getAttribute('data-kind') + ':' + card.getAttribute('data-no');
    seen.add(k);
    rendered.add(k);
  });

  function safeKind(kind) {
    return kind === 'post' || kind === 'comment' ? kind : '';
  }

  function cardKey(row) {
    var kind = safeKind(row && row.kind);
    if (!kind || !row.no) {
      return '';
    }
    return kind + ':' + String(row.no);
  }

  function safeHref(href) {
    return href && href.charAt(0) === '/' ? href : '';
  }

  function afterURL() {
    var url = '/diary.json?after_post=' + encodeURIComponent(String(newestPost)) +
      '&after_comment=' + encodeURIComponent(String(newestComment));
    if (query) {
      url += '&' + query;
    }
    return url;
  }

  function beforeURL() {
    var url = '/diary.json?before_time=' + encodeURIComponent(String(oldestTime)) +
      '&before_no=' + encodeURIComponent(String(oldestNo)) +
      '&before_kind=' + encodeURIComponent(oldestKind);
    if (query) {
      url += '&' + query;
    }
    return url;
  }

  function appendLinkedName(parent, name, aid, href) {
    var link = safeHref(href);
    if (link) {
      var a = document.createElement('a');
      a.setAttribute('href', link);
      a.textContent = name || aid || '';
      parent.appendChild(a);
    } else if (name) {
      parent.appendChild(document.createTextNode(name));
    }
    if (link || name) {
      parent.appendChild(document.createTextNode(' '));
    }
    var aidSpan = document.createElement('span');
    aidSpan.className = 'muted small';
    aidSpan.textContent = aid || '';
    parent.appendChild(aidSpan);
  }

  function buildHiddenInput(name, value) {
    var input = document.createElement('input');
    input.type = 'hidden';
    input.name = name;
    input.value = value;
    return input;
  }

  function buildCard(row, isNew) {
    var kind = safeKind(row.kind) || 'post';
    var article = document.createElement('article');
    article.className = 'diary-card diary-kind-' + kind + (isNew ? ' diary-new' : '');
    article.setAttribute('data-kind', kind);
    article.setAttribute('data-no', String(row.no || ''));
    article.setAttribute('data-time-ms', String(row.time_ms || 0));
    article.setAttribute('data-no-num', String(row.no_num || 0));

    var meta = document.createElement('div');
    meta.className = 'diary-meta';

    var badge = document.createElement('span');
    badge.className = 'chat-kind diary-pill-' + kind;
    badge.textContent = kind === 'post' ? 'Entry' : 'Comment';
    meta.appendChild(badge);
    meta.appendChild(document.createTextNode(' '));

    var numSpan = document.createElement('span');
    if (kind === 'post') {
      numSpan.textContent = '#' + (row.no || '');
      meta.appendChild(numSpan);
      if (row.open_state) {
        meta.appendChild(document.createTextNode(' '));
        var statePill = document.createElement('span');
        statePill.className = 'pill';
        statePill.textContent = row.open_state;
        meta.appendChild(statePill);
      }
    } else {
      numSpan.textContent = '#' + (row.no || '') + ' on entry #' + (row.post_no || '');
      meta.appendChild(numSpan);
    }
    meta.appendChild(document.createTextNode(' '));

    var authorWrap = document.createElement('span');
    appendLinkedName(authorWrap, row.author_name, row.author_aid, row.author_href);
    meta.appendChild(authorWrap);

    if (kind === 'comment') {
      meta.appendChild(document.createTextNode(' '));
      var ownerWrap = document.createElement('span');
      ownerWrap.appendChild(document.createTextNode('on '));
      appendLinkedName(ownerWrap, row.owner_name, row.owner_aid, row.owner_href);
      ownerWrap.appendChild(document.createTextNode("'s diary"));
      meta.appendChild(ownerWrap);
    }

    if (row.time) {
      meta.appendChild(document.createTextNode(' '));
      var timeSpan = document.createElement('span');
      timeSpan.className = 'muted small';
      timeSpan.textContent = row.time + ' UTC';
      meta.appendChild(timeSpan);
    }
    article.appendChild(meta);

    if (row.title) {
      var titleEl = document.createElement('p');
      titleEl.className = 'strong';
      titleEl.textContent = row.title;
      article.appendChild(titleEl);
    }

    if (row.content) {
      var contentEl = document.createElement('p');
      contentEl.className = 'diary-text';
      contentEl.textContent = row.content;
      article.appendChild(contentEl);
    }

    var photoIDs = Array.isArray(row.photo_ids) ? row.photo_ids : [];
    if (photoIDs.length > 0) {
      var thumbs = document.createElement('div');
      thumbs.className = 'diary-thumbs';
      for (var i = 0; i < photoIDs.length; i++) {
        var pid = photoIDs[i];
        if (typeof pid !== 'number' || pid <= 0) {
          continue;
        }
        var photoPath = '/photos/' + String(pid) + '/file';
        var a = document.createElement('a');
        a.setAttribute('href', photoPath);
        var img = document.createElement('img');
        img.setAttribute('src', photoPath);
        img.setAttribute('alt', 'Diary photo #' + String(pid));
        img.setAttribute('loading', 'lazy');
        img.className = 'diary-thumb';
        a.appendChild(img);
        thumbs.appendChild(a);
      }
      if (thumbs.childNodes.length > 0) {
        article.appendChild(thumbs);
      }
    } else if (row.has_photos) {
      var noMedia = document.createElement('p');
      noMedia.className = 'muted small';
      noMedia.textContent = 'Photo attached (no stored media row).';
      article.appendChild(noMedia);
    }

    var actions = document.createElement('div');
    actions.className = 'diary-actions';
    if (row.author_player_id > 0) {
      var form = document.createElement('form');
      form.method = 'post';
      form.action = kind === 'post' ? '/diary/remove-post' : '/diary/remove-comment';
      form.className = 'row-form';
      form.setAttribute('data-confirm', kind === 'post'
        ? 'Remove this diary entry? Its comments, likes and unreferenced photos go with it. The audit trail keeps the number and your reason.'
        : 'Remove this diary comment? The audit trail keeps the number and your reason.');
      form.appendChild(buildHiddenInput('csrf', csrf));
      form.appendChild(buildHiddenInput('player_id', String(row.author_player_id)));
      form.appendChild(buildHiddenInput('no', String(row.no || '')));

      var reason = document.createElement('input');
      reason.type = 'text';
      reason.name = 'reason';
      reason.maxLength = 200;
      reason.placeholder = 'Reason';
      reason.setAttribute('aria-label', 'Reason for removing ' + kind + ' #' + String(row.no || ''));
      reason.required = true;
      form.appendChild(reason);
      form.appendChild(document.createTextNode(' '));

      var btn = document.createElement('button');
      btn.type = 'submit';
      btn.className = 'danger';
      btn.textContent = kind === 'post' ? 'Remove post' : 'Remove comment';
      form.appendChild(btn);

      actions.appendChild(form);
    } else {
      var noAcc = document.createElement('span');
      noAcc.className = 'muted small';
      noAcc.textContent = 'no account';
      actions.appendChild(noAcc);
    }
    article.appendChild(actions);

    return article;
  }

  function updateOldestFromCard(card) {
    if (!card) {
      return;
    }
    oldestTime = parseInt(card.getAttribute('data-time-ms') || '0', 10) || 0;
    oldestNo = parseInt(card.getAttribute('data-no-num') || '0', 10) || 0;
    oldestKind = safeKind(card.getAttribute('data-kind'));
  }

  function trimBottomIfNeeded() {
    var trimmed = false;
    var cards = listEl.querySelectorAll('article.diary-card');
    var count = cards.length;
    while (count > maxDomCards) {
      var last = listEl.lastElementChild;
      if (!last) {
        break;
      }
      var k = (last.getAttribute('data-kind') || '') + ':' + (last.getAttribute('data-no') || '');
      if (k !== ':') {
        seen.delete(k);
        rendered.delete(k);
      }
      last.remove();
      count--;
      trimmed = true;
    }
    if (trimmed) {
      updateOldestFromCard(listEl.lastElementChild);
      hasMore = true;
      updateMoreUI();
    }
  }

  function removeEmptyAndSummary() {
    var empty = listEl.querySelector('[data-diary-empty]');
    if (empty) {
      empty.remove();
    }
    if (summaryEl && summaryEl.parentNode) {
      summaryEl.remove();
      summaryEl = null;
    }
  }

  function prependCards(rows) {
    if (!rows.length) {
      return;
    }
    removeEmptyAndSummary();
    // Rows from ?after_post=&after_comment= arrive in ascending order; inserting each at top puts newest first.
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      var key = cardKey(r);
      if (!key || rendered.has(key)) {
        continue;
      }
      seen.add(key);
      rendered.add(key);
      listEl.insertBefore(buildCard(r, true), listEl.firstChild);
    }
    if (!oldestKind) {
      updateOldestFromCard(listEl.lastElementChild);
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
      pendingBtn.textContent = maxPending + '+ new items \u2191';
      return;
    }
    pendingBtn.textContent = pending.length + (pending.length === 1 ? ' new item \u2191' : ' new items \u2191');
  }

  function flushPending(scrollTop) {
    if (pendingOverflow) {
      window.location.href = liveHref;
      return;
    }
    if (!pending.length) {
      return;
    }
    var batch = pending;
    pending = [];
    updatePendingButton();
    prependCards(batch);
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
      a.setAttribute('href', '/auth/login?next=' + encodeURIComponent(liveHref));
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
        moreStatusEl.textContent = listEl.querySelector('article.diary-card') ? 'End of matching items.' : '';
      }
      return;
    }
    if (loadingOlder) {
      if (loadOlderBtn) {
        loadOlderBtn.hidden = true;
      }
      if (moreStatusEl) {
        moreStatusEl.textContent = 'Loading older items...';
      }
      return;
    }
    if (olderError) {
      if (loadOlderBtn) {
        loadOlderBtn.hidden = false;
        loadOlderBtn.textContent = 'Retry loading older items';
      }
      if (moreStatusEl) {
        moreStatusEl.textContent = 'Could not load older items.';
      }
      return;
    }
    if (loadOlderBtn) {
      loadOlderBtn.hidden = false;
      loadOlderBtn.textContent = 'Load older';
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
    fetch(afterURL(), { credentials: 'same-origin', cache: 'no-store' })
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
        if (typeof data.latest_post === 'number' && data.latest_post > newestPost) {
          newestPost = data.latest_post;
        }
        if (typeof data.latest_comment === 'number' && data.latest_comment > newestComment) {
          newestComment = data.latest_comment;
        }
        var incoming = [];
        (data.rows || []).forEach(function (row) {
          var key = cardKey(row);
          if (key && !seen.has(key)) {
            seen.add(key);
            incoming.push(row);
          }
        });
        if (incoming.length > 0) {
          if (window.scrollY < 160) {
            prependCards(incoming);
          } else {
            for (var i = 0; i < incoming.length; i++) {
              pending.push(incoming[i]);
            }
            if (pending.length > maxPending) {
              var dropped = pending.splice(0, pending.length - maxPending);
              for (var d = 0; d < dropped.length; d++) {
                var dk = cardKey(dropped[d]);
                if (dk) {
                  seen.delete(dk);
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
    if (!hasMore || loadingOlder || expired || !oldestKind) {
      return;
    }
    loadingOlder = true;
    olderError = false;
    updateMoreUI();
    fetch(beforeURL(), { credentials: 'same-origin', cache: 'no-store' })
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
          removeEmptyAndSummary();
          for (var i = 0; i < rows.length; i++) {
            var r = rows[i];
            var key = cardKey(r);
            if (key && !rendered.has(key)) {
              seen.add(key);
              rendered.add(key);
              listEl.appendChild(buildCard(r, false));
            }
          }
          var last = rows[rows.length - 1];
          var nextKind = safeKind(last && last.kind);
          if (nextKind) {
            oldestTime = last.time_ms || 0;
            oldestNo = last.no_num || 0;
            oldestKind = nextKind;
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
