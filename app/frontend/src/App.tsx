import { useCallback, useEffect, useState } from "react";
import { Events } from "@wailsio/runtime";
import { Service } from "../bindings/github.com/richardwooding/satchel/app";
import type { Offer, Pairing, Peer, Session, State } from "../bindings/github.com/richardwooding/satchel/internal/desk/models";

type Tab = "send" | "receive" | "devices" | "settings";

const empty: State = { shares: [], receives: [], peers: [], pairings: [] } as unknown as State;

function size(n: number): string {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : String(n)) + " " + u[i];
}

export default function App() {
  const [tab, setTab] = useState<Tab>("send");
  const [state, setState] = useState<State>(empty);
  const shares = state.shares ?? [];
  const receives = state.receives ?? [];
  const peers = state.peers ?? [];
  const pairings = state.pairings ?? [];
  const [busy, setBusy] = useState("");
  const [toast, setToast] = useState("");

  const fail = useCallback((err: unknown) => {
    setToast(String((err as Error)?.message ?? err));
    window.setTimeout(() => setToast(""), 6000);
  }, []);

  useEffect(() => {
    Service.State().then(setState).catch(fail);
    const offs = [
      Events.On("state", (ev) => setState(ev.data)),
      Events.On("view", (ev) => setTab(ev.data as Tab)),
      Events.On("busy", (ev) => setBusy(ev.data)),
    ];
    return () => offs.forEach((off) => off());
  }, [fail]);

  return (
    <div className="shell">
      <header className="top">
        <div className="brand"><Bag /> satchel</div>
        <nav className="tabs" role="tablist">
          {(["send", "receive", "devices", "settings"] as Tab[]).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
              {t === "send" ? `Send${shares.length ? ` · ${shares.length}` : ""}`
                : t === "receive" ? `Receive${receives.length ? ` · ${receives.length}` : ""}`
                : t === "devices" ? "Devices" : "⚙"}
            </button>
          ))}
        </nav>
      </header>
      <main>
        {tab === "send" && <Send shares={shares} busy={busy} fail={fail} />}
        {tab === "receive" && <Receive receives={receives} fail={fail} />}
        {tab === "devices" && <Devices peers={peers} pairings={pairings} fail={fail} />}
        {tab === "settings" && <Settings fail={fail} />}
      </main>
      {toast && <div className="toast" role="alert">{toast}</div>}
    </div>
  );
}

function Send({ shares, busy, fail }: { shares: Session[]; busy: string; fail: (e: unknown) => void }) {
  const [text, setText] = useState("");
  return (
    <section className="stack">
      <div className="actions">
        <button className="gl-btn primary" onClick={() => Service.ShareClipboard().catch(fail)}>Share clipboard</button>
        <button className="gl-btn ghost" onClick={() => Service.PickFiles(false).catch(fail)}>Files…</button>
        <button className="gl-btn ghost" onClick={() => Service.PickFiles(true).catch(fail)}>Folder…</button>
      </div>
      {/* Wails delivers drops only onto an element marked as a drop target. */}
      <div className="drop" data-file-drop-target>Drop files or folders here</div>
      <div className="row">
        <textarea value={text} onChange={(e) => setText(e.target.value)} rows={2} placeholder="…or type some text" />
        <button className="gl-btn ghost" disabled={!text.trim()} onClick={() => Service.ShareText(text).then(() => setText("")).catch(fail)}>Share</button>
      </div>
      {busy && <p className="gl-hint" role="status">{busy}</p>}
      {shares.length === 0 && <p className="gl-hint">Shares appear here with their phrase. The link is copied for you.</p>}
      {shares.map((s) => <ShareCard key={s.phrase} s={s} />)}
    </section>
  );
}

function ShareCard({ s }: { s: Session }) {
  const status = s.delivered > 0 ? `delivered ✓${s.delivered > 1 ? ` ×${s.delivered}` : ""}`
    : s.peer ? (s.peers ? `${s.peer} is deciding…` : `ringing ${s.peer}…`)
    : s.status === "waiting" ? "waiting for someone to join" : s.peers ? `${s.peers} connected` : s.status;
  return (
    <article className="card">
      {s.peer ? <p className="phrase to">→ {s.peer}</p> : <Phrase s={s} />}
      <p className="meta"><span>{s.title}</span><span>{s.items > 1 ? `${s.items} items · ` : ""}{size(s.bytes)}</span></p>
      <p className={"status " + (s.delivered > 0 ? "ok" : "")}>{status}</p>
      <div className="actions">
        <button className="gl-btn ghost" onClick={() => Service.Copy(s.link)}>Copy link</button>
        <button className="gl-btn ghost" onClick={() => Service.Copy(s.phrase)}>Copy phrase</button>
        <button className="gl-btn ghost stop" onClick={() => Service.Stop(s.phrase)}>Stop</button>
      </div>
    </article>
  );
}

// Phrase shows the code phrase large, with a QR code for a phone on demand.
function Phrase({ s }: { s: Session }) {
  const [qr, setQr] = useState("");
  return (
    <div className="phrase-row">
      <p className="phrase">{s.phrase}</p>
      <button className="gl-btn ghost qr-btn" aria-expanded={!!qr} onClick={() => qr ? setQr("") : Service.QR(s.link).then(setQr)}>QR</button>
      {qr && <img className="qr" src={qr} alt={"QR code for " + s.link} width={160} height={160} />}
    </div>
  );
}

function Receive({ receives, fail }: { receives: Session[]; fail: (e: unknown) => void }) {
  const [phrase, setPhrase] = useState("");
  const [joining, setJoining] = useState(false);
  const join = () => {
    if (!phrase.trim()) return;
    setJoining(true);
    Service.Receive(phrase).then(() => setPhrase("")).catch(fail).finally(() => setJoining(false));
  };
  return (
    <section className="stack">
      <div className="row">
        <input value={phrase} onChange={(e) => setPhrase(e.target.value)} onKeyDown={(e) => e.key === "Enter" && join()}
          placeholder="code phrase, like lion-42-maple" autoCapitalize="none" autoComplete="off" spellCheck={false} aria-label="Code phrase" />
        <button className="gl-btn primary" disabled={joining} onClick={join}>{joining ? "Joining…" : "Receive"}</button>
      </div>
      <button className="gl-btn ghost" onClick={() => Service.Open().catch(fail)}>New phrase for a phone to send to</button>
      {receives.length === 0 && <p className="gl-hint">Text you receive goes on your clipboard; files go to your satchel downloads folder.</p>}
      {receives.map((r) => (
        <article className="card" key={r.phrase}>
          <Phrase s={r} />
          {r.status === "waiting" && <p className="gl-hint">Open <b>{r.link}</b> on the other device, or scan the QR.</p>}
          {r.peer && <p className="gl-hint">from your paired device <b>{r.peer}</b></p>}
          {(r.offers ?? []).map((o) => <OfferRow key={o.id} o={o} phrase={r.phrase} fail={fail} />)}
          <div className="actions"><button className="gl-btn ghost stop" onClick={() => Service.Stop(r.phrase)}>Close</button></div>
        </article>
      ))}
    </section>
  );
}

function OfferRow({ o, phrase, fail }: { o: Offer; phrase: string; fail: (e: unknown) => void }) {
  const pct = o.bytes ? Math.min(100, Math.round((o.done / o.bytes) * 100)) : 100;
  return (
    <div className="offer">
      <p className="meta"><span className="name">{o.name}</span><span>{o.items > 1 ? `${o.items} items · ` : ""}{size(o.bytes)}</span></p>
      {o.status === "offered" && (
        <div className="actions">
          <button className="gl-btn primary" onClick={() => Service.Accept(phrase, o.from, o.id).catch(fail)}>Accept</button>
          <button className="gl-btn ghost" onClick={() => Service.Decline(phrase, o.from, o.id)}>Decline</button>
        </div>
      )}
      {o.status === "receiving" && <progress max={100} value={pct} aria-label={"receiving " + o.name} />}
      {o.status === "received" && o.text && (
        <div className="text-preview">
          <pre>{o.text}</pre>
          <button className="gl-btn ghost" onClick={() => Service.Copy(o.text!)}>Copy again</button>
        </div>
      )}
      <p className={"status " + (o.status === "received" ? "ok" : o.status === "failed" ? "bad" : "")}>
        {o.status === "received" ? (o.text !== undefined && o.text !== "" ? "on your clipboard ✓" : o.folder ? "saved ✓" : "received ✓")
          : o.status === "failed" ? "failed: " + o.error : o.status === "offered" ? "waiting for you" : o.status}
        {o.folder && <button className="linkish" onClick={() => Service.ShowFolder()}>Show in folder</button>}
      </p>
    </div>
  );
}

function Devices({ peers, pairings, fail }: { peers: Peer[]; pairings: Pairing[]; fail: (e: unknown) => void }) {
  const [phrase, setPhrase] = useState("");
  const [name, setName] = useState("");
  useEffect(() => { Service.Settings().then((s) => setName(s.deviceName)).catch(fail); }, [fail]);
  return (
    <section className="stack">
      <label className="field">This device is called
        <span className="row">
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={40} aria-label="Device name" />
          <button className="gl-btn ghost" onClick={() => Service.SetDeviceName(name).catch(fail)}>Save</button>
        </span>
      </label>

      <div className="actions">
        <button className="gl-btn primary" onClick={() => Service.PairStart().catch(fail)}>Pair a new device</button>
      </div>
      <div className="row">
        <input value={phrase} onChange={(e) => setPhrase(e.target.value)} placeholder="…or enter the phrase another device shows"
          autoCapitalize="none" autoComplete="off" spellCheck={false} aria-label="Pairing phrase"
          onKeyDown={(e) => e.key === "Enter" && phrase.trim() && Service.PairJoin(phrase).then(() => setPhrase("")).catch(fail)} />
        <button className="gl-btn ghost" disabled={!phrase.trim()} onClick={() => Service.PairJoin(phrase).then(() => setPhrase("")).catch(fail)}>Join</button>
      </div>

      {pairings.map((p) => (
        <article className="card" key={p.phrase}>
          <p className="phrase">{p.phrase}</p>
          {(p.candidates ?? []).length === 0
            ? <p className="gl-hint">Enter this phrase in satchel on the other device, under Devices.</p>
            : (p.candidates ?? []).map((c) => (
              <div className="offer" key={c.id}>
                <p className="meta"><span className="name">{c.name}</span></p>
                <p className="code">{c.code}</p>
                <p className="gl-hint">Pair only if the other screen shows the same code.</p>
                <div className="actions">
                  <button className="gl-btn primary" onClick={() => Service.PairConfirm(p.phrase, c.id).catch(fail)}>Codes match — pair</button>
                </div>
              </div>
            ))}
          <div className="actions"><button className="gl-btn ghost stop" onClick={() => Service.PairCancel(p.phrase)}>Cancel</button></div>
        </article>
      ))}

      <h3 className="sub">Paired devices</h3>
      {peers.length === 0 && <p className="gl-hint">None yet. Paired devices can send to each other by name — no phrase each time.</p>}
      {peers.map((p) => (
        <article className="card" key={p.id}>
          <p className="meta"><span className="name">{p.name}</span><span>paired {new Date(p.paired).toLocaleDateString()}</span></p>
          <div className="actions">
            <button className="gl-btn primary" onClick={() => Service.CallPeer(p.id).catch(fail)}>Call</button>
            <button className="gl-btn ghost" onClick={() => Service.ShareClipboardTo(p.id).catch(fail)}>Send clipboard</button>
            <button className="gl-btn ghost" onClick={() => Service.PickFilesTo(p.id, false).catch(fail)}>Send files…</button>
            <button className="gl-btn ghost stop" onClick={() => Service.Unpair(p.id).catch(fail)}>Unpair</button>
          </div>
        </article>
      ))}
    </section>
  );
}

function Settings({ fail }: { fail: (e: unknown) => void }) {
  const [dl, setDl] = useState("");
  const [relay, setRelay] = useState("");
  const [auto, setAuto] = useState(false);
  const [version, setVersion] = useState("");
  useEffect(() => {
    Service.Settings().then((s) => { setDl(s.downloads); setRelay(s.relay); }).catch(fail);
    Service.Autostart().then(setAuto).catch(fail);
    Service.Version().then(setVersion);
  }, [fail]);
  return (
    <section className="stack">
      <label className="field">Received files go to
        <span className="row"><code className="path">{dl}</code>
          <button className="gl-btn ghost" onClick={() => Service.PickDownloads().then(setDl).catch(fail)}>Change…</button></span>
        <span className="gl-hint">A new folder applies from the next restart.</span>
      </label>
      <label className="check">
        <input type="checkbox" checked={auto} onChange={(e) => Service.SetAutostart(e.target.checked).then(() => setAuto(e.target.checked)).catch(fail)} />
        Start satchel at login
      </label>
      <p className="gl-hint">Relay: <code>{relay}</code> — it sees only encrypted frames, never what you share or its name.</p>
      <p className="gl-hint">satchel {version} · <a href="https://github.com/richardwooding/satchel" target="_blank" rel="noreferrer">GitHub</a></p>
    </section>
  );
}

function Bag() {
  return (
    <svg viewBox="0 0 32 32" width="22" height="22" aria-hidden="true">
      <path d="M9 12.5a7 5 0 0 1 14 0" fill="none" stroke="var(--gl-accent-2)" strokeWidth="2.6" />
      <rect x="4.5" y="12" width="23" height="14.5" rx="3" fill="var(--gl-accent)" />
      <path d="M4.5 17.5V15a3 3 0 0 1 3-3h17a3 3 0 0 1 3 3v2.5z" fill="var(--gl-accent-2)" />
      <rect x="14" y="16" width="4" height="3.5" rx="1" fill="var(--gl-bg)" />
    </svg>
  );
}
