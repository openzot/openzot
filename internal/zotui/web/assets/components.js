(() => {
  const escapeHTML = (value) =>
    String(value ?? "").replace(
      /[&<>"']/g,
      (character) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[character],
    );

  const define = (name, Component) => {
    if (!customElements.get(name)) customElements.define(name, Component);
  };

  class ZotTopbar extends HTMLElement {
    static get observedAttributes() {
      return ["product", "state"];
    }

    connectedCallback() {
      this.render();
    }

    attributeChangedCallback() {
      if (this.isConnected) this.render();
    }

    render() {
      const product = this.getAttribute("product") || "ZOT";
      const brand = product.endsWith("_") ? product : `${product}_`;
      const state = this.getAttribute("state") || "FACTORY CONTROL / CONNECTED";
      this.innerHTML = `
        <header class="topbar">
          <div class="brand">
            <svg class="pion-mark zot-mark" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5h16L8 19h12"/><path class="inner" d="M7 8h9M11 12h7M8 16h7"/></svg>
            <span>${escapeHTML(brand)}</span>
          </div>
          <div class="topbar-spacer" aria-hidden="true"></div>
          <div class="system-state"><span class="state-dot"></span><span>${escapeHTML(state)}</span></div>
        </header>`;
    }
  }

  class ZotAmbient extends HTMLElement {
    connectedCallback() {
      if (this._connected) return;
      this._connected = true;
      this.innerHTML =
        '<canvas id="ambient" aria-hidden="true"></canvas><div class="scanline" aria-hidden="true"></div><div class="noise" aria-hidden="true"></div>';
      this.canvas = this.querySelector("canvas");
      this.context = this.canvas?.getContext("2d");
      if (!this.context) return;
      this.reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
      this.resize = this.resize.bind(this);
      this.draw = this.draw.bind(this);
      addEventListener("resize", this.resize);
      this.resize();
      this.draw();
    }

    disconnectedCallback() {
      removeEventListener("resize", this.resize);
      if (this.frame) cancelAnimationFrame(this.frame);
    }

    number(name, fallback) {
      const raw = this.getAttribute(name);
      if (raw === null || raw === "") return fallback;
      const value = Number(raw);
      return Number.isFinite(value) ? value : fallback;
    }

    resize() {
      const density = Math.min(devicePixelRatio || 1, 2);
      this.canvas.width = innerWidth * density;
      this.canvas.height = innerHeight * density;
      this.canvas.style.width = `${innerWidth}px`;
      this.canvas.style.height = `${innerHeight}px`;
      this.context.setTransform(density, 0, 0, density, 0, 0);
      const fixed = this.hasAttribute("fixed-count");
      const cap = this.number("count", 48);
      const spacing = this.number("spacing", 30);
      const count = fixed ? cap : Math.min(cap, Math.floor(innerWidth / spacing));
      const xStep = this.number("x-step", 149.7);
      const yStep = this.number("y-step", 83.2);
      const speed = this.number("speed", 0.08);
      const speedStep = this.number("speed-step", 0.04);
      const alpha = this.number("alpha", 0.024);
      const alphaStep = this.number("alpha-step", 0.008);
      this.motes = Array.from({ length: count }, (_, index) => ({
        x: (index * xStep) % innerWidth,
        y: (index * yStep) % innerHeight,
        speed: speed + (index % 5) * speedStep,
        alpha: alpha + (index % 7) * alphaStep,
        radius: 0.35 + (index % 4) * 0.18,
      }));
    }

    draw() {
      this.context.clearRect(0, 0, innerWidth, innerHeight);
      this.motes.forEach((mote) => {
        if (!this.reduceMotion) {
          mote.y -= mote.speed;
          mote.x += Math.sin((mote.y + mote.x) * 0.003) * 0.08;
          if (mote.y < -4) mote.y = innerHeight + 4;
        }
        this.context.fillStyle = `rgba(255,90,90,${mote.alpha})`;
        if (this.getAttribute("variant") === "soft") {
          this.context.beginPath();
          this.context.arc(mote.x, mote.y, mote.radius, 0, Math.PI * 2);
          this.context.fill();
        } else {
          this.context.fillRect(mote.x, mote.y, 1, 1);
        }
      });
      if (!this.reduceMotion) this.frame = requestAnimationFrame(this.draw);
    }
  }

  class ZotFooter extends HTMLElement {
    static get observedAttributes() {
      return ["keys", "section", "start", "version"];
    }

    connectedCallback() {
      this.render();
      this.startClock();
    }

    attributeChangedCallback() {
      if (!this.isConnected) return;
      this.render();
      this.startClock();
    }

    disconnectedCallback() {
      if (this.frame) cancelAnimationFrame(this.frame);
    }

    render() {
      const keys = (this.getAttribute("keys") || "")
        .split(",")
        .filter(Boolean)
        .map((item) => {
          const separator = item.indexOf(":");
          const key = separator < 0 ? item : item.slice(0, separator);
          const label = separator < 0 ? "" : item.slice(separator + 1);
          return `<span><i>${escapeHTML(key)}</i>${escapeHTML(label)}</span>`;
        })
        .join("");
      this.innerHTML = `
        <footer class="footer">
          <div>ZOT <b>${escapeHTML(this.getAttribute("version") || "v0.1.0")}</b> // ${escapeHTML(this.getAttribute("section") || "SOFTWARE FACTORY")}</div>
          <div class="footer-center" id="clock"></div>
          <div class="keys">${keys}</div>
        </footer>`;
    }

    startClock() {
      if (this.frame) cancelAnimationFrame(this.frame);
      this.clockStart = new Date(
        this.getAttribute("start") || "2026-08-10T08:46:55.000Z",
      ).getTime();
      this.realStart = performance.now();
      const tick = () => {
        const clock = this.querySelector("#clock");
        if (!clock) return;
        const now = new Date(this.clockStart + (performance.now() - this.realStart));
        const month = now
          .toLocaleString("en-GB", { month: "short", timeZone: "UTC" })
          .toUpperCase();
        clock.textContent = `${String(now.getUTCDate()).padStart(2, "0")} ${month} ${now.getUTCFullYear()} // ${now.toISOString().slice(11, 19)}.${String(now.getUTCMilliseconds()).padStart(3, "0")} UTC`;
        this.frame = requestAnimationFrame(tick);
      };
      tick();
    }
  }

  class ZotInstanceCard extends HTMLElement {
    set configuration(value) {
      this._configuration = value;
      this.render();
    }

    connectedCallback() {
      this.render();
    }

    render() {
      if (!this._configuration) return;
      const { instance, index, active, state, stateLabel } = this._configuration;
      const activeRun = instance.runs.find((run) =>
        ["running", "paused"].includes(run.state),
      );
      const recordLabel = `${instance.runs.length} ${instance.runs.length === 1 ? "record" : "records"}`;
      const progressLabel = activeRun
        ? `iteration ${activeRun.iteration}`
        : instance.schedule?.short || "manual";
      const scope = `${instance.environment} / ${instance.backend} / ${instance.model}`;
      this.innerHTML = `
        <button class="instance-card${active ? " active" : ""}" data-instance="${index}" aria-pressed="${active}">
          <div class="card-row"><span>${escapeHTML(instance.id)}</span><span class="status ${escapeHTML(state)}">${escapeHTML(stateLabel)}</span></div>
          <div class="instance-name">${escapeHTML(instance.name.toUpperCase())}</div>
          <div class="instance-meta"><span>${escapeHTML(recordLabel)}</span><span>${escapeHTML(progressLabel)}</span></div>
          <div class="instance-scope">${escapeHTML(scope)}</div>
        </button>`;
    }
  }

  class ZotRunRow extends HTMLElement {
    set configuration(value) {
      this._configuration = value;
      this.render();
    }

    connectedCallback() {
      this.render();
    }

    render() {
      if (!this._configuration) return;
      const { run, index, active, stateLabel } = this._configuration;
      this.innerHTML = `
        <button class="run-row${active ? " active" : ""}" data-run="${index}" aria-pressed="${active}">
          <div class="card-row"><span class="run-id">${escapeHTML(run.id)}</span><span class="status ${escapeHTML(run.state)}">${escapeHTML(stateLabel)}</span></div>
          <div class="run-task">${escapeHTML(run.task)}</div>
          <div class="run-info"><span>iteration ${escapeHTML(run.iteration)}</span><span>${escapeHTML(run.elapsed)}</span></div>
        </button>`;
    }
  }

  define("zot-topbar", ZotTopbar);
  define("zot-ambient", ZotAmbient);
  define("zot-footer", ZotFooter);
  define("zot-instance-card", ZotInstanceCard);
  define("zot-run-row", ZotRunRow);
})();
