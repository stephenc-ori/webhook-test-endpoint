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
  var bodyMode = "raw";   // "raw" | "json", reset per selected event
  var follow = true;      // auto-select the newest event as it arrives
  var proxyOn = false;    // mirror of config.proxyEnabled, drives UI affordances

  // The proxy secret is server-global and never stored in the endpoint config,
  // so we keep it in sessionStorage and send it as a header when relaying.
  function proxySecret() {
    return sessionStorage.getItem("proxySecret") || "";
  }
  function setProxySecret(v) {
    if (v) sessionStorage.setItem("proxySecret", v);
    else sessionStorage.removeItem("proxySecret");
  }
  function proxyHeaders(extra) {
    var h = extra || {};
    h["X-Proxy-Secret"] = proxySecret();
    return h;
  }

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
      if (follow && events.length) {
        selectEvent(events[events.length - 1].id);
      }
      render();
    }
  }

  function isJSONEvent(ev) {
    var vals = (ev.headers || {})["Content-Type"] || [];
    return vals.some(function (v) { return v.toLowerCase().indexOf("json") !== -1; });
  }

  // Central selection change: the body view defaults to pretty JSON when the
  // request said it was JSON, raw otherwise.
  function selectEvent(id) {
    if (selectedId === id) return;
    selectedId = id;
    bodyMode = id != null && isJSONEvent(byId[id]) ? "json" : "raw";
  }

  function setFollow(on) {
    follow = on;
    $("follow-btn").classList.toggle("active", on);
    if (on && events.length) {
      selectEvent(events[events.length - 1].id);
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
    setFollow(false);
    selectEvent(Number(e.currentTarget.dataset.id));
    render();
  }

  $("follow-btn").addEventListener("click", function () {
    setFollow(!follow);
  });

  // ----- detail pane -----
  function updateRedeliverButton() {
    var ev = selectedId != null ? byId[selectedId] : null;
    $("redeliver-btn").hidden = !(proxyOn && ev);
  }

  function renderDetail() {
    var ev = selectedId != null ? byId[selectedId] : null;
    $("detail-empty").hidden = !!ev;
    $("detail").hidden = !ev;
    updateRedeliverButton();
    if (!ev) return;

    $("download-btn").href = base + "api/events/" + ev.id + "/download";
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
    fetch(base + "api/events", { method: "DELETE" }).finally(function () {
      events = [];
      byId = {};
      selectedId = null;
      setFollow(true);
      render();
    });
  });

  // ----- proxy re-delivery / upload -----
  function showDeliverMsg(text, isError) {
    var el = $("deliver-msg");
    el.textContent = text;
    el.className = "config-msg " + (isError ? "error" : "success");
    if (!isError) setTimeout(function () {
      if (el.textContent === text) el.textContent = "";
    }, 4000);
  }

  // reportForward turns a forwardResult (or an error) into a user message.
  function reportForward(promise, what) {
    return promise.then(function (res) {
      if (res.status === 403) { showDeliverMsg("Proxy secret required or incorrect — re-enter it in Settings.", true); return; }
      if (res.status === 409) { showDeliverMsg("Proxy is not enabled for this endpoint.", true); return; }
      return res.json().then(function (r) {
        if (r.error) showDeliverMsg(what + " failed: " + r.error, true);
        else showDeliverMsg(what + " → " + (r.statusText || r.status) + (r.ok ? "" : " (destination reported an error)"), !r.ok);
      });
    }).catch(function () { showDeliverMsg(what + " failed", true); });
  }

  $("redeliver-btn").addEventListener("click", function () {
    if (selectedId == null) return;
    showDeliverMsg("Re-delivering…", false);
    reportForward(fetch(base + "api/events/" + selectedId + "/redeliver", {
      method: "POST",
      headers: proxyHeaders({})
    }), "Re-delivery");
  });

  $("upload-btn").addEventListener("click", function () { $("upload-input").click(); });
  $("upload-input").addEventListener("change", function (e) {
    var file = e.target.files[0];
    if (!file) return;
    var isBru = /\.bru$/i.test(file.name);
    showDeliverMsg("Delivering " + file.name + "…", false);
    reportForward(file.text().then(function (text) {
      return fetch(base + "api/deliver", {
        method: "POST",
        headers: proxyHeaders({ "Content-Type": isBru ? "text/plain" : (file.type || "application/octet-stream") }),
        body: text
      });
    }), "Delivery");
    e.target.value = ""; // allow re-selecting the same file
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
    $("proxy-fields").hidden = !form.elements.proxyEnabled.checked;
  }
  form.addEventListener("change", syncSubFields);

  // Reflect the saved proxy state into the parts of the UI outside the form.
  function applyProxyState(cfg) {
    proxyOn = !!cfg.proxyEnabled;
    $("upload-btn").hidden = !proxyOn;
    updateRedeliverButton();
  }

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
    form.elements.proxyEnabled.checked = !!cfg.proxyEnabled;
    form.elements.proxyURL.value = cfg.proxyURL || "";
    form.elements.proxySecret.value = proxySecret();
    applyProxyState(cfg);
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
      respBody: form.elements.respBody.value,
      proxyEnabled: form.elements.proxyEnabled.checked,
      proxyURL: form.elements.proxyURL.value
    };
    // Persist the secret locally so re-delivery/upload can reuse it.
    setProxySecret(form.elements.proxySecret.value);
    fetch(base + "api/config", {
      method: "PUT",
      headers: proxyHeaders({ "Content-Type": "application/json" }),
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

  // Some proxies (e.g. Cloudflare quick tunnels) silently buffer SSE: the
  // stream opens but no messages ever arrive. The server sends a ping event
  // every 20s, so a healthy stream is never silent for long. If nothing has
  // arrived recently, fall back to polling so the UI still refreshes.
  var lastStreamMsg = 0;

  function connect() {
    var es = new EventSource(base + "api/stream");
    es.addEventListener("webhook", function (msg) {
      lastStreamMsg = Date.now();
      mergeEvents([JSON.parse(msg.data)]);
    });
    es.addEventListener("ping", function () {
      lastStreamMsg = Date.now();
      $("conn-status").className = "conn-dot connected";
      $("conn-status").title = "Live stream connected";
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

  setInterval(function () {
    if (Date.now() - lastStreamMsg > 30000) {
      $("conn-status").className = "conn-dot polling";
      $("conn-status").title = "Live stream silent — polling every 5s";
      loadEvents();
    }
  }, 5000);

  fetch(base + "api/config").then(function (r) { return r.json(); }).then(fillForm);
  loadEvents();
  connect();
  render();
})();
