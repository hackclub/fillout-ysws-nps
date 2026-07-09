// Localize any <time data-localtime> to the viewer's timezone. The server emits
// an absolute UTC instant in the datetime attribute and a UTC fallback as the
// text; here we swap the text for the local-zone rendering. Leaving the UTC text
// in place on parse failure keeps the value correct, just not local.
(function () {
  var els = document.querySelectorAll('time[data-localtime]');
  for (var i = 0; i < els.length; i++) {
    var iso = els[i].getAttribute('datetime');
    if (!iso) continue;
    var d = new Date(iso);
    if (isNaN(d.getTime())) continue;
    els[i].textContent = d.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit', timeZoneName: 'short'
    });
    els[i].title = d.toString();
  }
})();

// Show the optional template box only when a question maps to a feedback column
// (the templatable NPS columns).
(function () {
  var TEMPLATABLE = { doing_well: true, improve: true };
  document.querySelectorAll('.target-select').forEach(function (sel) {
    var tmpl = document.getElementById('template_' + sel.dataset.qid);
    if (!tmpl) return;
    var sync = function () {
      tmpl.classList.toggle('is-visible', !!TEMPLATABLE[sel.value]);
    };
    sel.addEventListener('change', sync);
    sync();
  });
})();

// Re-render the sample preview whenever the mapping changes, so it always shows
// what the sync would actually write. The server does the transform (POST
// /forms/preview/samples) and returns just the samples fragment, which we swap
// into #preview-samples. A sequence guard drops any response that a newer
// request has already superseded.
(function () {
  var form = document.querySelector('form[action="/sync/start"]');
  var box = document.getElementById('preview-samples');
  if (!form || !box) return;

  var seq = 0;
  function refresh() {
    var mine = ++seq;
    box.classList.add('is-loading');
    fetch('/forms/preview/samples', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams(new FormData(form)).toString(),
    }).then(function (resp) {
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      return resp.text();
    }).then(function (html) {
      if (mine !== seq) return;
      box.innerHTML = html;
      box.classList.remove('is-loading');
    }).catch(function () {
      if (mine !== seq) return;
      box.classList.remove('is-loading');
    });
  }

  var timer;
  function debounced() {
    clearTimeout(timer);
    timer = setTimeout(refresh, 350);
  }

  form.querySelectorAll('.target-select').forEach(function (sel) {
    sel.addEventListener('change', refresh);
  });
  form.querySelectorAll('.template-input').forEach(function (inp) {
    inp.addEventListener('input', debounced);
  });
})();
