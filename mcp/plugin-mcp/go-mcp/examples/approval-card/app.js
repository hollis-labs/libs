// No tool calls, model-context updates, messages, logging or storage of form data.
// Only the MCP Apps lifecycle and incoming tool results cross postMessage.
(() => {
  const root = document.getElementById("card");
  const status = document.getElementById("status");
  let ready = false;
  let busy = false;
  let current;
  const send = data => window.parent.postMessage({jsonrpc: "2.0", ...data}, "*");
  const el = (tag, text) => {
    const e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    return e;
  };
  const input = (parent, label, type = "text") => {
    const wrapper = el("label", label);
    const e = el("input"); e.type = type; e.autocomplete = "off";
    wrapper.append(e); parent.append(wrapper); return e;
  };
  function target(raw, origin) {
    const base = new URL(origin);
    const u = new URL(raw, base);
    if (u.origin !== base.origin || u.username || u.password || u.hash ||
        (u.protocol !== "https:" && !(u.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname)))) {
      throw new Error("Untrusted endpoint");
    }
    return u;
  }
  function pending(card) {
    return card.status === "pending" && Date.parse(card.expires_at) > Date.now();
  }
  function render(card) {
    current = card;
    root.replaceChildren(); status.textContent = "";
    if (card.version !== "gatecrasher.card/v1" || !["approve","choose","answer","request_secret"].includes(card.type)) {
      status.textContent = "Unsupported card"; return;
    }
    root.append(el("h2", card.title), el("p", card.summary || ""),
      el("p", `${card.tier} · ${card.status} · expires ${card.expires_at}`));
    // Text only: Gatecrasher content never becomes HTML or script.
    for (const key of ["principal", "requested", "evidence", "outcome"]) {
      if (card[key]) root.append(el("pre", JSON.stringify(card[key], null, 2)));
    }
    if (!pending(card)) return;
    const form = el("form"); form.addEventListener("submit", e => e.preventDefault()); root.append(form);
    const credential = input(form, "Owner credential (client memory only)", "password");
    let field;
    if (card.type === "choose") {
      form.append(el("p", card.prompt?.question || ""));
      field = el("select");
      for (const c of card.prompt?.choices || []) {
        const option = el("option", c.label); option.value = c.id; field.append(option);
      }
      form.append(field);
    } else if (card.type === "answer") {
      field = input(form, card.prompt?.question || "Answer"); field.maxLength = 4000;
    } else if (card.type === "request_secret") {
      field = input(form, "Secret (sent directly to intake)", "password");
    }
    for (const action of card.actions || []) {
      const kinds = {approve:["approve","deny"], choose:["choose","deny"], answer:["answer","deny"], request_secret:["provide_secret","deny"]};
      if (!kinds[card.type].includes(action.kind)) continue;
      const button = el("button", action.label); button.type = "button"; form.append(button);
      button.addEventListener("click", async () => {
        if (busy || current !== card || !pending(card)) return;
        busy = true; button.disabled = true;
        try {
          let response;
          if (action.kind === "provide_secret") {
            if (!card.secret?.write_only || card.secret.field !== "value" || !(Date.parse(card.secret.expires_at) > Date.now()) || !field.value) throw new Error("Invalid intake");
            const url = target(card.secret.post_url, config.intakeOrigin);
            const body = new URLSearchParams({[card.secret.field]:field.value});
            field.value = "";
            // Never attach the Gatecrasher owner bearer to the intake. The intake
            // URL/auth policy is producer-owned. Never read/forward its response.
            response = await fetch(url, {method:"POST", body, credentials:"omit", redirect:"error", cache:"no-store", referrerPolicy:"no-referrer"});
          } else {
            if (!credential.value) throw new Error("Owner credential required");
            const url = target(action.decide, config.gateOrigin);
            const path = `/api/gate/v1/approvals/${encodeURIComponent(card.approval_id)}/decide`;
            if (url.pathname !== path || url.search) throw new Error("Invalid decide path");
            const body = {outcome:action.kind === "deny" ? "denied" : "approved"};
            if (action.outcome !== body.outcome) throw new Error("Invalid outcome");
            if (action.kind === "choose") {
              if (!card.prompt?.choices?.some(c => c.id === field.value)) throw new Error("Invalid choice");
              body.choice = field.value;
            }
            if (action.kind === "answer") { if (!field.value) throw new Error("Answer required"); body.answer = field.value; }
            response = await fetch(url, {method:"POST", headers:{"Content-Type":"application/json", "Authorization":`Bearer ${credential.value}`}, body:JSON.stringify(body), credentials:"omit", redirect:"error", cache:"no-store", referrerPolicy:"no-referrer"});
          }
          credential.value = "";
          if (!response.ok) {
            status.textContent = response.status === 409 ? "Card is terminal; refresh it." : response.status === 403 ? "Not authorized to decide." : "Request failed; refresh before retrying.";
          } else if (response.status === 202) {
            status.textContent = "Shadow decision recorded; owner decision still required.";
          } else {
            status.textContent = action.kind === "provide_secret" ? "Secret submitted directly. Refresh for the producer's confirmation." : "Decision submitted. Refresh the card.";
          }
          // Do not replay uncertain or shadow decisions. A new card is required.
          form.replaceChildren();
        } catch (_) {
          credential.value = ""; if (field) field.value = "";
          status.textContent = "Request could not be completed. Refresh before retrying.";
          form.replaceChildren();
        } finally { credential.value = ""; if (field) field.value = ""; busy = false; }
      });
    }
  }
  window.addEventListener("message", event => {
    if (event.source !== window.parent) return;
    const msg = event.data;
    if (msg?.jsonrpc !== "2.0") return;
    if (msg.id === 1 && msg.result && !ready) {
      if (msg.result.protocolVersion !== "2026-01-26") return;
      ready = true; send({method:"ui/notifications/initialized"});
    } else if (ready && msg.method === "ui/notifications/tool-result" && msg.params?.structuredContent?.card) {
      render(msg.params.structuredContent.card);
    } else if (msg.method === "ui/notifications/tool-cancelled") {
      current = undefined; root.replaceChildren();
    } else if (msg.method === "ui/resource-teardown" && msg.id !== undefined) {
      current = undefined; root.replaceChildren(); send({id:msg.id,result:{}});
    } else if (msg.method === "ping" && msg.id !== undefined) {
      send({id:msg.id,result:{}});
    }
  });
  send({id:1,method:"ui/initialize",params:{appInfo:{name:"Gatecrasher approval",version:"0.1.0"},appCapabilities:{},protocolVersion:"2026-01-26"}});
})();
