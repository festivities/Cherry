// Cherry staff UI: the form editor of a document draft (PLAN Roadmap 8, slice 8.7). It enhances the draft page: the body
// textarea stays the value the server reads, and every change of the form is written back into it as pretty JSON. Without
// this file the page is the textarea alone. The JSON is read by this file's own parser, not JSON.parse: key order is kept,
// a duplicate key is refused, and every number keeps its source text (1900000006 and 12345678901234567890 come back
// exactly, never as a double). A string keeps its source text until it is edited, so a save changes only the lines edited.
(function () {
  'use strict';

  var NUMBER_AT = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;
  var NUMBER_ALL = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/;
  var LONG_KEY = /note|desc|text|message|body|summary|quote|hint|comment|reason/i;
  var SUMMARY_KEYS = ['id', 'name', 'title', 'code', 'key'];

  // The model: {t:'o', keys, keyRaw, vals} | {t:'a', vals} | {t:'s', v, raw} | {t:'n', raw} | {t:'b', v} | {t:'z'}.
  // raw is the source text of a string or number (null for a string that was edited).

  function parse(text) {
    var i = 0;
    var n = text.length;

    function fail(msg, at) {
      var before = text.slice(0, at === undefined ? i : at).split('\n');
      var last = before[before.length - 1];
      throw new Error(msg + ' (line ' + before.length + ', column ' + (last.length + 1) + ')');
    }
    function skip() {
      while (i < n) {
        var c = text.charCodeAt(i);
        if (c !== 32 && c !== 9 && c !== 10 && c !== 13) break;
        i++;
      }
    }
    function word(w, node) {
      if (text.substr(i, w.length) !== w) fail('Expected "' + w + '"');
      i += w.length;
      return node;
    }
    function str() {
      var start = i;
      i++;
      for (;;) {
        if (i >= n) fail('A string is not closed', start);
        var c = text.charCodeAt(i);
        if (c === 34) break;
        if (c < 32) fail('A string holds a raw line break or control character');
        if (c === 92) {
          var e = text.charAt(i + 1);
          if (e === 'u') {
            if (!/^[0-9a-fA-F]{4}$/.test(text.substr(i + 2, 4))) fail('Invalid \\u escape');
            i += 6;
          } else if (e !== '' && '"\\/bfnrt'.indexOf(e) !== -1) {
            i += 2;
          } else {
            fail('Invalid escape in a string');
          }
        } else {
          i++;
        }
      }
      i++;
      var raw = text.slice(start, i);
      return { t: 's', v: JSON.parse(raw), raw: raw };
    }
    function num() {
      NUMBER_AT.lastIndex = i;
      var m = NUMBER_AT.exec(text);
      if (!m) fail('Not a number');
      i += m[0].length;
      return { t: 'n', raw: m[0] };
    }
    function object() {
      var node = { t: 'o', keys: [], keyRaw: [], vals: [] };
      var seen = new Set();
      i++;
      skip();
      if (text.charAt(i) === '}') {
        i++;
        return node;
      }
      for (;;) {
        skip();
        if (text.charAt(i) !== '"') fail('Expected a key in double quotes');
        var k = str();
        if (seen.has(k.v)) fail('Duplicate key "' + k.v + '" (the form cannot show two values for one key; fix it in the JSON view)');
        seen.add(k.v);
        skip();
        if (text.charAt(i) !== ':') fail('Expected ":" after the key');
        i++;
        node.keys.push(k.v);
        node.keyRaw.push(k.raw);
        node.vals.push(value());
        skip();
        var c = text.charAt(i);
        if (c === ',') {
          i++;
        } else if (c === '}') {
          i++;
          return node;
        } else {
          fail('Expected "," or "}"');
        }
      }
    }
    function array() {
      var node = { t: 'a', vals: [] };
      i++;
      skip();
      if (text.charAt(i) === ']') {
        i++;
        return node;
      }
      for (;;) {
        node.vals.push(value());
        skip();
        var c = text.charAt(i);
        if (c === ',') {
          i++;
        } else if (c === ']') {
          i++;
          return node;
        } else {
          fail('Expected "," or "]"');
        }
      }
    }
    function value() {
      skip();
      if (i >= n) fail('The JSON ends too early');
      var c = text.charAt(i);
      if (c === '{') return object();
      if (c === '[') return array();
      if (c === '"') return str();
      if (c === '-' || (c >= '0' && c <= '9')) return num();
      if (c === 't') return word('true', { t: 'b', v: true });
      if (c === 'f') return word('false', { t: 'b', v: false });
      if (c === 'n') return word('null', { t: 'z' });
      return fail('Unexpected "' + c + '"');
    }

    var root = value();
    skip();
    if (i < n) fail('Unexpected "' + text.charAt(i) + '" after the JSON value');
    return root;
  }

  // dump writes the model as pretty JSON with two spaces per level.
  function dump(node, depth) {
    function pad(d) {
      return new Array(d + 1).join('  ');
    }
    switch (node.t) {
      case 'o':
        if (!node.keys.length) return '{}';
        return '{\n' + node.keys.map(function (k, j) {
          return pad(depth + 1) + node.keyRaw[j] + ': ' + dump(node.vals[j], depth + 1);
        }).join(',\n') + '\n' + pad(depth) + '}';
      case 'a':
        if (!node.vals.length) return '[]';
        return '[\n' + node.vals.map(function (v) {
          return pad(depth + 1) + dump(v, depth + 1);
        }).join(',\n') + '\n' + pad(depth) + ']';
      case 's':
        return node.raw !== null ? node.raw : JSON.stringify(node.v);
      case 'n':
        return node.raw;
      case 'b':
        return node.v ? 'true' : 'false';
      default:
        return 'null';
    }
  }

  // clone copies a value; blank copies its shape with every value cleared (an added item starts as a blank copy).
  function clone(node) {
    switch (node.t) {
      case 'o':
        return { t: 'o', keys: node.keys.slice(), keyRaw: node.keyRaw.slice(), vals: node.vals.map(clone) };
      case 'a':
        return { t: 'a', vals: node.vals.map(clone) };
      case 's':
        return { t: 's', v: node.v, raw: node.raw };
      case 'n':
        return { t: 'n', raw: node.raw };
      case 'b':
        return { t: 'b', v: node.v };
      default:
        return { t: 'z' };
    }
  }
  function blank(node) {
    switch (node.t) {
      case 'o':
        return { t: 'o', keys: node.keys.slice(), keyRaw: node.keyRaw.slice(), vals: node.vals.map(blank) };
      case 'a':
        return { t: 'a', vals: [] };
      case 's':
        return { t: 's', v: '', raw: null };
      case 'n':
        return { t: 'n', raw: '0' };
      case 'b':
        return { t: 'b', v: false };
      default:
        return { t: 'z' };
    }
  }

  // ---- the form ----

  var st = {}; // the editor: textarea, model, elements, mode and the set of fields holding text that is not a number

  function h(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }
  function button(label, cls, onClick, title) {
    var b = h('button', cls, label);
    b.type = 'button';
    if (title) b.title = title;
    b.addEventListener('click', onClick);
    return b;
  }
  function humanise(key) {
    if (!key) return '';
    var words = String(key).replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[\s_\-.]+/).filter(Boolean);
    var s = words.map(function (w) {
      return w.toLowerCase() === 'id' ? 'ID' : w.toLowerCase();
    }).join(' ');
    return s.charAt(0).toUpperCase() + s.slice(1);
  }
  function countText(N) {
    var n = N.t === 'o' ? N.keys.length : N.vals.length;
    var word = N.t === 'o' ? 'field' : 'item';
    return n + ' ' + word + (n === 1 ? '' : 's');
  }
  function cardLabel(N, j) {
    var text = '';
    SUMMARY_KEYS.some(function (name) {
      var idx = N.keys.indexOf(name);
      if (idx === -1 || (N.vals[idx].t !== 's' && N.vals[idx].t !== 'n')) return false;
      text = name + ' ' + (N.vals[idx].t === 's' ? N.vals[idx].v : N.vals[idx].raw);
      return true;
    });
    return 'Item ' + (j + 1) + (text ? ' · ' + text : '');
  }

  function showError(title, msg) {
    st.errTitle.textContent = title;
    st.errText.textContent = msg;
    st.err.hidden = false;
  }
  function hideError() {
    st.err.hidden = true;
    st.errText.textContent = '';
  }
  function updateChanged() {
    var changed = st.ta.value !== st.baseline;
    st.dirty = changed;
    st.changedBadge.hidden = !changed;
  }
  // sync writes the model into the textarea: the server reads the textarea, so every change lands there at once.
  function sync() {
    if (st.root) st.ta.value = dump(st.root, 0) + '\n';
    updateChanged();
  }
  function onEdit() {
    sync();
    applyFilter();
  }
  // touched runs after a value of a field changed; a card's summary follows its identifier fields.
  function touched(parentEl, key) {
    if (key !== null && SUMMARY_KEYS.indexOf(key) !== -1 && parentEl && parentEl._summarize) parentEl._summarize();
    onEdit();
  }

  // fillChildren (re)builds the elements of container C inside el: an object's fields or an array's items.
  function fillChildren(el, C) {
    var body = el._body;
    Array.from(st.bad).forEach(function (c) {
      if (body.contains(c)) st.bad.delete(c);
    });
    body.textContent = '';
    el._kids = [];
    C.vals.forEach(function (_, j) {
      var k = childEl(C, j, el);
      body.appendChild(k);
      el._kids.push(k);
    });
    if (el._count) el._count.textContent = countText(C);
  }

  // itemActions are the buttons of an array item: move, duplicate and remove.
  function itemActions(C, j, parentEl) {
    function move(to) {
      var t = C.vals[j];
      C.vals[j] = C.vals[to];
      C.vals[to] = t;
      parentEl._fill();
      onEdit();
    }
    var box = h('div', 'ed-actions');
    box.appendChild(button('Up', 'ghost', function () { move(j - 1); }, 'Move up'));
    if (j === 0) box.firstChild.disabled = true;
    box.appendChild(button('Down', 'ghost', function () { move(j + 1); }, 'Move down'));
    if (j === C.vals.length - 1) box.lastChild.disabled = true;
    box.appendChild(button('Duplicate', 'ghost', function () {
      C.vals.splice(j + 1, 0, clone(C.vals[j]));
      parentEl._fill();
      onEdit();
    }, 'Insert a copy below'));
    box.appendChild(button('Remove', 'ghost', function () {
      C.vals.splice(j, 1);
      parentEl._fill();
      onEdit();
    }, 'Remove this item'));
    return box;
  }

  // containerEl is an object or a list: a fieldset with a collapsible body (details) and, for a list, its add buttons.
  function containerEl(N, label, key, cls, labelFn) {
    var f = h('fieldset', 'ed-node ' + cls);
    var det = h('details');
    det.open = true;
    var sum = h('summary');
    var labelEl = h('span', 'ed-label', label);
    sum.appendChild(labelEl);
    if (key !== null) sum.appendChild(h('code', 'ed-key', key));
    f._count = h('span', 'ed-count');
    sum.appendChild(f._count);
    det.appendChild(sum);
    f._body = h('div', 'ed-body');
    det.appendChild(f._body);
    f._host = det;
    if (labelFn) {
      f._summarize = function () {
        labelEl.textContent = labelFn();
      };
    }
    f._fill = function () {
      fillChildren(f, N);
    };
    f._own = function () {
      return label + ' ' + (key || '');
    };
    if (N.t === 'a') {
      var foot = h('div', 'ed-actions');
      foot.appendChild(button('Add item', 'ghost', function () {
        N.vals.push(N.vals.length ? blank(N.vals[N.vals.length - 1]) : { t: 's', v: '', raw: null });
        f._fill();
        onEdit();
      }, 'Add an item shaped like the last one, with its values cleared'));
      var dup = button('Duplicate last', 'ghost', function () {
        N.vals.push(clone(N.vals[N.vals.length - 1]));
        f._fill();
        onEdit();
      }, 'Add a copy of the last item');
      dup.disabled = N.vals.length === 0;
      foot.appendChild(dup);
      det.appendChild(foot);
    }
    f.appendChild(det);
    f._fill();
    return f;
  }

  // leafEl is a value: text, number, yes/no or null.
  function leafEl(C, j, parentEl) {
    var N = C.vals[j];
    var item = C.t === 'a';
    var key = item ? null : C.keys[j];
    var label = item ? 'Item ' + (j + 1) : humanise(key);
    var f = h('fieldset', 'ed-node ed-leaf');
    var legend = h('legend', '', label);
    if (key !== null) legend.appendChild(h('code', 'ed-key', key));
    f.appendChild(legend);
    var val = h('div', 'ed-val');
    var hint = h('small', 'ed-hint');
    hint.hidden = true;
    var ctl;
    var own;
    if (N.t === 's') {
      var multi = N.v.indexOf('\n') !== -1 || N.v.length > 80 || (key !== null && LONG_KEY.test(key));
      ctl = h(multi ? 'textarea' : 'input');
      if (multi) {
        ctl.rows = Math.min(12, Math.max(2, N.v.split('\n').length));
      } else {
        ctl.type = 'text';
      }
      ctl.value = N.v;
      ctl.setAttribute('aria-label', label);
      ctl.addEventListener('input', function () {
        N.v = ctl.value;
        N.raw = null;
        touched(parentEl, key);
      });
      own = function () { return ctl.value; };
      val.appendChild(ctl);
    } else if (N.t === 'n') {
      ctl = h('input');
      ctl.type = 'number';
      ctl.step = 'any';
      ctl.value = N.raw;
      ctl.setAttribute('aria-label', label);
      ctl.addEventListener('input', function () {
        if (NUMBER_ALL.test(ctl.value)) {
          N.raw = ctl.value;
          st.bad.delete(ctl);
          ctl.removeAttribute('aria-invalid');
          hint.hidden = true;
          touched(parentEl, key);
        } else {
          st.bad.add(ctl);
          ctl.setAttribute('aria-invalid', 'true');
          hint.textContent = 'Not a JSON number (for example 12, -3.5 or 1e6). The last valid value is kept until this is fixed.';
          hint.hidden = false;
        }
      });
      own = function () { return ctl.value; };
      val.appendChild(ctl);
    } else if (N.t === 'b') {
      ctl = h('input');
      ctl.type = 'checkbox';
      ctl.checked = N.v;
      var word = h('span', '', String(N.v));
      var check = h('label', 'ed-check');
      check.appendChild(ctl);
      check.appendChild(word);
      ctl.addEventListener('change', function () {
        N.v = ctl.checked;
        word.textContent = String(N.v);
        touched(parentEl, key);
      });
      own = function () { return String(ctl.checked); };
      val.appendChild(check);
    } else {
      val.appendChild(h('span', 'ed-null', 'null'));
      ctl = h('select');
      ctl.setAttribute('aria-label', 'Set a value for ' + label + ' (now null)');
      [['', 'Set a value…'], ['s', 'text'], ['n', 'number'], ['b', 'true or false']].forEach(function (o) {
        var opt = h('option', '', o[1]);
        opt.value = o[0];
        ctl.appendChild(opt);
      });
      ctl.addEventListener('change', function () {
        var pick = ctl.value;
        if (!pick) return;
        C.vals[j] = pick === 's' ? { t: 's', v: '', raw: null } : pick === 'n' ? { t: 'n', raw: '0' } : { t: 'b', v: false };
        parentEl._fill();
        onEdit();
      });
      own = function () { return 'null'; };
      val.appendChild(ctl);
    }
    f.appendChild(val);
    f.appendChild(hint);
    f._own = function () {
      return label + ' ' + (key || '') + ' ' + own();
    };
    f._host = f;
    return f;
  }

  // childEl is child j of container C, shown inside parentEl. Items of an array get their move and remove buttons.
  function childEl(C, j, parentEl) {
    var N = C.vals[j];
    var item = C.t === 'a';
    var key = item ? null : C.keys[j];
    var el;
    if (N.t === 'o') {
      el = item
        ? containerEl(N, cardLabel(N, j), null, 'ed-card', function () { return cardLabel(N, j); })
        : containerEl(N, humanise(key), key, 'ed-object');
    } else if (N.t === 'a') {
      el = containerEl(N, item ? 'Item ' + (j + 1) : humanise(key), key, item ? 'ed-list ed-item' : 'ed-list');
    } else {
      el = leafEl(C, j, parentEl);
    }
    if (item) el._host.appendChild(itemActions(C, j, parentEl));
    return el;
  }

  // applyFilter hides the fields whose key, label or value do not hold the filter text; a match shows its fields.
  function applyFilter() {
    var term = st.filter.value.trim().toLowerCase();
    (function walk(el, forced) {
      var own = forced || !term || (el._own ? el._own().toLowerCase().indexOf(term) !== -1 : false);
      var any = false;
      (el._kids || []).forEach(function (k) {
        if (walk(k, own)) any = true;
      });
      var visible = !term || own || any;
      el.hidden = !visible;
      return visible;
    })(st.tree, false);
  }

  function buildTree() {
    st.tree._body = st.tree;
    st.tree._fill = function () {
      fillChildren(st.tree, st.root);
    };
    st.tree._fill();
    applyFilter();
  }

  // enterForm reads the textarea (the JSON view may have changed it) and builds the form from it. A text that does not
  // parse, or a top-level value that is not an object or a list, keeps the JSON view and says why.
  function enterForm() {
    var root;
    try {
      root = parse(st.ta.value);
    } catch (e) {
      showError('The form cannot open this JSON', e.message + '. Fix it in the JSON view; nothing was changed.');
      return false;
    }
    if (root.t !== 'o' && root.t !== 'a') {
      showError('The form needs an object or a list', 'The body is a single value. Edit it in the JSON view.');
      return false;
    }
    st.root = root;
    st.bad.clear();
    buildTree();
    return true;
  }

  function setMode(mode) {
    var form = mode === 'form';
    if (form && st.mode === 'form') return; // a second click keeps the fields (and any number being typed)
    if (form && !enterForm()) return;
    if (form) hideError();
    st.mode = mode;
    st.field.hidden = form;
    st.tree.parentNode.hidden = !form;
    st.tools.hidden = !form;
    st.tabForm.className = form ? '' : 'ghost';
    st.tabJson.className = form ? 'ghost' : '';
    st.tabForm.setAttribute('aria-pressed', String(form));
    st.tabJson.setAttribute('aria-pressed', String(!form));
  }

  function setOpen(open) {
    st.tree.querySelectorAll('details').forEach(function (d) {
      d.open = open;
    });
  }

  function onSubmit(e) {
    if (st.mode === 'form') {
      if (st.bad.size) {
        e.preventDefault();
        showError('Not saved', 'A number field holds text that is not a JSON number. Fix the field marked in red, or switch to the JSON view.');
        return;
      }
      st.ta.value = dump(st.root, 0) + '\n';
    } else {
      try {
        parse(st.ta.value);
      } catch (err) {
        e.preventDefault();
        showError('Not saved', err.message + '.');
        return;
      }
    }
    st.dirty = false; // leaving on purpose: no unsaved-changes prompt
  }

  function init() {
    var ta = document.querySelector('textarea[data-json-editor]');
    if (!ta || !ta.form) return;
    st.ta = ta;
    st.field = ta.closest('.body-field') || ta.parentNode;
    st.baseline = ta.value;
    st.bad = new Set();
    st.dirty = false;
    ta.required = false; // the form and the server check the JSON; an empty field is refused there

    var ui = h('div', 'ed');
    var bar = h('div', 'ed-bar');
    var tabs = h('div', 'ed-tabs');
    tabs.setAttribute('role', 'group');
    tabs.setAttribute('aria-label', 'How to edit the body');
    st.tabForm = button('Form', '', function () { setMode('form'); });
    st.tabJson = button('JSON', 'ghost', function () { setMode('json'); });
    tabs.appendChild(st.tabForm);
    tabs.appendChild(st.tabJson);
    st.changedBadge = h('span', 'pill warn', 'Changed');
    st.changedBadge.hidden = true;
    st.changedBadge.setAttribute('role', 'status');
    st.tools = h('div', 'ed-tools');
    st.filter = h('input');
    st.filter.type = 'search';
    st.filter.placeholder = 'Filter fields';
    st.filter.setAttribute('aria-label', 'Filter fields by key or value');
    st.filter.addEventListener('input', applyFilter);
    st.tools.appendChild(st.filter);
    st.tools.appendChild(button('Expand all', 'ghost', function () { setOpen(true); }));
    st.tools.appendChild(button('Collapse all', 'ghost', function () { setOpen(false); }));
    bar.appendChild(tabs);
    bar.appendChild(st.changedBadge);
    bar.appendChild(st.tools);
    st.err = h('div', 'error-box ed-error');
    st.err.setAttribute('role', 'alert');
    st.err.hidden = true;
    st.errTitle = h('p', 'strong');
    st.errText = h('pre', 'error-msg');
    st.err.appendChild(st.errTitle);
    st.err.appendChild(st.errText);
    var wrap = h('div', 'ed-tree-wrap');
    st.tree = h('div', 'ed-tree');
    wrap.appendChild(st.tree);
    ui.appendChild(bar);
    ui.appendChild(st.err);
    ui.appendChild(wrap);
    st.field.parentNode.insertBefore(ui, st.field);

    ta.addEventListener('input', updateChanged);
    ta.form.addEventListener('submit', onSubmit);
    window.addEventListener('beforeunload', function (e) {
      if (st.dirty) {
        e.preventDefault();
        e.returnValue = '';
      }
    });

    // Start in the form when the JSON reads; otherwise in the JSON view, with the reason.
    try {
      parse(ta.value);
      setMode('form');
    } catch (err) {
      setMode('json');
      showError('The JSON does not parse', err.message + '. Fix it here; the form opens once it parses.');
    }
  }

  init();
})();
