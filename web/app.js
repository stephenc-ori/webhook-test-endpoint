/* SPA for a single webhook endpoint.
   Keeps every event seen during this browser session (union of the server's
   last-50 snapshot and everything received live over SSE). */
(function () {
  "use strict";

  // The page is served at /{id}/ — derive the endpoint ID from the path.
  var endpointId = window.location.pathname.split("/").filter(Boolean)[0];
  var base = "/" + endpointId + "/";
  var hookURL = window.location.origin + base + "hook";

  var events = [];        // all events this session, oldest first
  var byId = {};          // event id -> event (server ids are monotonic)
  var selectedId = null;
  var bodyMode = "raw";   // "raw" | "json"

  var $ = function (id) { return document.getElementById(id); };

  // ----- header / hook URL -----
  $("hook-url").textContent = hookURL;
  $("curl-example").textContent = "curl -X POST \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"hello\":\"world\"}' \\\n  " + hookURL;
  $("copy-btn").addEventListener("click", function () {
    navigator.clipboard.writeText(hookURL).then(function () {
      var b = $("copy-btn");
      b.textContent = "Copied";
      setTimeout(function () { b.textContent = "Copy"; }, 1500);
    });
  });

  // ----- event list -----
  function mergeEvents(list) {
    var added = false;
    list.forEach(function (ev) {
      if (!byId[ev.id]) {
        byId[ev.id] = ev;
        events.push(ev);
        added = true;
      }
    });
    if (added) {
      events.sort(function (a, b) { return a.id - b.id; });
      render();
    }
  }

  function fmtTime(iso) {
    var d = new Date(iso);
    return d.toLocaleTimeString();
  }

  function statusDots(ev) {
    var html = "";
    if (ev.authResult !== "n/a") {
      html += '<span class="dot ' + (ev.authResult === "ok" ? "ok" : "bad") +
              '" title="Auth: ' + ev.authResult + '"></span>';
    }
    if (ev.sigResult !== "n/a") {
      html += '<span class="dot ' + (ev.sigResult === "ok" ? "ok" : "bad") +
              '" title="Signature: ' + ev.sigResult + '"></span>';
    }
    return html;
  }

  function render() {
    var list = $("event-list");
    list.innerHTML = "";
    // newest first in the list
    for (var i = events.length - 1; i >= 0; i--) {
      var ev = events[i];
      var li = document.createElement("li");
      li.className = "event-item" +
        (ev.id === selectedId ? " selected" : "") +
        (ev.rejected ? " rejected" : "");
      li.dataset.id = ev.id;
      li.innerHTML =
        '<span class="method-badge m-' + ev.method.toLowerCase() + '">' + ev.method + "</span>" +
        '<span class="event-time">' + fmtTime(ev.receivedAt) + "</span>" +
        '<span class="event-status">' + statusDots(ev) +
        (ev.rejected ? '<span class="rejected-tag">rejected</span>' : "") + "</span>";
      li.addEventListener("click", onSelect);
      list.appendChild(li);
    }
    $("event-count").textContent = events.length;
    $("empty-state").hidden = events.length > 0;
    renderDetail();
  }

  function onSelect(e) {
    selectedId = Number(e.currentTarget.dataset.id);
    render();
  }

  // ----- detail pane -----
  function renderDetail() {
    var ev = selectedId != null ? byId[selectedId] : null;
    $("detail-empty").hidden = !!ev;
    $("detail").hidden = !ev;
    if (!ev) return;

    $("d-method").textContent = ev.method;
    $("d-method").className = "method-badge m-" + ev.method.toLowerCase();
    $("d-path").textContent = ev.path;
    $("d-time").textContent = new Date(ev.receivedAt).toLocaleString();
    $("d-remote").textContent = ev.remoteAddr;

    var badges = "";
    if (ev.authResult !== "n/a") {
      badges += '<span class="badge ' + (ev.authResult === "ok" ? "ok" : "bad") + '">auth ' + ev.authResult + "</span>";
    }
    if (ev.sigResult !== "n/a") {
      badges += '<span class="badge ' + (ev.sigResult === "ok" ? "ok" : "bad") + '">signature ' + ev.sigResult + "</span>";
    }
    if (ev.rejected) badges += '<span class="badge bad">rejected</span>';
    if (ev.bodyTruncated) badges += '<span class="badge warn">body truncated at 1 MiB</span>';
    $("d-badges").innerHTML = badges;

    var tbody = $("d-headers");
    tbody.innerHTML = "";
    Object.keys(ev.headers || {}).sort().forEach(function (name) {
      ev.headers[name].forEach(function (value) {
        var tr = document.createElement("tr");
        var th = document.createElement("td");
        th.className = "hname";
        th.textContent = name;
        var td = document.createElement("td");
        td.textContent = value;
        tr.appendChild(th);
        tr.appendChild(td);
        tbody.appendChild(tr);
      });
    });

    var body = ev.body || "";
    var pretty = null;
    try { pretty = JSON.stringify(JSON.parse(body), null, 2); } catch (e) { /* not JSON */ }
    $("body-json").disabled = pretty === null;
    var mode = pretty === null ? "raw" : bodyMode;
    $("body-raw").classList.toggle("active", mode === "raw");
    $("body-json").classList.toggle("active", mode === "json");
    $("d-body").textContent = body === "" ? "(empty body)" : (mode === "json" ? pretty : body);
  }

  $("body-raw").addEventListener("click", function () { bodyMode = "raw"; renderDetail(); });
  $("body-json").addEventListener("click", function () { bodyMode = "json"; renderDetail(); });

  $("clear-btn").addEventListener("click", function () {
    events = [];
    byId = {};
    selectedId = null;
    render();
  });

  // ----- config panel -----
  var form = $("config-form");

  $("config-toggle").addEventListener("click", function () {
    $("config-panel").hidden = !$("config-panel").hidden;
  });

  function syncSubFields() {
    var mode = form.elements.authMode.value;
    $("basic-fields").hidden = mode !== "basic";
    $("bearer-fields").hidden = mode !== "bearer";
    $("sig-fields").hidden = !form.elements.sigEnabled.checked;
  }
  form.addEventListener("change", syncSubFields);

  function fillForm(cfg) {
    form.elements.authMode.value = cfg.authMode || "none";
    form.elements.basicUser.value = cfg.basicUser || "";
    form.elements.basicPass.value = cfg.basicPass || "";
    form.elements.bearerToken.value = cfg.bearerToken || "";
    form.elements.sigEnabled.checked = !!cfg.sigEnabled;
    form.elements.sigHeader.value = cfg.sigHeader || "X-Hub-Signature-256";
    form.elements.sigSecret.value = cfg.sigSecret || "";
    form.elements.failureMode.value = cfg.failureMode || "reject_log";
    form.elements.respStatus.value = cfg.respStatus || 200;
    form.elements.respContentType.value = cfg.respContentType || "application/json";
    form.elements.respBody.value = cfg.respBody != null ? cfg.respBody : '{"status":"success"}';
    syncSubFields();
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var cfg = {
      authMode: form.elements.authMode.value,
      basicUser: form.elements.basicUser.value,
      basicPass: form.elements.basicPass.value,
      bearerToken: form.elements.bearerToken.value,
      sigEnabled: form.elements.sigEnabled.checked,
      sigHeader: form.elements.sigHeader.value,
      sigSecret: form.elements.sigSecret.value,
      failureMode: form.elements.failureMode.value,
      respStatus: Number(form.elements.respStatus.value),
      respContentType: form.elements.respContentType.value,
      respBody: form.elements.respBody.value
    };
    fetch(base + "api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg)
    }).then(function (res) {
      if (res.ok) {
        showConfigMsg("Saved", false);
        return res.json().then(fillForm);
      }
      return res.text().then(function (t) { showConfigMsg(t.trim() || "Save failed", true); });
    }).catch(function () { showConfigMsg("Save failed", true); });
  });

  function showConfigMsg(text, isError) {
    var el = $("config-msg");
    el.textContent = text;
    el.className = "config-msg " + (isError ? "error" : "success");
    if (!isError) setTimeout(function () { el.textContent = ""; }, 2000);
  }

  // ----- data loading + SSE -----
  function loadEvents() {
    fetch(base + "api/events").then(function (r) { return r.json(); }).then(mergeEvents);
  }

  function connect() {
    var es = new EventSource(base + "api/stream");
    es.addEventListener("webhook", function (msg) {
      mergeEvents([JSON.parse(msg.data)]);
    });
    es.onopen = function () {
      $("conn-status").className = "conn-dot connected";
      $("conn-status").title = "Live stream connected";
      // Fill any gap that opened while we were disconnected.
      loadEvents();
    };
    es.onerror = function () {
      $("conn-status").className = "conn-dot disconnected";
      $("conn-status").title = "Live stream disconnected — reconnecting…";
    };
  }

  fetch(base + "api/config").then(function (r) { return r.json(); }).then(fillForm);
  loadEvents();
  connect();
  render();
})();
