/* The production viewer has no external runtime or network dependencies. */
(() => {
  "use strict";
  const payload = document.getElementById("report-data");
  const data = JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(payload.textContent.trim()), c => c.charCodeAt(0))));
  payload.remove();
  const run = data.current;
  const $ = (selector, root = document) => root.querySelector(selector);
  const el = (tag, text, cls) => {
    const n = document.createElement(tag);
    if (text !== undefined && text !== null) n.textContent = String(text);
    if (cls) n.className = cls;
    return n;
  };
  const clone = name => document.getElementById(name + "-component").content.firstElementChild.cloneNode(true);
  const number = n => n === null || n === undefined ? "Not recorded" : Number(n).toLocaleString("en-GB");
  const stack = (...nodes) => { const n = el("div", null, "stack"); nodes.filter(Boolean).forEach(x => n.append(x)); return n; };
  const short = id => id ? id.slice(0, 12) : "Unavailable";
  const button = (text, action, cls = "link") => { const n = el("button", text, cls); n.type = "button"; n.addEventListener("click", action); return n; };
  const badge = text => {
    const n = clone("badge"); n.textContent = text || "not recorded";
    if (["accepted", "supported", "complete", "deleted", "applied", "Newly supported"].includes(text)) n.classList.add("good");
    if (["partial", "inconclusive", "unsent", "unresolved", "running", "candidate"].includes(text)) n.classList.add("warn");
    if (["rejected", "refuted", "failed", "conflicted"].includes(text)) n.classList.add("bad");
    return n;
  };
  const notice = (text, warning = false) => el("div", text, "notice" + (warning ? " warning" : ""));
  function card(title, description, parent = $("#view")) {
    const n = clone("card"); $("h2", n).textContent = title; $(".muted", n).textContent = description;
    parent.append(n); return $(".card-body", n);
  }
  function metric(parent, label, value, description) {
    const n = clone("metric"); $(".metric-label", n).textContent = label; $("strong", n).textContent = value; $("small", n).textContent = description; parent.append(n);
  }
  function toast(text) { const n = $("#toast"); n.textContent = text; n.style.display = "block"; setTimeout(() => { n.style.display = "none"; }, 2200); }
  function code(title, text) {
    const n = clone("code"); $("h3", n).textContent = title; $("code", n).textContent = text || "Not recorded";
    $(".copy", n).addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(text || ""); toast("Copied"); }
      catch { const selection = getSelection(), range = document.createRange(); range.selectNodeContents($("code", n)); selection.removeAllRanges(); selection.addRange(range); toast("Text selected. Use your keyboard to copy."); }
    });
    return n;
  }
  function schema(before, after) { const n = clone("schema"); n.append(code("Declared request schema", before), code("Observed request schema", after)); return n; }
  function list(parent, values) { const n = el("ul"); (values || []).forEach(v => n.append(el("li", v))); parent.append(n); }
  function disclosures(parent, title, values) {
    if (!values || !values.length) return;
    const n = el("details"); n.append(el("summary", title + " (" + values.length + ")")); list(n, values); parent.append(n);
  }
  function operation(key) { const n = el("div", null, "operation-label mono"); n.textContent = key || "Authentication / unassociated request"; return n; }

  // All tables use the same filtering and bounded DOM pagination component.
  function table(parent, rows, columns, options = {}) {
    let page = 0, filtered = rows;
    const pageSize = 50;
    const n = clone("table");
    $("caption", n).textContent = options.title || "Recorded results";
    const header = $("thead tr", n), body = $("tbody", n);
    columns.forEach(c => { const h = el("th", c[0]); h.scope = "col"; header.append(h); });
    const previous = $(".previous", n), next = $(".next", n);
    function render() {
      body.replaceChildren();
      const start = page * pageSize;
      for (const row of filtered.slice(start, start + pageSize)) {
        const tr = el("tr");
        for (const [, renderCell] of columns) {
          const td = el("td"), value = renderCell(row);
          if (value instanceof Node) td.append(value); else td.textContent = value === null || value === undefined ? "—" : String(value);
          tr.append(td);
        }
        body.append(tr);
      }
      if (!filtered.length) { const td = el("td", "No matching records.", "empty"); td.colSpan = columns.length; const tr = el("tr"); tr.append(td); body.append(tr); }
      $(".table-count", n).textContent = filtered.length ? `${number(start + 1)}–${number(Math.min(start + pageSize, filtered.length))} of ${number(filtered.length)} records` : "0 records";
      previous.disabled = page === 0; next.disabled = start + pageSize >= filtered.length;
    }
    previous.addEventListener("click", () => { page--; render(); });
    next.addEventListener("click", () => { page++; render(); });
    if (options.filters !== false) {
      const filters = clone("filters"), ops = $(".operation", filters), statuses = $(".status", filters), search = $("input", filters);
      [...new Set(rows.map(r => r.operation).filter(Boolean))].sort().forEach(v => { const o = el("option", v); o.value = v; ops.append(o); });
      [...new Set(rows.map(r => r.statusLabel || r.outcome || r.status || r.state).filter(v => typeof v === "string" && v))].sort().forEach(v => { const o = el("option", v); o.value = v; statuses.append(o); });
      if (ops.options.length === 1) ops.parentElement.hidden = true;
      if (statuses.options.length === 1) statuses.parentElement.hidden = true;
      const searchable = rows.map(r => (options.search ? options.search(r) : Object.entries(r).filter(([k, v]) => typeof v !== "object" && !["json", "input", "baseline", "response", "headers"].includes(k)).map(([, v]) => String(v)).join(" ")).toLowerCase());
      function filter() {
        const query = search.value.trim().toLowerCase();
        filtered = rows.filter((r, i) => (!ops.value || r.operation === ops.value) && (!statuses.value || (r.statusLabel || r.outcome || r.status || r.state) === statuses.value) && (!query || searchable[i].includes(query)));
        page = 0; render();
      }
      ops.addEventListener("change", filter); statuses.addEventListener("change", filter); search.addEventListener("input", filter);
      parent.append(filters);
    }
    parent.append(n); render(); return n;
  }

  const history = [];
  function detail(title, kicker, build, remember = true) {
    if (remember) history.push({ title, kicker, build });
    $("#detail-title").textContent = title; $("#detail-kicker").textContent = kicker;
    const body = $("#detail-body"); body.replaceChildren(); build(body);
    $("#detail-back").hidden = history.length < 2;
    const dialog = $("#detail"); if (!dialog.open) dialog.showModal(); dialog.scrollTop = 0;
  }
  $("#detail-back").addEventListener("click", () => { history.pop(); const previous = history[history.length - 1]; detail(previous.title, previous.kicker, previous.build, false); });
  $("#detail-close").addEventListener("click", () => $("#detail").close());
  $("#detail").addEventListener("close", () => { history.length = 0; });
  const findRule = (id, r = run) => r.rules.find(x => x.id === id);
  const evidenceLink = (id, r = run, label) => button(label || short(id), () => showEvidence(id, r));
  const ruleLink = (id, r = run, label) => button(label || findRule(id, r)?.summary || id, () => showRule(id, r));
  function evidenceList(parent, ids, r = run) {
    table(parent, [...new Set(ids || [])].map(id => ({ id, ...(r.evidence[id] || { outcome: "unavailable" }) })), [
      ["Evidence", e => evidenceLink(e.id, r)], ["Phase", e => e.phase], ["Outcome", e => badge(e.sent === false ? "unsent" : e.outcome)], ["HTTP", e => e.status || "—"], ["Operation", e => operation(e.operation)]
    ], { filters: false, title: "Supporting evidence" });
  }
  function showEvidence(id, r = run) {
    const e = r.evidence[id];
    detail("Request evidence", (r === run ? "CURRENT" : "BASELINE") + " · " + id, parent => {
      if (!e) { parent.append(notice("This evidence reference has no recorded payload in this snapshot.", true)); return; }
      const n = clone("evidence"); parent.append(n);
      $(".evidence-meta", n).append(badge(e.sent ? e.outcome : "unsent"), badge(e.phase), badge(e.status ? "HTTP " + e.status : "No HTTP status"), operation(e.operation));
      $(".evidence-reason", n).textContent = [e.reason, e.started, "Elapsed: " + e.duration + " (may include authentication and pacing)"].filter(Boolean).join(" · ");
      if (e.origin) $(".evidence-reason", n).append(el("p", "Inherited from run " + e.origin.runId + " · spec " + (e.origin.spec.release || e.origin.spec.version || e.origin.spec.canonical) + ". This request was sent in that run."));
      const links = $(".evidence-links", n);
      if (e.control) links.append(evidenceLink(e.control, r, "Matched control →"));
      const experiment = r.experiments.find(x => x.observation === id || x.requests.some(qid => r.requests.find(q => q.id === qid)?.evidence === id));
      if (experiment) links.append(button("Experiment & lifecycle →", () => showExperiment(experiment, r)));
      if (e.beforeEvidence) links.append(evidenceLink(e.beforeEvidence, r, "Read before →"));
      if (e.afterEvidence) links.append(evidenceLink(e.afterEvidence, r, "Readback →"));
      $(".evidence-payloads", n).append(code("Request input · recorded and redacted", e.input), code("HTTP response body", e.response));
      const effects = $(".evidence-effects", n);
      if (e.completion) effects.append(code("Effective asynchronous completion", e.completion));
      if (e.before || e.after) { const pair = el("div", null, "two-columns"); pair.append(code("Stored state before", e.before), code("Stored state after", e.after)); effects.append(pair); }
      effects.append(code("Response headers", e.headers));
    });
  }
  function showRule(id, r = run) {
    const rule = findRule(id, r);
    detail("Behavioural finding", (r === run ? "CURRENT" : "BASELINE") + " · " + id, parent => {
      if (!rule) { parent.append(notice("This rule is unavailable in this snapshot.", true)); return; }
      parent.append(badge(rule.status), el("p", rule.summary), operation(rule.operation), el("p", "Fields: " + (rule.fields || []).join(", "), "mono"), el("p", rule.trials, "muted"));
      if (rule.implied) parent.append(notice("This OR relationship is already satisfied by an unconditional required field. It does not establish a dependency on its other field."));
      parent.append(code("Recorded rule · conditions, scope, and evidence", rule.json));
      evidenceList(parent, rule.evidence, r);
    });
  }
  function showExperiment(x, r = run) {
    detail("Experiment & lifecycle", x.id, parent => {
      const chips = el("div", null, "chips"); chips.append(badge(x.outcome), badge(x.phase), badge(x.provenance)); parent.append(chips, operation(x.operation), el("p", x.purpose));
      if (x.error) parent.append(notice(x.error, true));
      parent.append(el("p", "Changed fields: " + ((x.changed || []).join(", ") || "None recorded"), "muted"));
      if (x.provenance === "inherited") parent.append(notice("This completed experiment was inherited from a compatible earlier revision. Its requests are excluded from this run's HTTP totals."));
      else if (x.provenance !== "recorded") parent.append(notice("Legacy association uses recorded contexts and explicit resource bindings. Purpose and exact planning inputs were not recorded; the comparison uses the earliest accepted baseline."));
      const links = el("div", null, "actions");
      if (x.observation) links.append(evidenceLink(x.observation, r, "Main request →"));
      if (x.control) links.append(evidenceLink(x.control, r, "Matched control →"));
      parent.append(links);
      const pair = el("div", null, "two-columns"); pair.append(code("Comparison baseline", x.baseline), code("Experiment input", x.input)); parent.append(pair);
      if (x.group) {
        const group = r.experiments.filter(y => y.group === x.group && y.id !== x.id);
        if (group.length) { const n = card("Confirmation pair", "Repeated controls and trials in the same recorded group.", parent); experimentTable(n, group, r, false); }
      }
      const n = card("Associated HTTP requests", "Setup, reads, the trial, asynchronous completion, and cleanup where provenance is recorded.", parent);
      requestTable(n, r.requests.filter(q => x.requests.includes(q.id)), r, false);
    });
  }
  function showRequest(q, r = run) {
    if (q.evidence && r.evidence[q.evidence]) { showEvidence(q.evidence, r); return; }
    detail("HTTP dispatch", q.id, parent => {
      parent.append(badge(q.phase), el("p", q.method + " · " + q.started), notice("This dispatch has no recorded response payload or status. Authentication dispatches are counted without storing credential responses."));
      const x = r.experiments.find(x => x.id === q.experiment); if (x) parent.append(button("Experiment & lifecycle →", () => showExperiment(x, r)));
    });
  }
  function requestTable(parent, rows, r = run, filters = true) {
    table(parent, rows, [["Request", q => button(q.id, () => showRequest(q, r))], ["Phase", q => q.phase], ["Operation", q => operation(q.operation)], ["HTTP", q => q.status || "Not recorded"], ["Outcome", q => badge(q.outcome)], ["Experiment", q => {
      const x = r.experiments.find(x => x.id === q.experiment); return x ? button(short(x.id), () => showExperiment(x, r)) : "Unassociated";
    }]], { filters, title: "HTTP dispatch ledger" });
  }
  function experimentTable(parent, rows, r = run, filters = true) {
    table(parent, rows, [["Experiment", x => button(short(x.id), () => showExperiment(x, r))], ["Operation", x => operation(x.operation)], ["Purpose / phase", x => stack(el("span", x.purpose), el("small", x.phase, "muted"))], ["Changed fields", x => (x.changed || []).join(", ") || "—"], ["Outcome", x => badge(x.outcome)], ["HTTP", x => number(x.requests.length)]], { filters, title: "Experiments and lifecycle traffic", search: x => [x.id, x.operation, x.phase, x.purpose, x.outcome, ...(x.changed || [])].join(" ") });
  }
  function showField(op, field, r = run) {
    detail(field.id, op.key, parent => {
      parent.append(el("p", "Declared: " + field.declared, "muted"));
      const rules = r.rules.filter(rule => field.rules.includes(rule.id));
      table(parent, rules, [["Observed finding", rule => ruleLink(rule.id, r)], ["Status", rule => badge(rule.status)], ["Basis", rule => rule.trials]], { filters: false, title: "Field findings" });
      const related = r.operations.filter(o => (op.related || []).includes(o.key));
      if (related.length) {
        const n = card("Related create / update behaviour", "Operations are linked by recorded resource bindings. Shared names alone do not establish a relationship.", parent);
        for (const other of related) {
          const match = other.fields.find(f => f.id === field.id);
          if (match) n.append(button(other.key + " · " + match.id + " →", () => showField(other, match, r)));
        }
      }
      const samples = card("Observed value matrix", "Each outcome belongs to a complete request. Other fields and state can affect it; open the evidence before generalising a value’s acceptance.", parent);
      table(samples, field.samples, [["Value", s => el("span", s.value, "mono")], ["Outcome", s => badge(s.outcome)], ["Requests", s => button(number(s.evidence.length) + " evidence records", () => detail("Value evidence", field.id + " · " + s.value, p => evidenceList(p, s.evidence, r)))]], { title: "Observed field values" });
      parent.append(code("Declared field schema", field.schema));
    });
  }

  const phaseColours = ["#078879", "#20a88e", "#68b7a5", "#4389b2", "#729fc2", "#6767a5", "#9a85be", "#ca9c50", "#b56d52", "#b18eab", "#8296a5", "#556775"];
  function cost(parent, phases, label) {
    const n = clone("chart"), svg = $("svg", n), legend = $(".chart-legend", n);
    const entries = Object.entries(phases).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
    const total = entries.reduce((sum, [, count]) => sum + count, 0);
    $("figcaption", n).textContent = label + " · " + number(total) + " HTTP requests";
    svg.setAttribute("aria-label", entries.map(([name, count]) => name + ": " + count).join(", ") || "No HTTP requests");
    let left = 0;
    entries.forEach(([name, count], i) => {
      const colour = phaseColours[i % phaseColours.length], width = total ? count / total * 1000 : 0;
      const rect = document.createElementNS("http://www.w3.org/2000/svg", "rect");
      for (const [key, value] of Object.entries({ x: left, y: 0, width, height: 44, fill: colour })) rect.setAttribute(key, String(value));
      const title = document.createElementNS("http://www.w3.org/2000/svg", "title"); title.textContent = name + ": " + number(count); rect.append(title); svg.append(rect); left += width;
      const item = el("div", null, "legend-item"), swatch = el("span", null, "swatch"); swatch.style.background = colour;
      item.append(swatch, el("span", name), el("strong", number(count))); legend.append(item);
    });
    parent.append(n);
  }
  function overview() {
    const view = $("#view"), m = run.metrics, cards = el("div", null, "cards"); view.append(cards);
    metric(cards, "Operations inspected", number(m.selected) + " / " + number(m.inventory), "Unique method / path pairs in the source");
    metric(cards, "Discovery cases", number(m.discovery), "Distinct mutations recorded in coverage");
    metric(cards, "Confirmation requests", number(m.confirmations), "Repeated trials supporting findings");
    metric(cards, "Total HTTP requests", number(m.http), "Every dispatch, including auth and cleanup");
    metric(cards, "Supported findings", number(m.supported), "Rules with recorded supporting evidence");
    metric(cards, "Matched controls", number(m.controls), "Discovery and confirmation controls");
    metric(cards, "Unresolved schema changes", number(m.unresolved), "Corrections the exporter could not apply");
    metric(cards, "Resources cleaned", number(m.deleted) + " / " + number(m.resources), "Recorded resources with deletion confirmed");
    view.append(notice(run.state === "complete" ? "Complete for the selected operations and configured exploration scope. Unselected operations and untested values remain unverified." : "This snapshot is " + run.state + ". Its findings are usable evidence, but the coverage and cleanup gaps below still apply.", run.state !== "complete"));
    if (run.baseline) {
      const lineage = card("Incremental inspection", "Baseline run " + run.baseline.runId + " · " + number(run.baseline.inheritedObservations) + " inherited evidence records; HTTP totals count requests sent in this run.");
      lineage.append(notice(run.baseline.assumption));
      table(lineage, run.baseline.operations, [["Operation", x => operation(x.operation)], ["Spec change", x => x.change], ["Scheduled action", x => x.action], ["Reused cases", x => x.reusedCases], ["Reused trial pairs", x => x.reusedPairs], ["Reason", x => (x.reasons || []).join(" · ")]], { title: "Incremental schedule" });
    }
    const ops = card("Determination by operation", "Model convergence, interaction coverage, and complete input enumeration are separate claims.");
    table(ops, run.operations.map(o => ({ ...o, operation: o.key })), [["Operation", o => button(o.key, () => detail("Operation coverage", o.key, p => { p.append(badge(o.state), el("p", o.coverage)); disclosures(p, "Recorded reasons", o.reasons); p.append(schema(o.before, o.after)); }))], ["Determination", o => badge(o.state)], ["Model converged", o => o.modelConverged ? "Yes" : "No"], ["Interactions complete", o => o.interactionComplete ? "Yes" : "No"], ["All inputs enumerated", o => o.inputComplete ? "Yes" : "No"]], { filters: false, title: "Operation determinations" });
    const chart = card("Where the requests went", "Discovery cases are only one part of execution. Controls, repetitions, fixtures, reads, cleanup, and authentication add HTTP requests.");
    const controls = el("div", null, "actions"); chart.append(controls);
    const plot = el("div"); chart.append(plot);
    const draw = latest => { plot.replaceChildren(); cost(plot, latest ? run.latestPhases : run.phases, latest ? "Latest session since " + run.latestSession : "Cumulative run"); };
    controls.append(button("Cumulative run", () => draw(false), "quiet"), button("Latest session", () => draw(true), "quiet")); draw(false);
    const notes = card("Evidence limits", "The report distinguishes validation rejection, unsupported hypotheses, and inconclusive execution.");
    notes.append(el("p", "An expected 400-class validation rejection is evidence about the input contract. It is not automatically a failed test. HTTP requests without a recorded outcome remain inconclusive.", "muted"));
    disclosures(notes, "Recorded warnings", run.warnings);
    if (!run.warnings.length) notes.append(el("p", "No recorded warnings.", "muted"));
  }
  function fields() {
    const parent = card("Field behaviour matrix", "Open a field to inspect its rules, tested values, evidence, and related create / update behaviour.");
    const rows = run.operations.flatMap(op => op.fields.map(field => ({ operation: op.key, field: field.id, declared: field.declared, op, value: field })));
    table(parent, rows, [["Operation", x => operation(x.operation)], ["Field", x => button(x.field, () => showField(x.op, x.value))], ["Declared", x => x.declared], ["Observed", x => {
      const n = el("div", null, "field-summary"), rules = run.rules.filter(r => x.value.rules.includes(r.id) && r.status === "supported" && !r.implied);
      const seen = new Set();
      rules.forEach(r => { if (!seen.has(r.kind)) { seen.add(r.kind); n.append(ruleLink(r.id, run, r.kind)); } });
      if (!rules.length) n.append(el("span", "No supported finding", "muted")); return n;
    }], ["Value outcomes", x => number(x.value.samples.length)]], { title: "Field behaviour matrix", search: x => [x.operation, x.field, x.declared, ...run.rules.filter(r => x.value.rules.includes(r.id)).map(r => r.summary)].join(" ") });
    const rules = card("All recorded findings", "Includes supported, refuted, candidate, and implied rules. The status and evidence belong to this snapshot.");
    table(rules, run.rules, [["Finding", r => ruleLink(r.id)], ["Field(s)", r => el("span", (r.fields || []).join(", "), "mono")], ["Operation", r => operation(r.operation)], ["Status", r => badge(r.status)], ["Basis", r => r.trials]], { title: "All findings" });
  }
  function experiments() {
    const p = card("Discovery, controls & confirmations", "Each row is a logical experiment. Open it to see the associated HTTP requests; legacy planning purpose is explicitly unavailable.");
    experimentTable(p, run.experiments);
  }
  function requests() {
    const p = card("Complete HTTP ledger", "One row per actual dispatch. Authentication and unassociated traffic remain visible; unsent experiments do not increase this count.");
    requestTable(p, run.requests);
  }
  function changes() {
    const p = card("Corrections to the declared contract", "Applied changes come from the exporter. Unresolved changes retain their status and supporting evidence.");
    table(p, run.changes, [["Operation", x => operation(x.operation)], ["Schema location", x => el("span", x.field, "mono")], ["Change", x => x.description], ["Status", x => badge(x.status)], ["Evidence", x => button("Inspect change →", () => detail("Schema correction", x.operation, parent => {
      parent.append(badge(x.status), el("p", x.description), el("p", x.field, "mono"));
      if (x.rule) parent.append(ruleLink(x.rule));
      const op = run.operations.find(o => o.key === x.operation); if (op) parent.append(schema(op.before, op.after));
      evidenceList(parent, x.evidence);
    }))]], { title: "Schema corrections" });
    const schemas = card("Before / after request schemas", "Local references are resolved from the saved source and observed contract. Select an operation to inspect both schemas.");
    for (const op of run.operations) { const row = el("div", null, "status-line"); row.append(button(op.key + " →", () => detail("Before / after request schemas", op.key, parent => parent.append(schema(op.before, op.after))))); schemas.append(row); }
  }
  function dependencies() {
    const p = card("Field dependency graph", "Each gate is one complete logical rule. OR, AND, and conditional groups retain their meaning; the graph does not turn co-occurrence into causation.");
    const toggle = el("label", null, "check"), input = el("input"); input.type = "checkbox"; input.id = "show-implied";
    toggle.append(input, document.createTextNode("Show rules implied by unconditional requirements")); p.append(toggle);
    const graph = el("div"); p.append(graph);
    function draw() {
      graph.replaceChildren();
      const all = run.rules.filter(r => r.status === "supported" && r.relation), shown = all.filter(r => input.checked || !r.implied);
      if (!shown.length) graph.append(el("p", "No independent supported field dependencies were recorded in this snapshot.", "empty"));
      const hidden = all.length - shown.length;
      if (hidden) graph.append(el("p", number(hidden) + " redundant OR rules are collapsed because an unconditional required field already satisfies them. All remain available under Field findings.", "muted"));
      for (const rule of shown) {
        const n = clone("dependency"); $(".dependency-gate", n).textContent = rule.relation;
        const text = $(".dependency-rule", n); text.append(el("span", rule.summary), el("small", rule.operation + (rule.implied ? " · implied by a required field" : "")));
        $(".dependency-evidence", n).append(ruleLink(rule.id, run, "Evidence →")); graph.append(n);
      }
    }
    input.addEventListener("change", draw); draw();
  }
  function resources() {
    const p = card("Resource lifecycle & cleanup", "Resources recorded by the inspector and their latest cleanup state. Open creation evidence to inspect the request and associated lifecycle.");
    table(p, run.resources, [["Resource", x => el("span", short(x.id), "mono")], ["Created by", x => operation(x.operation)], ["State", x => badge(x.state)], ["Attempts", x => x.attempts], ["Cleanup notes", x => (x.errors || []).join(" · ") || "—"], ["Evidence", x => x.evidence ? evidenceLink(x.evidence, run, "Creation →") : "Unavailable"]], { title: "Resource cleanup" });
  }
  function comparison() {
    const comparison = data.comparison, previous = data.baseline;
    if (!comparison || !previous) return;
    const p = card("What changed between the snapshots", "Rules are compared by their meaning, not generated IDs. A revised finding is a change in recorded knowledge; it does not by itself prove the server changed.");
    const pair = el("div", null, "comparison-pair");
    for (const [name, r] of [["Baseline", previous], ["Current", run]]) {
      const n = el("div"); n.append(el("small", name.toUpperCase(), "eyebrow"), el("strong", r.id), badge(r.state), el("p", number(r.metrics.http) + " HTTP · " + number(r.metrics.supported) + " supported findings"), el("small", "Snapshot " + r.snapshot, "mono")); pair.append(n);
    }
    p.append(pair);
    if (comparison.sameSnapshot) p.append(notice("Both inputs identify the same journal snapshot."));
    if (comparison.contextDifferences.length) { p.append(notice("Comparison context differs. Consider these differences before attributing any result to a server behaviour change.", true)); list(p, comparison.contextDifferences); }
    else p.append(notice("No differences were found in the recorded comparison settings. Runtime credentials and unrecorded server state cannot be compared."));
    table(p, comparison.coverage, [["Operation", x => operation(x.operation)], ["Baseline", x => badge(x.before)], ["Current", x => badge(x.after)]], { filters: false, title: "Coverage comparison" });
    const findings = card("Finding changes", "Newly supported, revised, status changes, and rules no longer reported. Evidence-count increases alone are not semantic changes.");
    table(findings, comparison.findings, [["Operation / field", x => stack(operation(x.operation), el("span", x.field, "mono"))], ["Change", x => badge(x.status)], ["Baseline", x => x.beforeRule ? ruleLink(x.beforeRule, previous, x.before) : "Not reported"], ["Current", x => x.afterRule ? ruleLink(x.afterRule, run, x.after) : "Not reported"]], { title: "Finding changes" });
  }
  const sections = {
    overview: ["What the API revealed", "Recorded behaviour, tested boundaries, and the evidence behind each finding.", overview],
    fields: ["Field findings", "From declared field shapes to observed behaviour, with evidence at every step.", fields],
    experiments: ["Every experiment, in context", "Separate the hypothesis being tested from the requests needed to test it.", experiments],
    requests: ["The HTTP request ledger", "Account for every dispatch across the full run and its continuations.", requests],
    changes: ["From specification to observation", "Review the corrections that shape the contract your SDK builder will ingest.", changes],
    dependencies: ["How fields depend on each other", "Read conditional and compound rules without losing their logical structure.", dependencies],
    resources: ["Resource cleanup", "Follow the resources created during inspection through to deletion.", resources],
    comparison: ["Compare the evidence", "Understand how findings and coverage evolved between saved snapshots.", comparison]
  };
  function navigate() {
    const key = location.hash.slice(1) || "overview", section = sections[key] || sections.overview;
    $("#page-title").textContent = section[0]; $("#page-description").textContent = section[1];
    document.querySelectorAll("[data-view]").forEach(n => { if (n.dataset.view === key) n.setAttribute("aria-current", "page"); else n.removeAttribute("aria-current"); });
    $("#view").replaceChildren(); section[2]();
  }
  $("#run-state").append(badge(run.state)); $("#run-target").textContent = run.target;
  $("#run-date").textContent = "Started " + run.started;
  $("#snapshot").addEventListener("click", () => detail("Snapshot details", run.id, p => {
    if (run.specIdentity) p.append(code("Specification identity", JSON.stringify(run.specIdentity, null, 2)));
    if (run.baseline) p.append(code("Baseline identity", JSON.stringify({ runId: run.baseline.runId, journalHash: run.baseline.journalHash, spec: run.baseline.spec }, null, 2)));
    table(p, [["Run", run.id], ["Journal snapshot hash", run.snapshot], ["Source specification hash", run.specHash], ["Target", run.target], ["Started", run.started], ["Latest session", run.latestSession], ["Finished", run.finished], ["View format", data.version]].map(([name, value]) => ({ name, value })), [["Property", x => x.name], ["Recorded value", x => el("span", x.value, "mono")]], { filters: false, title: "Snapshot metadata" });
  }));
  let theme = "system";
  try { theme = localStorage.getItem("restapi-inspector-theme") || "system"; } catch { /* Storage is optional for file URLs. */ }
  function applyTheme() {
    if (theme === "system") delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = theme;
    $("#theme").textContent = "Theme: " + theme;
  }
  $("#theme").addEventListener("click", () => { theme = ["system", "light", "dark"][(["system", "light", "dark"].indexOf(theme) + 1) % 3]; applyTheme(); try { localStorage.setItem("restapi-inspector-theme", theme); } catch { /* The theme still applies without persistence. */ } });
  applyTheme(); $("#print").addEventListener("click", () => window.print());
  $("#download").disabled = !run.contract;
  $("#download").addEventListener("click", () => {
    const url = URL.createObjectURL(new Blob([run.contract], { type: "application/json" }));
    const a = el("a"); a.href = url; a.download = "observed-contract-with-the-facts.openapi.json"; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  window.addEventListener("hashchange", navigate); navigate();
})();
