// satchel page: renders what the Go core reports and forwards what the
// person does. It never implements protocol, and every string that came from
// a peer is written with textContent — never innerHTML.
"use strict";

const $ = (id) => document.getElementById(id);
const send = (cmd, files) => window.satchel_send(JSON.stringify(cmd), files);

const state = { phrase: "", url: "", offers: new Map(), outgoing: new Map() };

// ---- formatting ----
function size(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + " " + u[i];
}
const icon = { text: "✎", image: "▣", file: "▤", dir: "▥" };
function describe(items, bytes) {
  return items === 1 ? size(bytes) : `${items} items · ${size(bytes)}`;
}

function toast(msg) {
  const t = $("toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; }, 5000);
}

function show(view) {
  for (const v of document.querySelectorAll(".view")) v.classList.toggle("hidden", v.id !== view);
}

// ---- list items ----
function newItem(kind, name, meta) {
  const li = $("tpl-offer").content.firstElementChild.cloneNode(true);
  li.querySelector(".item-kind").textContent = icon[kind] || icon.file;
  li.querySelector(".item-name").textContent = name;
  li.querySelector(".item-meta").textContent = meta;
  return li;
}
function button(label, cls, onClick) {
  const b = document.createElement("button");
  b.className = "gl-btn " + cls;
  b.textContent = label;
  b.addEventListener("click", onClick);
  return b;
}
function setProgress(li, done, total) {
  const p = li.querySelector("progress");
  p.hidden = false;
  p.value = total ? done / total : 1;
}
function status(li, text) {
  const a = li.querySelector(".item-actions");
  a.replaceChildren();
  const s = document.createElement("span");
  s.className = "gl-hint";
  s.textContent = text;
  a.append(s);
}

// ---- core events ----
const handlers = {
  "core.ready"() {
    $("btn-join").disabled = false;
    $("btn-create").disabled = false;
    $("home-status").textContent = "ready";
    const h = decodeURIComponent(location.hash.slice(1)).trim();
    if (h) { $("join-phrase").value = h; join(); }
  },
  "session.created"(e) {
    state.phrase = e.phrase;
    state.url = e.url;
    $("session-role").textContent = "your share";
    $("session-phrase").textContent = e.phrase;
    $("invite").hidden = false;
    if (e.qr) $("invite-qr").src = "data:image/png;base64," + e.qr;
    history.replaceState(null, "", "#" + e.phrase);
    show("view-session");
  },
  "session.joined"(e) {
    state.phrase = e.phrase;
    $("session-role").textContent = "joined";
    $("session-phrase").textContent = e.phrase;
    $("invite").hidden = true;
    $("peers").textContent = "connected";
    show("view-session");
  },
  peers(e) {
    $("peers").textContent = e.count === 0 ? "waiting for someone to join…"
      : e.count === 1 ? "1 other person here" : `${e.count} other people here`;
  },
  "session.reconnecting"() { $("reconnect-banner").hidden = false; },
  "session.resumed"() { $("reconnect-banner").hidden = true; },
  "session.closed"(e) {
    $("reconnect-banner").hidden = true;
    if (e.reason !== "left") toast("session ended: " + e.reason);
    reset();
  },
  error(e) { toast(e.message); },

  hashing(e) {
    $("send-status").textContent = `preparing ${e.index + 1} of ${e.count}: ${e.name}`;
  },
  offered(e) {
    $("send-status").textContent = "";
    const li = newItem(e.items === 1 ? "file" : "dir", e.name, describe(e.items, e.bytes) + " · offered");
    li.querySelector(".item-actions").append(button("Withdraw", "ghost", () => {
      send({ type: "withdraw", id: e.id });
      status(li, "withdrawn");
    }));
    state.outgoing.set(e.id, li);
    $("outgoing").prepend(li);
  },
  delivered(e) {
    const li = state.outgoing.get(e.id);
    if (li) li.querySelector(".item-meta").textContent += " · delivered ✓";
  },

  offer(e) {
    $("in-empty").hidden = true;
    const li = newItem(e.kind, e.name, describe(e.items, e.bytes));
    const actions = li.querySelector(".item-actions");
    if (e.tooBig) {
      status(li, "too large for a browser — receive it in the satchel app");
    } else {
      actions.append(
        button("Accept", "primary", () => { status(li, "receiving…"); send({ type: "accept", from: e.from, id: e.id }); }),
        button("Decline", "ghost", () => { send({ type: "decline", from: e.from, id: e.id }); li.remove(); }),
      );
    }
    state.offers.set(e.id, { li, from: e.from });
    $("incoming").prepend(li);
  },
  progress(e) {
    const o = state.offers.get(e.id);
    if (o) setProgress(o.li, e.done, e.total);
  },
  item(e, bytes) {
    const o = state.offers.get(e.id);
    if (o) render(o.li.querySelector(".item-body"), e, bytes);
  },
  received(e) {
    const o = state.offers.get(e.id);
    if (o) { setProgress(o.li, 1, 1); status(o.li, "received ✓"); }
  },
  failed(e) {
    const o = state.offers.get(e.id) || { li: state.outgoing.get(e.id) };
    if (o.li) status(o.li, "failed: " + e.message);
  },
  withdrawn(e) {
    const o = state.offers.get(e.id);
    if (o) status(o.li, "withdrawn by the sender");
  },
};

// render shows one received item: text inline with a copy button, images as
// a preview, everything else as a download link. A file's name is only ever
// the download attribute's suggestion; the browser decides where it goes.
function render(body, e, bytes) {
  const blob = new Blob([bytes], { type: e.mime || "application/octet-stream" });
  const url = URL.createObjectURL(blob);
  const name = e.path.split("/").pop();
  const row = document.createElement("div");
  row.className = "received";
  if (e.kind === "text") {
    const pre = document.createElement("pre");
    pre.className = "text";
    pre.textContent = new TextDecoder().decode(bytes);
    row.append(pre, button("Copy", "ghost", () => navigator.clipboard.writeText(pre.textContent).then(() => toast("copied"))));
  } else if (e.kind === "image" || (e.mime || "").startsWith("image/")) {
    const img = document.createElement("img");
    img.alt = name;
    img.src = url;
    row.append(img);
  }
  if (e.kind !== "text") {
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.className = "gl-btn ghost";
    a.textContent = "Save " + (e.path.includes("/") ? e.path : name);
    row.append(a);
  }
  body.append(row);
}

window.satchelOnEvent = (json, bytes) => {
  const e = JSON.parse(json);
  const h = handlers[e.type];
  if (h) h(e, bytes);
};

// ---- actions ----
function join() {
  const p = $("join-phrase").value.trim();
  if (!p) { toast("enter a code phrase"); return; }
  $("home-status").textContent = "joining…";
  send({ type: "join", phrase: p });
}
function reset() {
  state.offers.clear();
  state.outgoing.clear();
  $("incoming").replaceChildren();
  $("outgoing").replaceChildren();
  $("in-empty").hidden = false;
  history.replaceState(null, "", location.pathname);
  $("home-status").textContent = "ready";
  show("view-home");
}
function offerFiles(list) {
  if (!list || !list.length) return;
  send({ type: "offer.files" }, Array.from(list));
}

$("btn-join").addEventListener("click", join);
$("join-phrase").addEventListener("keydown", (ev) => { if (ev.key === "Enter") join(); });
$("btn-create").addEventListener("click", () => { $("home-status").textContent = "starting…"; send({ type: "create" }); });
$("btn-leave").addEventListener("click", () => { send({ type: "leave" }); reset(); });
$("btn-copy-link").addEventListener("click", () => navigator.clipboard.writeText(state.url).then(() => toast("link copied")));
$("btn-copy-phrase").addEventListener("click", () => navigator.clipboard.writeText(state.phrase).then(() => toast("phrase copied")));
$("btn-send-text").addEventListener("click", () => {
  const t = $("send-text").value;
  if (!t.trim()) return;
  send({ type: "offer.text", text: t });
  $("send-text").value = "";
});
$("pick-files").addEventListener("change", (ev) => { offerFiles(ev.target.files); ev.target.value = ""; });
$("pick-folder").addEventListener("change", (ev) => { offerFiles(ev.target.files); ev.target.value = ""; });

const drop = $("drop");
drop.addEventListener("dragover", (ev) => { ev.preventDefault(); drop.classList.add("over"); });
drop.addEventListener("dragleave", () => drop.classList.remove("over"));
drop.addEventListener("drop", (ev) => {
  ev.preventDefault();
  drop.classList.remove("over");
  offerFiles(ev.dataTransfer.files);
});

fetch("/version").then((r) => r.text()).then((v) => { $("version-badge").textContent = "satchel " + v; }).catch(() => {});

// ---- boot the core ----
const go = new Go();
WebAssembly.instantiateStreaming(fetch("satchel.wasm"), go.importObject)
  .then((r) => go.run(r.instance))
  .catch((err) => { $("home-status").textContent = "couldn't load the core: " + err; });
