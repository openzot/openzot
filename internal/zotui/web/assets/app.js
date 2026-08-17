const $ = (selector) => document.querySelector(selector);

let state = { choices: { repos: [], repositories: {}, environments: [], models: [], defaultMaxIterations: 1000000 }, workers: [] };
let selectedWorker = 0;
let selectedRun = 0;
let editingID = "";
let schedule = { cron: "", timezone: "", runtimeMinutes: 0 };
let poll;

async function request(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: options.body ? { "Content-Type": "application/json", ...options.headers } : options.headers,
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try { message = (await response.json()).error || message; } catch (_) {}
    throw new Error(message);
  }
  if (response.status === 204) return null;
  return response.headers.get("content-type")?.includes("application/json") ? response.json() : response.text();
}

function worker() { return state.workers[selectedWorker]; }
function run() { return worker()?.runs[selectedRun]; }
function activeRun(item = worker()) {
  return item?.runs.find((record) => ["scheduled", "running", "paused"].includes(record.status));
}
function stateClass(status) {
  return ({ succeeded: "done", failed: "error", scheduled: "provisioning" })[status] || status || "idle";
}
function stateLabel(status) {
  return ({ succeeded: "COMPLETE", failed: "FAILED", scheduled: "STARTING" })[status] || (status || "idle").toUpperCase();
}
function elapsed(record) {
  if (!record?.startedAt) return "00:00";
  const end = record.finishedAt ? new Date(record.finishedAt) : new Date();
  const seconds = Math.max(0, Math.floor((end - new Date(record.startedAt)) / 1000));
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}
function scheduleText(value) {
  if (!value?.cron) return "Manual start";
  return `${value.cron} · ${value.timezone} · ${value.runtimeMinutes}m runtime`;
}

async function refresh({ quiet = true } = {}) {
  try {
    const previousWorker = worker()?.id;
    const previousRun = run()?.id;
    state = await request("/api/state");
    if (previousWorker) {
      const index = state.workers.findIndex((item) => item.id === previousWorker);
      selectedWorker = index < 0 ? 0 : index;
    }
    const current = worker();
    if (previousRun && current) {
      const index = current.runs.findIndex((item) => item.id === previousRun);
      selectedRun = index < 0 ? 0 : index;
    } else {
      selectedRun = Math.min(selectedRun, Math.max(0, (current?.runs.length || 1) - 1));
    }
    render();
  } catch (error) {
    if (!quiet) showToast("COMMAND FAILED", error.message);
    $("zot-topbar").setAttribute("state", "FACTORY CONTROL / DISCONNECTED");
  }
}

function render() {
  $("zot-topbar").setAttribute("state", "FACTORY CONTROL / CONNECTED");
  renderWorkers();
  renderRuns();
  renderHeader();
  renderTelemetry();
  loadOutput();
}

function renderWorkers() {
  const list = $("#instance-list");
  list.replaceChildren();
  state.workers.forEach((item, index) => {
    const record = activeRun(item);
    const card = document.createElement("zot-instance-card");
    card.configuration = {
      instance: {
        ...item,
        backend: item.repo,
        runs: item.runs.map((entry) => ({ ...entry, state: stateClass(entry.status), iteration: entry.iteration })),
        schedule: { short: item.schedule?.cron || "manual" },
      },
      index,
      active: index === selectedWorker,
      state: stateClass(record?.status || "idle"),
      stateLabel: stateLabel(record?.status || "idle"),
    };
    card.querySelector("button").addEventListener("click", () => {
      selectedWorker = index;
      selectedRun = 0;
      render();
    });
    list.append(card);
  });
  const allRuns = state.workers.flatMap((item) => item.runs);
  $("#active-count").textContent = String(allRuns.filter((item) => item.status === "running").length).padStart(2, "0");
  $("#paused-count").textContent = String(allRuns.filter((item) => item.status === "paused").length).padStart(2, "0");
  $("#run-count").textContent = String(allRuns.length).padStart(2, "0");
}

function renderRuns() {
  const item = worker();
  const list = $("#run-list");
  list.replaceChildren();
  $("#run-list-count").textContent = `${item?.runs.length || 0} records`;
  if (!item?.runs.length) {
    list.innerHTML = `<div class="run-empty"><strong>${item?.schedule?.cron ? "Schedule armed" : "Worker not launched"}</strong><span>${item?.schedule?.cron ? scheduleText(item.schedule) : "Launch the worker to begin its mission."}</span></div>`;
    return;
  }
  item.runs.forEach((record, index) => {
    const row = document.createElement("zot-run-row");
    row.configuration = {
      run: { ...record, state: stateClass(record.status), task: record.mission, elapsed: elapsed(record) },
      index,
      active: index === selectedRun,
      stateLabel: stateLabel(record.status),
    };
    row.querySelector("button").addEventListener("click", () => { selectedRun = index; renderRuns(); renderTelemetry(); loadOutput(); });
    list.append(row);
  });
}

function renderHeader() {
  const item = worker();
  const current = activeRun(item);
  $("#instance-title").textContent = item ? item.name.toUpperCase() : "NO WORKERS";
  $("#instance-id").textContent = item?.id || "—";
  $("#header-kicker").textContent = item ? `${item.repo} / ${item.repository}` : "Create a worker to begin";
  $("#environment-value").textContent = item?.environment || "—";
  $("#backend-value").textContent = item ? scheduleText(item.schedule) : "—";
  $("#objective-value").textContent = item?.mission || "—";
  $("#edit-worker").disabled = !item;
  $("#run-start").disabled = !item || Boolean(current);
  $("#run-pause").disabled = !current;
  $("#run-stop").disabled = !current;
  $("#run-pause").textContent = current?.status === "paused" ? "Resume run" : "Pause run";
  $("#view-topology").disabled = !run();
}

function renderTelemetry() {
  const record = run();
  const status = record?.status || "idle";
  $("#metric-state").innerHTML = `${stateLabel(status)} <small>/ ITERATION ${record?.iteration || 0}</small>`;
  $("#metric-tool").innerHTML = `${(record?.tool || "WAITING").toUpperCase()} <small>/ ${escapeText(record?.action || "no active action")}</small>`;
  $("#metric-iterations").innerHTML = `${record?.iteration || 0} <small>/ ${record?.maxIterations || worker()?.maxIterations || 0}</small>`;
  $("#metric-elapsed").textContent = elapsed(record);
  const percent = record?.maxIterations ? Math.min(100, record.iteration / record.maxIterations * 100) : 0;
  $("#iteration-bar").style.setProperty("--value", `${percent}%`);
  $("#state-bar").style.setProperty("--value", status === "running" ? "60%" : status === "succeeded" ? "100%" : "12%");
}

async function loadOutput() {
  const record = run();
  $("#output-run-id").textContent = record?.id || "NO RUN SELECTED";
  $("#tail-state").textContent = record?.status === "running" ? "LIVE TAIL" : "RUN RECORD";
  if (!record) {
    $("#terminal").textContent = "Select or start a run to inspect its output.";
    return;
  }
  try {
    const output = await request(`/api/runs/${encodeURIComponent(record.id)}/output`);
    $("#terminal").textContent = output || record.error || "Run has not emitted output yet.";
    $("#terminal").scrollTop = $("#terminal").scrollHeight;
  } catch (error) {
    $("#terminal").textContent = error.message;
  }
}

function fillChoices(selectedRepo = "", selectedRepository = "") {
  $("#repo").innerHTML = state.choices.repos.map((value) => `<option value="${escapeAttribute(value)}">${escapeText(value)}</option>`).join("");
  if (selectedRepo) $("#repo").value = selectedRepo;
  fillRepositories(selectedRepository);
  $("#environment-grid").innerHTML = state.choices.environments.map((value, index) => `<button type="button" class="environment-option${index === 0 ? " active" : ""}" data-environment="${escapeAttribute(value)}"><span>CONFIGURED</span><strong>${escapeText(value.toUpperCase())}</strong><small>Reusable runtime<br/>from zotui config</small></button>`).join("");
  $("#model-grid").innerHTML = state.choices.models.map((value, index) => `<button type="button" class="choice${index === 0 ? " active" : ""}" data-model="${escapeAttribute(value)}" role="option" aria-selected="${index === 0}"><strong>${escapeText(value)}</strong><small>CONFIGURED MODEL</small></button>`).join("");
	$("#environment-label").textContent = (state.choices.environments[0] || "NO ENVIRONMENT").toUpperCase();
  bindChoiceButtons();
}

function fillRepositories(preferred = "") {
  const connection = $("#repo").value;
  const repositories = state.choices.repositories?.[connection] || [];
  const select = $("#repository");
  const manual = $("#repository-manual");
  const label = $("#repository-label");
  const hint = $("#repository-hint");

  if (repositories.length) {
    const options = repositories.length > 1 ? ['<option value="">Select a repository…</option>'] : [];
    options.push(...repositories.map((value) => `<option value="${escapeAttribute(value)}">${escapeText(value)}</option>`));
    if (preferred && !repositories.includes(preferred)) {
      options.push(`<option value="${escapeAttribute(preferred)}">${escapeText(preferred)} (current)</option>`);
    }
    select.innerHTML = options.join("");
    select.hidden = false;
    select.disabled = false;
    manual.hidden = true;
    manual.disabled = true;
    manual.value = "";
    label.htmlFor = "repository";
    select.value = preferred || (repositories.length === 1 ? repositories[0] : "");
    hint.textContent = repositories.length === 1
      ? `Selected automatically from ${connection}.`
      : `Choose one of ${repositories.length} repositories available through ${connection}.`;
    return;
  }

  select.innerHTML = "";
  select.hidden = true;
  select.disabled = true;
  manual.hidden = false;
  manual.disabled = !connection;
  manual.value = preferred;
  label.htmlFor = "repository-manual";
  hint.textContent = connection
    ? `No fixed repository list is configured for ${connection}; enter owner/name.`
    : "Choose a repo connection first.";
}

function repositoryValue() {
  return $("#repository").hidden ? $("#repository-manual").value : $("#repository").value;
}

function bindChoiceButtons() {
  document.querySelectorAll("[data-environment]").forEach((button) => button.addEventListener("click", () => {
    document.querySelectorAll("[data-environment]").forEach((item) => item.classList.toggle("active", item === button));
    $("#environment-label").textContent = button.dataset.environment.toUpperCase();
  }));
  document.querySelectorAll("[data-model]").forEach((button) => button.addEventListener("click", () => {
    document.querySelectorAll("[data-model]").forEach((item) => {
      item.classList.toggle("active", item === button);
      item.setAttribute("aria-selected", item === button);
    });
    $("#model").value = button.dataset.model;
  }));
}

function choose(selector, dataName, value) {
  const buttons = [...document.querySelectorAll(selector)];
  buttons.forEach((button) => {
    const active = button.dataset[dataName] === value;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", active);
  });
}

function openCreate() {
  editingID = "";
  fillChoices();
  $("#dialog-kicker").textContent = "Provision autonomous runtime";
  $("#dialog-title").textContent = "Create ZOT worker_";
  $("#create-submit").textContent = "Create worker";
  $("#instance-name").value = "";
  $("#objective").value = "";
  $("#max-iterations").value = String(state.choices.defaultMaxIterations ?? 1000000);
  $("#model").value = state.choices.models[0] || "";
  schedule = { cron: "", timezone: "UTC", runtimeMinutes: 90 };
  updateScheduleSummary();
  $("#create-dialog").showModal();
}

function openEdit() {
  const item = worker();
  if (!item) return;
  editingID = item.id;
  fillChoices(item.repo, item.repository);
  $("#dialog-kicker").textContent = `Update ${item.id} · history retained`;
  $("#dialog-title").textContent = "Edit ZOT worker_";
  $("#create-submit").textContent = "Save worker";
  $("#instance-name").value = item.name;
  $("#objective").value = item.mission;
  $("#max-iterations").value = item.maxIterations;
  $("#model").value = item.model;
  choose("[data-environment]", "environment", item.environment);
  choose("[data-model]", "model", item.model);
  schedule = { ...item.schedule };
  updateScheduleSummary();
  $("#create-dialog").showModal();
}

async function saveWorker(event) {
  event.preventDefault();
  const environment = document.querySelector("[data-environment].active")?.dataset.environment;
  const payload = {
    name: $("#instance-name").value,
    repo: $("#repo").value,
    repository: repositoryValue(),
    environment,
    model: $("#model").value,
    mission: $("#objective").value,
    maxIterations: Number($("#max-iterations").value),
    schedule,
  };
  try {
    await request(editingID ? `/api/workers/${editingID}` : "/api/workers", { method: editingID ? "PUT" : "POST", body: JSON.stringify(payload) });
    $("#create-dialog").close();
    showToast(editingID ? "WORKER UPDATED" : "WORKER REGISTERED", payload.name.toUpperCase());
    await refresh({ quiet: false });
  } catch (error) { showToast("SAVE FAILED", error.message); }
}

async function startRun() {
  if (!worker()) return;
  try {
    await request(`/api/workers/${worker().id}/runs`, { method: "POST" });
    selectedRun = 0;
    await refresh({ quiet: false });
  } catch (error) { showToast("LAUNCH FAILED", error.message); }
}

async function pauseResume() {
  const record = activeRun();
  if (!record) return;
  const action = record.status === "paused" ? "resume" : "pause";
  try {
    await request(`/api/runs/${record.id}/${action}`, { method: "POST" });
    await refresh({ quiet: false });
  } catch (error) { showToast(`${action.toUpperCase()} FAILED`, error.message); }
}

async function stopRun() {
  const record = activeRun();
  if (!record) return;
  try {
    await request(`/api/runs/${record.id}/stop`, { method: "POST" });
    await refresh({ quiet: false });
  } catch (error) { showToast("STOP FAILED", error.message); }
}

function updateScheduleSummary() {
  const configured = Boolean(schedule.cron);
  $("#schedule-label").textContent = configured ? "SCHEDULED" : "NO SCHEDULE";
  $("#schedule-cron").textContent = configured ? schedule.cron : "Not configured";
  $("#schedule-human").textContent = configured ? schedule.cron : "Starts manually";
  $("#schedule-meta").textContent = configured ? `${schedule.timezone} · ${schedule.runtimeMinutes} minute limit` : "Open to add a cron schedule";
  $("#open-schedule-dialog").querySelector("i").textContent = configured ? "EDIT ↗" : "ADD ↗";
}

function openSchedule() {
  $("#cron-expression").value = schedule.cron || "0 */4 * * *";
  $("#cron-timezone").value = schedule.timezone || "UTC";
  $("#cron-runtime").value = schedule.runtimeMinutes || 90;
  $("#schedule-clear").hidden = !schedule.cron;
	updateCronPreview();
  $("#schedule-dialog").showModal();
}

function updateCronPreview() {
	$("#cron-description").textContent = $("#cron-expression").value.trim() || "No expression";
	$("#cron-next").textContent = `${$("#cron-timezone").value} · ${$("#cron-runtime").value} minute runtime limit`;
}

function applySchedule() {
  schedule = { cron: $("#cron-expression").value.trim(), timezone: $("#cron-timezone").value, runtimeMinutes: Number($("#cron-runtime").value) };
  updateScheduleSummary();
  $("#schedule-dialog").close();
}

function showToast(label, message) {
  $("#toast small").textContent = label;
  $("#toast-result").textContent = message;
  $("#toast").classList.add("show");
  setTimeout(() => $("#toast").classList.remove("show"), 3600);
}

function escapeText(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);
}
function escapeAttribute(value) { return escapeText(value); }

$("#new-instance").addEventListener("click", openCreate);
$("#edit-worker").addEventListener("click", openEdit);
$("#repo").addEventListener("change", () => fillRepositories());
$("#create-form").addEventListener("submit", saveWorker);
$("#dialog-close").addEventListener("click", () => $("#create-dialog").close());
$("#dialog-cancel").addEventListener("click", () => $("#create-dialog").close());
$("#run-start").addEventListener("click", startRun);
$("#run-pause").addEventListener("click", pauseResume);
$("#run-stop").addEventListener("click", stopRun);
$("#view-topology").addEventListener("click", () => showToast("PR VIEW UNAVAILABLE", "Run metadata does not expose pull requests yet."));
$("#open-schedule-dialog").addEventListener("click", openSchedule);
$("#schedule-apply").addEventListener("click", applySchedule);
$("#schedule-clear").addEventListener("click", () => { schedule = { cron: "", timezone: "UTC", runtimeMinutes: 0 }; updateScheduleSummary(); $("#schedule-dialog").close(); });
$("#schedule-dialog-close").addEventListener("click", () => $("#schedule-dialog").close());
$("#schedule-cancel").addEventListener("click", () => $("#schedule-dialog").close());
$("#generate-cron").addEventListener("click", () => showToast("AI SCHEDULE UNAVAILABLE", "Enter the cron expression directly for now."));
$("#cron-expression").addEventListener("input", updateCronPreview);
$("#cron-timezone").addEventListener("change", updateCronPreview);
$("#cron-runtime").addEventListener("input", updateCronPreview);
$("#cron-direct-tab").addEventListener("click", () => {
	$("#cron-direct-panel").hidden = false; $("#cron-ai-panel").hidden = true;
	$("#cron-direct-tab").classList.add("active"); $("#cron-ai-tab").classList.remove("active");
	$("#cron-direct-tab").setAttribute("aria-selected", "true"); $("#cron-ai-tab").setAttribute("aria-selected", "false");
});
$("#cron-ai-tab").addEventListener("click", () => {
	$("#cron-direct-panel").hidden = true; $("#cron-ai-panel").hidden = false;
	$("#cron-ai-tab").classList.add("active"); $("#cron-direct-tab").classList.remove("active");
	$("#cron-ai-tab").setAttribute("aria-selected", "true"); $("#cron-direct-tab").setAttribute("aria-selected", "false");
});
$("#create-dialog").addEventListener("close", () => { if ($("#schedule-dialog").open) $("#schedule-dialog").close(); });
document.addEventListener("keydown", (event) => {
  if (event.key.toLowerCase() === "n" && !$("#create-dialog").open) openCreate();
  if (event.key.toLowerCase() === "r" && !$("#create-dialog").open) startRun();
  if (event.key === "Escape") { if ($("#schedule-dialog").open) $("#schedule-dialog").close(); else if ($("#create-dialog").open) $("#create-dialog").close(); }
});

await refresh({ quiet: false });
poll = setInterval(() => refresh(), 1500);
addEventListener("beforeunload", () => clearInterval(poll));
