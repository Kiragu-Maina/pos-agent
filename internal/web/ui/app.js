// POS sell screen behavior. ES2017 only (async/await is fine) so it runs on the
// older browsers found on Windows 7. No optional chaining or nullish coalescing.

(function () {
  "use strict";

  // ---------- State ----------
  var products = [];
  var cart = {};          // productId -> { product, qty }
  var settings = {};
  var filter = "";
  var nudgeDismissed = false;
  var pendingConfirm = null;

  // Payment entry, in whole shillings unless an exact amount is chosen.
  var payShillings = 0;
  var payExactCents = -1; // >= 0 when "Exact" was pressed
  var payMethod = "cash"; // "cash" or "mpesa"
  var lastSale = null;    // for reprint-on-failure

  var cartBackdrop = null;

  // Runtime mode. The same UI runs on the local agent and on the hosted cloud;
  // /api/config tells us which, and which features to show. Default to local with
  // everything on, so a server that predates the endpoint still works.
  var CONFIG = { mode: "local", features: {} };
  var authMode = "login"; // "login" | "signup" | "forgot" | "reset"
  var gateOpen = false;
  var resetToken = "";    // password-reset token from the email link
  var verifyDismissed = false;
  // The get-the-app banner stays dismissed across reloads once closed.
  var getAppDismissed = false;
  try { getAppDismissed = localStorage.getItem("getapp_dismissed") === "1"; } catch (e) {}
  function hosted() { return CONFIG.mode === "hosted"; }
  // feature is on unless config explicitly turned it off (undefined => on).
  function feature(name) { return CONFIG.features[name] !== false; }

  // ---------- Helpers ----------
  function $(id) { return document.getElementById(id); }
  function show(el) { if (el) el.classList.remove("is-hidden"); }
  function hide(el) { if (el) el.classList.add("is-hidden"); }

  function money(cents) {
    var neg = cents < 0;
    if (neg) cents = -cents;
    var sh = Math.floor(cents / 100);
    var ct = cents % 100;
    var s = String(sh).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    return (neg ? "-" : "") + "KSh " + s + "." + (ct < 10 ? "0" + ct : ct);
  }

  function toCents(value) {
    var n = parseFloat(value);
    if (isNaN(n) || n < 0) return 0;
    return Math.round(n * 100);
  }

  var toastTimer = null;
  function toast(msg) {
    var t = $("toast");
    t.textContent = msg;
    show(t);
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { hide(t); }, 3200);
  }

  async function api(path, opts) {
    var res = await fetch(path, opts);
    var data = {};
    try { data = await res.json(); } catch (e) { data = {}; }
    // On the hosted app a 401 means the session expired; send the user back to
    // the sign-in gate (but never loop on the auth calls themselves).
    if (res.status === 401 && hosted() && path.indexOf("/api/auth/") !== 0) {
      showAuthGate();
    }
    return { ok: res.ok, data: data, status: res.status };
  }

  // ---------- Views ----------
  function switchView(name) {
    ["sell", "items", "today", "sales", "setup"].forEach(function (v) {
      var el = $("view-" + v);
      if (v === name) show(el); else hide(el);
    });
    var tabs = document.querySelectorAll(".tab");
    for (var i = 0; i < tabs.length; i++) {
      tabs[i].classList.toggle("is-active", tabs[i].getAttribute("data-view") === name);
    }
    if (name === "items") renderItems();
    if (name === "today") loadToday();
    if (name === "sales") doSalesSearch();
    if (name === "setup") { ensureSetupPanel(); if (feature("deviceSync")) loadSyncStatus(); }
    if (name !== "sell") closeCartSheet();
  }

  // ---------- Setup sub-tabs ----------
  // Setup is laid out as a vertical rail of sections; only the selected panel is
  // shown. Feature-gating can hide a section (and its rail tab) in hosted mode,
  // so selection always falls back to the first section still visible.
  function selectSetupPanel(id) {
    var tabs = document.querySelectorAll(".setup-tab");
    for (var i = 0; i < tabs.length; i++) {
      var on = tabs[i].getAttribute("data-panel") === id;
      tabs[i].classList.toggle("is-active", on);
      if (on) tabs[i].setAttribute("aria-current", "true");
      else tabs[i].removeAttribute("aria-current");
    }
    var panels = document.querySelectorAll(".setup-panels > .panel");
    for (var j = 0; j < panels.length; j++) {
      panels[j].classList.toggle("is-current", panels[j].id === id);
    }
  }

  function ensureSetupPanel() {
    var active = document.querySelector(".setup-tab.is-active");
    if (active && !active.classList.contains("is-hidden")) return;
    var tabs = document.querySelectorAll(".setup-tab");
    for (var i = 0; i < tabs.length; i++) {
      if (!tabs[i].classList.contains("is-hidden")) {
        selectSetupPanel(tabs[i].getAttribute("data-panel"));
        return;
      }
    }
  }

  // toggleSetupSection hides or shows a Setup section together with its rail tab,
  // keeping the rail in step with which panels the current mode offers.
  function toggleSetupSection(panelId, on) {
    toggleEl(panelId, on);
    var tab = document.querySelector('.setup-tab[data-panel="' + panelId + '"]');
    if (tab) tab.classList.toggle("is-hidden", !on);
  }

  // Low stock threshold used for warnings on tracked items.
  var LOW_STOCK = 5;

  function stockStatus(p) {
    if (!p.trackStock) return { cls: "none", text: "Not tracked" };
    if (p.stock <= 0) return { cls: "out", text: "Out of stock" };
    if (p.stock <= LOW_STOCK) return { cls: "low", text: "Low: " + p.stock };
    return { cls: "ok", text: p.stock + " in stock" };
  }

  // ---------- Catalogue ----------
  async function loadProducts() {
    var r = await api("/api/products");
    products = (r.ok && r.data.products) ? r.data.products : [];
    renderGrid();
    renderItems();
  }

  function visibleProducts() {
    if (!filter) return products;
    var f = filter.toLowerCase();
    return products.filter(function (p) { return p.name.toLowerCase().indexOf(f) !== -1; });
  }

  function renderGrid() {
    var grid = $("productGrid");
    grid.innerHTML = "";

    var hasAny = products.length > 0;
    var list = visibleProducts();

    $("catalogueEmpty").classList.toggle("is-hidden", hasAny);
    $("searchEmpty").classList.toggle("is-hidden", !(hasAny && list.length === 0));
    grid.classList.toggle("is-hidden", !hasAny || list.length === 0);

    list.forEach(function (p) {
      var tile = document.createElement("button");
      tile.type = "button";
      tile.className = "tile";
      tile.setAttribute("role", "listitem");

      var name = document.createElement("span");
      name.className = "tile-name";
      name.textContent = p.name;

      var foot = document.createElement("div");
      foot.className = "tile-foot";
      var price = document.createElement("span");
      price.className = "tile-price num";
      price.textContent = money(p.priceCents);
      foot.appendChild(price);

      // Only surface a badge when stock is low or out, to keep tiles quiet.
      if (p.trackStock && p.stock <= 0) {
        foot.appendChild(badge("Out", "out"));
      } else if (p.trackStock && p.stock <= LOW_STOCK) {
        foot.appendChild(badge("Low", "low"));
      }

      tile.appendChild(name);
      tile.appendChild(foot);
      tile.addEventListener("click", function () {
        addToCart(p);
        tile.classList.remove("flash");
        // reflow to restart the animation
        void tile.offsetWidth;
        tile.classList.add("flash");
      });
      grid.appendChild(tile);
    });
  }

  function onSearch() {
    filter = $("search").value.trim();
    renderGrid();
  }

  function onSearchKey(e) {
    if (e.key !== "Enter") return;
    var list = visibleProducts();
    if (list.length > 0) {
      addToCart(list[0]);
      $("search").value = "";
      filter = "";
      renderGrid();
    }
  }

  // ---------- Cart ----------
  function addToCart(p) {
    if (cart[p.id]) cart[p.id].qty += 1;
    else cart[p.id] = { product: p, qty: 1 };
    renderCart();
  }

  function setQty(id, qty) {
    if (!cart[id]) return;
    if (qty <= 0) delete cart[id];
    else cart[id].qty = qty;
    renderCart();
  }

  function clearCart() {
    cart = {};
    renderCart();
  }

  function cartCount() {
    var n = 0;
    Object.keys(cart).forEach(function (id) { n += cart[id].qty; });
    return n;
  }

  // ---------- Tax (mirrors the server's ComputeTax exactly) ----------
  function roundHalfUp(n, d) { return Math.floor((n + Math.floor(d / 2)) / d); }

  function taxRateBps() {
    var n = parseInt(settings.tax_rate_bps, 10);
    return (isNaN(n) || n < 0) ? 0 : n;
  }
  function taxModeOf() { return settings.tax_mode || "none"; }
  function taxEnabled() { return taxModeOf() !== "none"; }

  function lineTaxCents(gross, taxable, rate, mode) {
    if (rate <= 0 || mode === "none" || !taxable) return 0;
    if (mode === "exclusive") return roundHalfUp(gross * rate, 10000);
    if (mode === "inclusive") return roundHalfUp(gross * rate, 10000 + rate);
    return 0;
  }

  // cartTotals breaks the bill down the same way the receipt prints it.
  function cartTotals() {
    var rate = taxRateBps(), mode = taxModeOf();
    var subtotal = 0, tax = 0;
    Object.keys(cart).forEach(function (id) {
      var e = cart[id];
      var gross = e.product.priceCents * e.qty;
      subtotal += gross;
      tax += lineTaxCents(gross, !!e.product.taxable, rate, mode);
    });
    var total = (mode === "exclusive" && rate > 0) ? subtotal + tax : subtotal;
    return { subtotal: subtotal, tax: tax, total: total, rate: rate, mode: mode };
  }

  // cartTotalCents is the amount to charge, tax included.
  function cartTotalCents() { return cartTotals().total; }

  // pct renders a rate in basis points as a trimmed percentage, 1600 -> "16%".
  function pct(rateBps) {
    var s = (rateBps / 100).toFixed(2).replace(/\.?0+$/, "");
    return s + "%";
  }

  // renderBill fills the subtotal and tax rows for the cart or payment sheet.
  // prefix is "cart" or "pay". With tax off it hides them, so the bill looks
  // exactly as it did before tax existed.
  function renderBill(prefix, t) {
    var subRow = $(prefix + "SubtotalRow");
    var taxRow = $(prefix + "TaxRow");
    var note = $(prefix + "InclNote"); // cart only
    if (note) hide(note);
    var on = t.tax > 0 && t.mode !== "none" && t.rate > 0;
    if (!on) { hide(subRow); hide(taxRow); return; }
    var label = "VAT (" + pct(t.rate) + ")";
    if (t.mode === "exclusive") {
      show(subRow); $(prefix + "Subtotal").textContent = money(t.subtotal);
      show(taxRow); $(prefix + "TaxLabel").textContent = label; $(prefix + "Tax").textContent = money(t.tax);
    } else { // inclusive: the total already contains the tax
      hide(subRow);
      show(taxRow); $(prefix + "TaxLabel").textContent = "Incl. " + label; $(prefix + "Tax").textContent = money(t.tax);
    }
  }

  function renderCart() {
    var list = $("cartList");
    list.innerHTML = "";
    var ids = Object.keys(cart);
    var empty = ids.length === 0;

    $("cartEmpty").classList.toggle("is-hidden", !empty);
    list.classList.toggle("is-hidden", empty);

    ids.forEach(function (id) {
      var entry = cart[id];
      var li = document.createElement("li");
      li.className = "cart-item";
      li.setAttribute("role", "listitem");

      var info = document.createElement("div");
      info.className = "cart-info";
      var nm = document.createElement("span");
      nm.className = "cart-name";
      nm.textContent = entry.product.name;
      var ln = document.createElement("span");
      ln.className = "cart-line num";
      ln.textContent = money(entry.product.priceCents) + "  x" + entry.qty +
        "  =  " + money(entry.product.priceCents * entry.qty);
      info.appendChild(nm);
      info.appendChild(ln);

      var stepper = document.createElement("div");
      stepper.className = "stepper";
      stepper.appendChild(stepButton("−", "Decrease " + entry.product.name, function () { setQty(id, entry.qty - 1); }));
      var count = document.createElement("span");
      count.className = "count num";
      count.textContent = entry.qty;
      stepper.appendChild(count);
      stepper.appendChild(stepButton("+", "Increase " + entry.product.name, function () { setQty(id, entry.qty + 1); }));

      li.appendChild(info);
      li.appendChild(stepper);
      list.appendChild(li);
    });

    var t = cartTotals();
    renderBill("cart", t);
    $("cartTotal").textContent = money(t.total);
    $("chargeBtn").disabled = empty;

    // Mobile cart bar
    var bar = $("cartBar");
    if (empty) {
      hide(bar);
      closeCartSheet();
    } else {
      show(bar);
      $("cartBarCount").textContent = cartCount() + (cartCount() === 1 ? " item" : " items");
      $("cartBarTotal").textContent = money(t.total);
    }
  }

  function stepButton(label, aria, onClick) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "step";
    b.textContent = label;
    b.setAttribute("aria-label", aria);
    b.addEventListener("click", onClick);
    return b;
  }

  // Mobile cart sheet
  function openCartSheet() {
    var cart = $("cart");
    if (!cartBackdrop) {
      cartBackdrop = document.createElement("div");
      cartBackdrop.className = "cart-backdrop";
      cartBackdrop.addEventListener("click", closeCartSheet);
    }
    document.body.appendChild(cartBackdrop);
    cart.classList.add("open");
  }

  function closeCartSheet() {
    var cart = $("cart");
    cart.classList.remove("open");
    if (cartBackdrop && cartBackdrop.parentNode) {
      cartBackdrop.parentNode.removeChild(cartBackdrop);
    }
  }

  // ---------- Payment ----------
  function openPayment() {
    if (cartTotalCents() <= 0) return;
    payShillings = 0;
    payExactCents = -1;
    $("mpesaRef").value = "";
    setPayMethod("cash");
    show($("payOverlay"));
  }

  // setPayMethod switches between cash (numeric entry + change) and M-Pesa
  // (exact amount, capture the transaction code).
  function setPayMethod(m) {
    payMethod = m;
    var opts = document.querySelectorAll("#payMethodSeg .seg-opt");
    for (var i = 0; i < opts.length; i++) {
      opts[i].classList.toggle("is-active", opts[i].getAttribute("data-method") === m);
    }
    var cash = (m === "cash");
    $("quickCash").classList.toggle("is-hidden", !cash);
    $("pad").classList.toggle("is-hidden", !cash);
    $("payReceivedRow").classList.toggle("is-hidden", !cash);
    $("payChangeRow").classList.toggle("is-hidden", !cash);
    $("mpesaRefField").classList.toggle("is-hidden", cash);
    if (cash) { payExactCents = -1; payShillings = 0; }
    renderPayment();
    if (!cash) $("mpesaRef").focus();
  }

  function paidCents() {
    if (payMethod === "mpesa") return cartTotalCents(); // paid in full via M-Pesa
    if (payExactCents >= 0) return payExactCents;
    return payShillings * 100;
  }

  function renderPayment() {
    var t = cartTotals();
    var total = t.total;
    var paid = paidCents();
    renderBill("pay", t);
    $("payTotal").textContent = money(total);
    $("payReceived").textContent = money(paid);
    var change = paid - total;
    $("payChange").textContent = money(change > 0 ? change : 0);
    $("completeSale").disabled = paid < total;
  }

  function padDigit(d) {
    payExactCents = -1;
    payShillings = payShillings * 10 + d;
    if (payShillings > 99999999) payShillings = 99999999;
    renderPayment();
  }
  function padZeros() {
    payExactCents = -1;
    payShillings = payShillings * 100;
    if (payShillings > 99999999) payShillings = 99999999;
    renderPayment();
  }
  function padBack() {
    payExactCents = -1;
    payShillings = Math.floor(payShillings / 10);
    renderPayment();
  }
  function quickCash(value) {
    payExactCents = -1;
    payShillings = value;
    renderPayment();
  }
  function quickExact() {
    payExactCents = cartTotalCents();
    renderPayment();
  }

  async function completeSale() {
    var total = cartTotalCents();
    var paid = paidCents();
    if (paid < total) return;

    var items = Object.keys(cart).map(function (id) {
      return { productId: id, qty: cart[id].qty };
    });
    var reference = (payMethod === "mpesa") ? $("mpesaRef").value.trim() : "";

    // Save the sale first, without printing, so the recorded sale never
    // depends on the printer. Printing is then a separate, visible step.
    $("completeSale").disabled = true;
    var r = await api("/api/sales", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        items: items,
        paidCents: paid,
        paymentMethod: payMethod,
        reference: reference,
        print: false
      })
    });
    $("completeSale").disabled = false;

    if (!r.ok) {
      toast(r.data.error || "Could not complete the sale.");
      return;
    }

    lastSale = r.data.sale;
    hide($("payOverlay"));
    closeCartSheet();
    clearCart();
    loadProducts(); // refresh stock counts and badges after the sale
    if (feature("deviceSync")) loadSyncStatus(); // reflect the queued sale on the pill
    openSuccess(r.data.sale);
    if (feature("printing")) {
      printReceipt(r.data.sale); // thermal printer: drives the print step
    } else {
      // Hosted browser app: offer a printable receipt instead of a thermal one.
      setPrintStep("ok", "Sale saved. Print or share the receipt below.");
      show($("printReceiptBtn"));
    }
  }

  // ---------- Success and the print step ----------
  function openSuccess(sale) {
    $("successBadge").classList.remove("failed");
    $("successTitle").textContent = "Sale complete";
    $("successChange").textContent = money(sale.changeCents);
    hide($("successRetry"));
    hide($("printSetup"));
    hide($("printReceiptBtn"));
    show($("successOverlay"));
    $("successNext").focus();
  }

  // setPrintStep shows the receipt's progress and result. state is one of
  // "working", "ok", "warn", or "failed".
  function setPrintStep(state, text) {
    $("printStep").className = "print-step " + state;
    $("printStepText").textContent = text;
  }

  // printReceipt prints the just saved sale and reports each outcome with an
  // action the cashier can actually take.
  async function printReceipt(sale) {
    if (!(settings.printer_addr || "")) {
      // Nothing to retry against: guide the user to set a printer up.
      setPrintStep("warn", "No printer set up yet. The sale is saved.");
      hide($("successRetry"));
      show($("printSetup"));
      return;
    }

    setPrintStep("working", "Printing receipt...");
    hide($("successRetry"));
    hide($("printSetup"));

    var r = await api("/api/reprint", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: sale.id })
    });

    if (r.ok) {
      setPrintStep("ok", "Receipt printed.");
      hide($("successRetry"));
      hide($("printSetup"));
    } else {
      setPrintStep("failed", r.data.error || "The receipt did not print. The printer may be off or disconnected.");
      show($("successRetry"));
      show($("printSetup"));
    }
  }

  async function retryPrint() {
    if (!lastSale) return;
    var btn = $("successRetry");
    btn.disabled = true;
    await printReceipt(lastSale);
    btn.disabled = false;
  }

  // ---------- Browser receipt (hosted web app, no thermal printer) ----------
  function escapeHTML(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  // receiptHTML renders a sale as a narrow, print-friendly HTML document. It is
  // written into a hidden iframe and printed with the browser's own dialog, so the
  // hosted app needs no printer and no server round-trip.
  function receiptHTML(sale) {
    var shop = settings.shop_name || "My Shop";
    var when = formatDateTime(sale.createdAt);
    var rows = (sale.items || []).map(function (it) {
      return '<tr><td>' + escapeHTML(it.name) + '</td>' +
        '<td class="q">' + it.qty + '</td>' +
        '<td class="r">' + money(it.priceCents * it.qty) + '</td></tr>';
    }).join("");

    var lines = '<div class="line"><span>Subtotal</span><span>' + money(sale.subtotalCents) + '</span></div>';
    if (sale.taxCents > 0) {
      var taxLabel = sale.taxMode === "inclusive" ? "VAT included" : "VAT";
      lines += '<div class="line"><span>' + taxLabel + '</span><span>' + money(sale.taxCents) + '</span></div>';
    }
    lines += '<div class="line total"><span>Total</span><span>' + money(sale.totalCents) + '</span></div>';
    var method = sale.paymentMethod === "mpesa" ? "M-Pesa" : "Cash";
    lines += '<div class="line"><span>Paid (' + method + ')</span><span>' + money(sale.paidCents) + '</span></div>';
    if (sale.changeCents > 0) {
      lines += '<div class="line"><span>Change</span><span>' + money(sale.changeCents) + '</span></div>';
    }
    if (sale.reference) {
      lines += '<div class="ref">Ref: ' + escapeHTML(sale.reference) + '</div>';
    }

    var logo = settings.has_logo === "yes"
      ? '<img class="logo" src="/api/logo?v=' + Date.now() + '" alt="">' : "";
    var header = settings.receipt_header ? '<div class="sub">' + escapeHTML(settings.receipt_header) + '</div>' : "";
    var footer = settings.receipt_footer || "Asante sana · Thank you";

    return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>Receipt</title><style>' +
      '* { margin:0; padding:0; box-sizing:border-box; }' +
      'body { font:13px/1.5 "Courier New",monospace; color:#000; padding:12px; width:300px; }' +
      '.logo { max-width:120px; max-height:64px; display:block; margin:0 auto 8px; }' +
      '.shop { text-align:center; font-size:16px; font-weight:bold; }' +
      '.sub, .when { text-align:center; font-size:11px; }' +
      '.when { margin-bottom:8px; }' +
      'hr { border:0; border-top:1px dashed #000; margin:8px 0; }' +
      'table { width:100%; border-collapse:collapse; }' +
      'td { padding:1px 0; vertical-align:top; }' +
      'td.q { width:28px; text-align:center; } td.r { width:88px; text-align:right; white-space:nowrap; }' +
      '.line { display:flex; justify-content:space-between; }' +
      '.line.total { font-weight:bold; font-size:15px; margin:4px 0; }' +
      '.ref { margin-top:6px; font-size:11px; }' +
      '.foot { text-align:center; margin-top:10px; white-space:pre-line; }' +
      '@media print { body { width:auto; } }' +
      '</style></head><body>' +
      logo + '<div class="shop">' + escapeHTML(shop) + '</div>' + header +
      '<div class="when">' + escapeHTML(when) + '</div><hr>' +
      '<table>' + rows + '</table><hr>' + lines + '<hr>' +
      '<div class="foot">' + escapeHTML(footer) + '</div>' +
      '</body></html>';
  }

  // printBrowserReceipt renders the receipt into a hidden iframe and opens the
  // browser print dialog. The iframe is removed once printing is dismissed.
  function printBrowserReceipt(sale) {
    if (!sale) return;
    var frame = document.createElement("iframe");
    frame.setAttribute("aria-hidden", "true");
    frame.style.position = "fixed";
    frame.style.width = "0"; frame.style.height = "0";
    frame.style.border = "0"; frame.style.right = "0"; frame.style.bottom = "0";
    document.body.appendChild(frame);
    var doc = frame.contentWindow.document;
    doc.open();
    doc.write(receiptHTML(sale));
    doc.close();
    var done = false;
    function cleanup() {
      if (done) return;
      done = true;
      setTimeout(function () { if (frame.parentNode) frame.parentNode.removeChild(frame); }, 500);
    }
    setTimeout(function () {
      try {
        frame.contentWindow.focus();
        frame.contentWindow.onafterprint = cleanup;
        frame.contentWindow.print();
      } catch (e) { /* printing unavailable; leave the sale recorded */ }
      // Fallback cleanup in case onafterprint never fires (some browsers).
      setTimeout(cleanup, 60000);
    }, 250);
  }

  // ---------- Items management (create, edit, delete) ----------
  var scanAddToCart = false;
  var editId = null;

  function scannerOn() { return feature("scanner") && settings.barcode_scanner === "on"; }

  function badge(text, cls) {
    var el = document.createElement("span");
    el.className = "tile-badge " + cls;
    el.textContent = text;
    return el;
  }

  function renderItems() {
    var list = $("itemsList");
    if (!list) return;
    list.innerHTML = "";
    $("itemsEmpty").classList.toggle("is-hidden", products.length > 0);

    products.forEach(function (p) {
      var li = document.createElement("li");
      li.className = "item-row";
      li.setAttribute("role", "listitem");

      var main = document.createElement("div");
      main.className = "item-main";
      var nm = document.createElement("span");
      nm.className = "item-name";
      nm.textContent = p.name;
      var sub = document.createElement("div");
      sub.className = "item-sub";
      var price = document.createElement("span");
      price.className = "item-price num";
      price.textContent = money(p.priceCents);
      var st = stockStatus(p);
      var pill = document.createElement("span");
      pill.className = "stock " + st.cls;
      pill.textContent = st.text;
      sub.appendChild(price);
      sub.appendChild(pill);
      main.appendChild(nm);
      main.appendChild(sub);

      var actions = document.createElement("div");
      actions.className = "item-actions";
      if (p.trackStock && feature("restock")) {
        actions.appendChild(rowButton("Add stock", function () { openRestock(p); }));
      }
      actions.appendChild(rowButton("Edit", function () { openEditItem(p); }));
      actions.appendChild(rowButton("Delete", function () { confirmDelete(p); }));

      li.appendChild(main);
      li.appendChild(actions);
      list.appendChild(li);
    });
  }

  function rowButton(label, onClick) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "btn-ghost";
    b.textContent = label;
    b.addEventListener("click", onClick);
    return b;
  }

  // fillItemForm prepares the Add/Edit sheet. Pass a product to edit, or null
  // to add a fresh item.
  function fillItemForm(p) {
    editId = p ? p.id : null;
    $("itemTitle").textContent = p ? "Edit item" : "Add item";
    $("itemName").value = p ? p.name : "";
    $("itemPrice").value = p ? (p.priceCents / 100) : "";
    var bc = p ? (p.barcode || "") : "";
    $("itemBarcode").value = bc;
    // Show the barcode field when the scanner is on, or the item already has one.
    $("itemBarcodeField").classList.toggle("is-hidden", !(scannerOn() || bc));
    // Taxable defaults to on for new items; the row only shows when tax is on.
    $("taxable").checked = p ? (p.taxable !== false) : true;
    $("taxableField").classList.toggle("is-hidden", !taxEnabled());
    var track = p ? !!p.trackStock : false;
    $("trackStock").checked = track;
    $("stockCount").value = (p && p.trackStock) ? p.stock : "";
    $("stockField").classList.toggle("is-hidden", !track);
    $("saveItem").textContent = p ? "Save changes" : "Save item";
  }

  function openItem(barcode, fromScan) {
    scanAddToCart = !!fromScan;
    fillItemForm(null);
    if (barcode) {
      $("itemBarcode").value = barcode;
      $("itemBarcodeField").classList.remove("is-hidden");
    }
    show($("itemOverlay"));
    $("itemName").focus();
  }

  function openEditItem(p) {
    scanAddToCart = false;
    fillItemForm(p);
    show($("itemOverlay"));
    $("itemName").focus();
  }

  // ---------- Restock ----------
  // Restocking adds a relative quantity (a merge-safe stock event), the right
  // way to record goods coming in; correcting an absolute count is done in Edit.
  var restockId = null;
  function openRestock(p) {
    restockId = p.id;
    $("restockTitle").textContent = "Add stock: " + p.name;
    $("restockCurrent").textContent = (p.stock || 0) + " in stock now";
    $("restockQty").value = "";
    show($("restockOverlay"));
    $("restockQty").focus();
  }

  async function saveRestock() {
    var qty = parseInt($("restockQty").value, 10);
    if (!qty || qty <= 0) { toast("Enter how many to add."); return; }
    var r = await api("/api/products/restock", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: restockId, qty: qty })
    });
    if (!r.ok) { toast(r.data.error || "Could not add stock."); return; }
    hide($("restockOverlay"));
    await loadProducts();
    loadSyncStatus();
    toast("Stock updated.");
  }

  function onTrackStockToggle() {
    $("stockField").classList.toggle("is-hidden", !$("trackStock").checked);
  }

  async function saveItem() {
    var name = $("itemName").value.trim();
    var cents = toCents($("itemPrice").value);
    if (!name || cents <= 0) {
      toast("Please enter a name and a price.");
      return;
    }
    var track = $("trackStock").checked;
    var stock = track ? Math.max(0, parseInt($("stockCount").value, 10) || 0) : 0;
    var payload = {
      name: name,
      priceCents: cents,
      barcode: $("itemBarcode").value.trim(),
      taxable: $("taxable").checked,
      trackStock: track,
      stock: stock
    };
    var url = "/api/products";
    if (editId) {
      url = "/api/products/update";
      payload.id = editId;
    }

    var wasEdit = !!editId;
    var wasScanAdd = scanAddToCart && !editId;
    var r = await api(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    if (!r.ok) {
      toast(r.data.error || "Could not save the item.");
      return;
    }
    hide($("itemOverlay"));
    await loadProducts();
    if (wasScanAdd && r.data.product) {
      addToCart(r.data.product);
      toast(r.data.product.name + " added to the sale.");
    } else {
      toast(wasEdit ? "Item updated." : "Item added.");
    }
    scanAddToCart = false;
    editId = null;
  }

  function closeConfirm() {
    hide($("confirmOverlay"));
    pendingConfirm = null;
  }

  // confirmAction shows the shared confirm dialog with a custom title, message,
  // and confirm-button label, running fn when the user confirms.
  function confirmAction(title, text, okLabel, fn) {
    $("confirmTitle").textContent = title;
    $("confirmText").textContent = text;
    $("confirmOk").textContent = okLabel;
    pendingConfirm = fn;
    show($("confirmOverlay"));
    $("confirmOk").focus();
  }

  function confirmDelete(p) {
    confirmAction("Delete item",
      "Remove " + p.name + " from your items? This does not affect past sales.",
      "Delete",
      function () { doDelete(p.id); });
  }

  async function doDelete(id) {
    hide($("confirmOverlay"));
    var r = await api("/api/products/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: id })
    });
    if (r.ok) {
      await loadProducts();
      toast("Item removed.");
    } else {
      toast(r.data.error || "Could not remove the item.");
    }
  }

  // ---------- Barcode scanner ----------
  // USB scanners emulate a keyboard: a fast burst of characters ending in Enter.
  // We buffer keystrokes that arrive close together and treat the result as a
  // scan. Only active on the sell view, only when opted in, and never while a
  // field is focused (so scanning into the barcode field still works normally).
  var scanBuf = "";
  var scanLast = 0;

  function onScanKey(e) {
    if (!scannerOn()) return;
    var tag = (document.activeElement && document.activeElement.tagName) || "";
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
    if ($("view-sell").classList.contains("is-hidden")) return;
    if (!$("payOverlay").classList.contains("is-hidden")) return;
    if (!$("itemOverlay").classList.contains("is-hidden")) return;

    var now = (window.performance && performance.now) ? performance.now() : new Date().getTime();
    if (now - scanLast > 80) scanBuf = "";
    scanLast = now;

    if (e.key === "Enter") {
      var code = scanBuf;
      scanBuf = "";
      if (code.length >= 3) handleScan(code);
      return;
    }
    if (e.key.length === 1) scanBuf += e.key;
  }

  function handleScan(code) {
    var match = null;
    for (var i = 0; i < products.length; i++) {
      if (products[i].barcode && products[i].barcode === code) { match = products[i]; break; }
    }
    if (match) {
      addToCart(match);
      toast(match.name + " added.");
    } else {
      toast("New item. Please add its details.");
      openItem(code, true);
    }
  }

  async function saveScanner() {
    var on = $("scannerToggle").checked;
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ barcode_scanner: on ? "on" : "off" })
    });
    if (r.ok) {
      settings.barcode_scanner = on ? "on" : "off";
      toast(on ? "Barcode scanner is on." : "Barcode scanner is off.");
    } else {
      $("scannerToggle").checked = !on;
      toast(r.data.error || "Could not save.");
    }
  }

  // ---------- Today (dashboard) ----------
  async function loadToday() {
    var analyticsReq = api("/api/analytics");
    var salesReq = api("/api/sales/today");

    var ar = await analyticsReq;
    if (ar.ok && ar.data) {
      renderStats(ar.data);
      renderWeekChart(ar.data.days || []);
      renderTopProducts(ar.data.topProducts || []);
      renderHoursChart(ar.data.hours || []);
    }

    var r = await salesReq;
    var sales = (r.ok && r.data.sales) ? r.data.sales : [];
    $("todayCount").textContent = sales.length + (sales.length === 1 ? " receipt" : " receipts");

    var list = $("salesList");
    list.innerHTML = "";
    $("salesEmpty").classList.toggle("is-hidden", sales.length > 0);

    sales.forEach(function (sale) {
      list.appendChild(buildSaleRow(sale, false));
    });
  }

  // buildSaleRow renders one sale. withDetail adds the date and the payment /
  // reference tags, used by the Sales search where rows span many days.
  function buildSaleRow(sale, withDetail) {
    var li = document.createElement("li");
    li.className = "sale-row";
    li.setAttribute("role", "listitem");

    var meta = document.createElement("div");
    meta.className = "sale-meta";
    var when = document.createElement("span");
    when.className = "sale-time";
    when.textContent = withDetail ? formatDateTime(sale.createdAt) : formatTime(sale.createdAt);
    var count = document.createElement("span");
    count.className = "muted";
    var n = itemCount(sale);
    count.textContent = n + (n === 1 ? " item" : " items");
    meta.appendChild(when);
    meta.appendChild(count);

    if (withDetail) {
      var tags = document.createElement("div");
      tags.className = "sale-tags";
      var isMpesa = sale.paymentMethod === "mpesa";
      var method = document.createElement("span");
      method.className = "sale-tag" + (isMpesa ? " is-mpesa" : "");
      method.textContent = isMpesa ? "M-Pesa" : "Cash";
      tags.appendChild(method);
      if (sale.reference) {
        var ref = document.createElement("span");
        ref.className = "sale-tag";
        ref.textContent = sale.reference;
        tags.appendChild(ref);
      }
      meta.appendChild(tags);
    }

    var amount = document.createElement("span");
    amount.className = "sale-amount num";
    amount.textContent = money(sale.totalCents);

    var reprint = document.createElement("button");
    reprint.type = "button";
    reprint.className = "btn-ghost";
    reprint.textContent = "Reprint";
    reprint.addEventListener("click", function () { reprintSale(sale.id, reprint); });

    li.appendChild(meta);
    li.appendChild(amount);
    li.appendChild(reprint);
    return li;
  }

  function itemCount(sale) {
    var n = 0;
    (sale.items || []).forEach(function (it) { n += it.qty; });
    return n;
  }

  // ---------- Sales search ----------
  async function doSalesSearch(e) {
    if (e && e.preventDefault) e.preventDefault();
    var body = {
      reference: $("qRef").value.trim(),
      itemName: $("qItem").value.trim(),
      paymentMethod: $("qMethod").value,
      start: $("qFrom").value,
      end: $("qTo").value,
      minCents: toCents($("qMin").value),
      maxCents: toCents($("qMax").value)
    };
    var btn = $("salesSearchBtn");
    btn.disabled = true;
    var r = await api("/api/sales/search", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    btn.disabled = false;
    if (!r.ok) { toast(r.data.error || "Could not run the search."); return; }
    renderSalesResults((r.data.sales || []), !!r.data.truncated);
  }

  function renderSalesResults(sales, truncated) {
    var list = $("salesResults");
    list.innerHTML = "";
    $("salesSearchEmpty").classList.toggle("is-hidden", sales.length > 0);
    $("salesResultCount").textContent = sales.length + (sales.length === 1 ? " sale" : " sales");
    $("salesTruncated").classList.toggle("is-hidden", !truncated);
    sales.forEach(function (sale) {
      list.appendChild(buildSaleRow(sale, true));
    });
  }

  function clearSalesSearch() {
    ["qRef", "qItem", "qMethod", "qFrom", "qTo", "qMin", "qMax"].forEach(function (id) {
      $(id).value = "";
    });
    doSalesSearch();
  }

  function formatDateTime(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return formatTime(iso);
    return d.toLocaleDateString([], { day: "2-digit", month: "short" }) + "  " + formatTime(iso);
  }

  // ---------- Dashboard rendering ----------
  function renderStats(a) {
    var today = a.today || {}, yest = a.yesterday || {};
    $("statSalesToday").textContent = money(today.totalCents || 0);
    var d = deltaText(today.totalCents || 0, yest.totalCents || 0);
    var de = $("statSalesDelta");
    de.className = "stat-delta " + d.cls;
    de.textContent = d.text;
    $("statReceipts").textContent = today.saleCount || 0;
    $("statReceiptsDelta").textContent = (yest.saleCount || 0) + " yesterday";
    $("statItems").textContent = today.itemCount || 0;
    $("statItemsDelta").textContent = (yest.itemCount || 0) + " yesterday";
  }

  // deltaText compares today against yesterday and returns a class and a plain
  // sentence. No dashes are used, to keep the front facing text clean.
  function deltaText(today, yest) {
    if (yest <= 0) {
      if (today > 0) return { cls: "is-up", text: "New since yesterday" };
      return { cls: "is-flat", text: "No sales yet" };
    }
    var pct = Math.round((today - yest) / yest * 100);
    if (pct > 0) return { cls: "is-up", text: "Up " + pct + "% vs yesterday" };
    if (pct < 0) return { cls: "is-down", text: "Down " + Math.abs(pct) + "% vs yesterday" };
    return { cls: "is-flat", text: "Same as yesterday" };
  }

  function renderWeekChart(days) {
    var el = $("weekChart");
    el.innerHTML = "";
    var max = 0, total = 0;
    days.forEach(function (d) { if (d.totalCents > max) max = d.totalCents; total += d.totalCents; });
    $("weekTotal").textContent = money(total) + " total";

    days.forEach(function (d, i) {
      var isToday = i === days.length - 1;
      var col = document.createElement("div");
      col.className = "bar-col";

      var val = document.createElement("span");
      val.className = "bar-val num";
      val.textContent = d.totalCents > 0 ? shortMoney(d.totalCents) : "";

      var grow = document.createElement("div");
      grow.className = "bar-grow";
      var bar = document.createElement("div");
      bar.className = d.totalCents === 0 ? "bar is-zero" : "bar";
      bar.style.height = (max > 0 ? Math.round(d.totalCents / max * 100) : 0) + "%";
      bar.title = (isToday ? "Today" : d.label) + ": " + money(d.totalCents);
      grow.appendChild(bar);

      var x = document.createElement("span");
      x.className = "bar-x";
      x.textContent = isToday ? "Today" : d.label;

      col.appendChild(val);
      col.appendChild(grow);
      col.appendChild(x);
      el.appendChild(col);
    });
  }

  function renderHoursChart(hours) {
    var chart = $("hoursChart"), empty = $("hoursEmpty");
    chart.innerHTML = "";
    var max = 0, first = -1, last = -1;
    hours.forEach(function (h) {
      if (h.totalCents > 0) { if (first < 0) first = h.hour; last = h.hour; }
      if (h.totalCents > max) max = h.totalCents;
    });
    if (max === 0) {
      chart.classList.add("is-hidden");
      empty.classList.remove("is-hidden");
      return;
    }
    chart.classList.remove("is-hidden");
    empty.classList.add("is-hidden");

    first = Math.max(0, first - 1);
    last = Math.min(23, last + 1);
    for (var hr = first; hr <= last; hr++) {
      var h = hours[hr];
      var col = document.createElement("div");
      col.className = "bar-col";

      var grow = document.createElement("div");
      grow.className = "bar-grow";
      var bar = document.createElement("div");
      bar.className = h.totalCents === 0 ? "bar is-zero" : "bar";
      bar.style.height = Math.round(h.totalCents / max * 100) + "%";
      bar.title = hourLabel(hr) + ": " + money(h.totalCents);
      grow.appendChild(bar);

      var x = document.createElement("span");
      x.className = "bar-x";
      x.textContent = (hr === first || hr === last || hr % 3 === 0) ? hourLabel(hr) : "";

      col.appendChild(grow);
      col.appendChild(x);
      chart.appendChild(col);
    }
  }

  function renderTopProducts(list) {
    var box = $("topProducts"), empty = $("topEmpty");
    box.innerHTML = "";
    if (!list || list.length === 0) {
      box.classList.add("is-hidden");
      empty.classList.remove("is-hidden");
      return;
    }
    box.classList.remove("is-hidden");
    empty.classList.add("is-hidden");
    var max = list[0].revenueCents || 1;

    list.forEach(function (p, i) {
      var row = document.createElement("div");
      row.className = "rank-row";

      var num = document.createElement("span");
      num.className = "rank-num";
      num.textContent = i + 1;

      var main = document.createElement("div");
      main.className = "rank-main";
      var name = document.createElement("div");
      name.className = "rank-name";
      name.textContent = p.name;
      var track = document.createElement("div");
      track.className = "rank-track";
      var fill = document.createElement("div");
      fill.className = "rank-fill";
      fill.style.width = Math.max(4, Math.round(p.revenueCents / max * 100)) + "%";
      track.appendChild(fill);
      main.appendChild(name);
      main.appendChild(track);

      var side = document.createElement("div");
      side.className = "rank-side";
      var amt = document.createElement("div");
      amt.className = "rank-amount num";
      amt.textContent = money(p.revenueCents);
      var qty = document.createElement("div");
      qty.className = "rank-qty num";
      qty.textContent = p.qty + " sold";
      side.appendChild(amt);
      side.appendChild(qty);

      row.appendChild(num);
      row.appendChild(main);
      row.appendChild(side);
      box.appendChild(row);
    });
  }

  // shortMoney gives a compact shilling figure for chart labels, for example
  // "3.2k" or "850".
  function shortMoney(cents) {
    var sh = Math.round(cents / 100);
    if (sh >= 1000) {
      var k = sh / 1000;
      return (k >= 10 ? Math.round(k) : Math.round(k * 10) / 10) + "k";
    }
    return String(sh);
  }

  function hourLabel(h) {
    var ampm = h >= 12 ? "pm" : "am";
    var hh = h % 12; if (hh === 0) hh = 12;
    return hh + ampm;
  }

  function formatTime(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    var h = d.getHours();
    var m = d.getMinutes();
    var ampm = h >= 12 ? "PM" : "AM";
    h = h % 12; if (h === 0) h = 12;
    return h + ":" + (m < 10 ? "0" + m : m) + " " + ampm;
  }

  async function reprintSale(id, btn) {
    if (!(settings.printer_addr || "")) {
      toast("No printer set up yet. Open Setup to add one.");
      return;
    }
    btn.disabled = true;
    var original = btn.textContent;
    btn.textContent = "Printing...";
    var r = await api("/api/reprint", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: id })
    });
    btn.disabled = false;
    btn.textContent = original;
    toast(r.ok ? "Receipt reprinted." : (r.data.error || "Could not reach the printer. Please check it is on."));
  }

  // ---------- Setup ----------
  async function loadSettings() {
    var r = await api("/api/settings");
    settings = (r.ok && r.data.settings) ? r.data.settings : {};
    $("shopName").textContent = settings.shop_name || "My Shop";
    $("shopNameInput").value = settings.shop_name || "";
    if (settings.paper_width) $("paperWidth").value = settings.paper_width;
    $("scannerToggle").checked = settings.barcode_scanner === "on";
    setActiveTheme(settings.receipt_theme || "classic");
    $("headerLine").value = settings.receipt_header || "";
    $("footerText").value = (settings.receipt_footer !== undefined && settings.receipt_footer !== null)
      ? settings.receipt_footer : "";
    var mode = settings.tax_mode || "none";
    $("taxMode").value = mode;
    var bps = taxRateBps();
    $("taxRate").value = bps ? (bps / 100) : "";
    $("taxRateField").classList.toggle("is-hidden", mode === "none");
    updateTaxHint();
    loadPreview();
    refreshLogo();
    updatePrinterState();
  }

  // ---------- Tax settings ----------
  function updateTaxHint() {
    var mode = $("taxMode").value;
    var hint = "";
    if (mode === "exclusive") hint = "Tax is added on top of your prices when you charge.";
    else if (mode === "inclusive") hint = "Your prices already include tax; the receipt shows how much.";
    $("taxHint").textContent = hint;
  }

  function onTaxModeChange() {
    var mode = $("taxMode").value;
    $("taxRateField").classList.toggle("is-hidden", mode === "none");
    updateTaxHint();
  }

  async function saveTax() {
    var mode = $("taxMode").value;
    var bps = 0;
    if (mode !== "none") {
      var rate = parseFloat($("taxRate").value);
      if (isNaN(rate) || rate < 0) { toast("Please enter a valid tax rate."); return; }
      bps = Math.round(rate * 100);
    }
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tax_mode: mode, tax_rate_bps: String(bps) })
    });
    if (!r.ok) { toast(r.data.error || "Could not save tax settings."); return; }
    settings.tax_mode = mode;
    settings.tax_rate_bps = String(bps);
    $("taxRateField").classList.toggle("is-hidden", mode === "none");
    renderCart();  // the cart breakdown reflects the new tax at once
    loadPreview(); // and so does the Setup receipt preview
    toast("Tax settings saved.");
  }

  // ---------- Logo ----------
  function refreshLogo() {
    var has = settings.has_logo === "yes";
    var url = "/api/logo?v=" + Date.now();
    var brandLogo = $("brandLogo"), brandMark = $("brandMark");
    var thumb = $("logoThumb"), preview = $("previewLogo"), remove = $("logoRemove");
    if (has) {
      brandLogo.src = url; brandLogo.classList.remove("is-hidden");
      brandMark.classList.add("is-hidden");
      thumb.src = url; thumb.classList.remove("is-hidden");
      preview.src = url; preview.classList.remove("is-hidden");
      remove.classList.remove("is-hidden");
    } else {
      brandLogo.classList.add("is-hidden");
      brandMark.classList.remove("is-hidden");
      thumb.classList.add("is-hidden");
      preview.classList.add("is-hidden");
      remove.classList.add("is-hidden");
    }
  }

  async function uploadLogo() {
    var input = $("logoInput");
    var file = input.files && input.files[0];
    if (!file) return;
    if (file.size > 2 * 1024 * 1024) {
      toast("That image is too big. Please use one under 2 MB.");
      input.value = "";
      return;
    }
    var res = await fetch("/api/logo", {
      method: "POST",
      headers: { "Content-Type": file.type || "application/octet-stream" },
      body: file
    });
    var data = {};
    try { data = await res.json(); } catch (e) { data = {}; }
    input.value = "";
    if (!res.ok) {
      toast(data.error || "Could not upload the logo.");
      return;
    }
    settings.has_logo = "yes";
    refreshLogo();
    toast("Logo saved.");
  }

  async function removeLogo() {
    var r = await api("/api/logo/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    });
    if (r.ok) {
      settings.has_logo = "no";
      refreshLogo();
      toast("Logo removed.");
    } else {
      toast(r.data.error || "Could not remove the logo.");
    }
  }

  // ---------- Cloud sync ----------
  // ---------- Records export ----------
  // downloadExport triggers a download (CSV, Excel, or PDF) for the chosen date
  // range. The server sends the file as an attachment, so clicking a hidden link
  // downloads it without navigating away from the app.
  function downloadExport(kind) {
    var from = $("exportFrom").value, to = $("exportTo").value;
    var format = $("exportFormat").value || "csv";
    var qs = ["format=" + encodeURIComponent(format)];
    if (from) qs.push("from=" + encodeURIComponent(from));
    if (to) qs.push("to=" + encodeURIComponent(to));
    var url = "/api/" + kind + "/export?" + qs.join("&");
    var a = document.createElement("a");
    a.href = url;
    a.download = "";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  async function loadSyncStatus() {
    var r = await api("/api/sync/status");
    if (r.ok) {
      renderSync(r.data || {});
      renderSyncPill(r.data || {});
    }
  }

  // renderSyncPill drives the always-visible indicator on the top bar. It is
  // shown only when the device is linked, so a purely local shop never sees it.
  function renderSyncPill(d) {
    var pill = $("syncPill");
    if (!pill) return;
    if (!d || !d.available || !d.linked) { hide(pill); return; }
    show(pill);
    var pending = (d.pendingProducts || 0) + (d.pendingSales || 0) + (d.pendingSettings ? 1 : 0);
    var cls = "sync-pill", text = "Synced";
    if (d.syncing) { cls += " syncing"; text = "Syncing"; }
    else if (!d.online) { cls += " offline"; text = "Offline"; }
    else if (d.lastError) { cls += " pending"; text = "Sync issue"; }
    else if (pending > 0) { cls += " pending"; text = pending + " to sync"; }
    else { cls += " ok"; text = "Synced"; }
    pill.className = cls;
    $("syncPillText").textContent = text;
    pill.title = "Cloud sync: " + text + (d.email ? "  ·  " + d.email : "");
  }

  function renderSync(d) {
    var panel = $("syncPanel");
    if (!d || !d.available) { hide(panel); return; }
    show(panel);
    var linked = !!d.linked;
    $("syncLinkForm").classList.toggle("is-hidden", linked);
    $("syncLinked").classList.toggle("is-hidden", !linked);
    if (!linked) {
      return;
    }
    $("syncLinkedText").textContent = "Linked as " + (d.email || "this shop");
    var pending = (d.pendingProducts || 0) + (d.pendingSales || 0);
    var bits = [];
    if (d.lastSync && d.lastSync.indexOf("0001") !== 0) bits.push("Last synced " + formatDateTime(d.lastSync));
    bits.push(pending > 0 ? (pending + (pending === 1 ? " change waiting" : " changes waiting")) : "Up to date");
    $("syncMeta").textContent = bits.join("  ·  ");
    var err = $("syncError");
    if (d.lastError) { err.textContent = d.lastError; show(err); } else hide(err);
    $("syncStateLine").className = "sync-state " + (d.lastError ? "warn" : "ok");
  }

  async function linkDevice() {
    var btn = $("syncLink");
    btn.disabled = true;
    var r = await api("/api/sync/link", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        email: $("syncEmail").value.trim(),
        password: $("syncPassword").value
      })
    });
    btn.disabled = false;
    if (!r.ok) { $("syncStatus").textContent = r.data.error || "Could not link this device."; return; }
    $("syncPassword").value = "";
    $("syncStatus").textContent = "";
    renderSync(r.data);
    // A first sync may have pulled the shop's catalogue and settings down.
    await loadSettings();
    await loadProducts();
    toast("This device is linked and synced.");
  }

  async function syncNow() {
    var btn = $("syncNow");
    btn.disabled = true;
    btn.textContent = "Syncing...";
    var r = await api("/api/sync/now", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
    btn.disabled = false;
    btn.textContent = "Sync now";
    if (!r.ok) {
      $("syncStatus").textContent = r.data.error || "Sync did not complete.";
      await loadSyncStatus();
      return;
    }
    $("syncStatus").textContent = "";
    renderSync(r.data);
    await loadProducts();
    toast("Synced.");
  }

  function unlinkDevice() {
    confirmAction("Unlink this device?",
      "Your data stays on this device and keeps working. It will stop syncing until you link again.",
      "Unlink",
      async function () {
        closeConfirm();
        var r = await api("/api/sync/unlink", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
        if (r.ok) { renderSync(r.data); toast("This device is no longer linked."); }
      });
  }

  // ---------- Receipt theme + preview ----------
  function currentTheme() {
    var active = document.querySelector(".theme-opt.is-active");
    return active ? active.getAttribute("data-theme") : (settings.receipt_theme || "classic");
  }

  function setActiveTheme(theme) {
    var opts = document.querySelectorAll(".theme-opt");
    for (var i = 0; i < opts.length; i++) {
      opts[i].classList.toggle("is-active", opts[i].getAttribute("data-theme") === theme);
    }
  }

  async function loadPreview() {
    var params = "theme=" + encodeURIComponent(currentTheme()) +
      "&header=" + encodeURIComponent($("headerLine").value) +
      "&footer=" + encodeURIComponent($("footerText").value);
    var r = await api("/api/receipt/preview?" + params);
    if (r.ok) $("receiptPreview").textContent = r.data.text || "";
  }

  // Save the editable header line and ending message.
  async function saveReceiptText() {
    var payload = {
      receipt_header: $("headerLine").value,
      receipt_footer: $("footerText").value
    };
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    if (r.ok) {
      settings.receipt_header = payload.receipt_header;
      settings.receipt_footer = payload.receipt_footer;
    }
  }

  var previewTimer = null;
  function livePreview() {
    if (previewTimer) clearTimeout(previewTimer);
    previewTimer = setTimeout(loadPreview, 200);
  }

  async function chooseTheme(theme) {
    setActiveTheme(theme);
    await loadPreview();
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ receipt_theme: theme })
    });
    if (r.ok) {
      settings.receipt_theme = theme;
      toast("Receipt style saved.");
    }
  }

  async function printSample() {
    var btn = $("printSample");
    btn.disabled = true;
    var r = await api("/api/receipt/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ theme: currentTheme() })
    });
    btn.disabled = false;
    toast(r.ok ? "Sample sent to the printer." : (r.data.error || "Could not print the sample."));
  }

  function updatePrinterState() {
    if (!feature("printing")) { hide($("printerNudge")); return; }
    var addr = settings.printer_addr || "";
    var ready = !!addr;
    $("currentPrinter").classList.toggle("ready", ready);
    $("currentPrinterText").textContent = ready ? "Printer is set up and ready." : "No printer chosen yet.";

    // Show which printer is in use, so "ready" has visible proof behind it.
    $("printerAddrText").textContent = addr;
    $("printerAddrLine").classList.toggle("is-hidden", !ready);

    // The find prompt and a primary Find button only make sense when nothing is
    // set up yet. Once a printer is saved, finding becomes a secondary action
    // and a test and remove appear instead.
    $("printerHelp").classList.toggle("is-hidden", ready);
    var find = $("find");
    find.textContent = ready ? "Find a different printer" : "Find my printer";
    find.classList.toggle("btn-primary", !ready);
    find.classList.toggle("btn-secondary", ready);
    $("printerTest").classList.toggle("is-hidden", !ready);
    $("printerForget").classList.toggle("is-hidden", !ready);

    $("printerNudge").classList.toggle("is-hidden", ready || nudgeDismissed);
  }

  // forgetPrinter clears the saved printer so the shop is back to a clean,
  // honest "no printer yet" state.
  async function forgetPrinter() {
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ printer_addr: "" })
    });
    if (r.ok) {
      $("results").innerHTML = "";
      $("setupStatus").textContent = "";
      await loadSettings();
      toast("Printer removed.");
    } else {
      toast(r.data.error || "Could not remove the printer.");
    }
  }

  async function saveShop() {
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        shop_name: $("shopNameInput").value.trim() || "My Shop",
        paper_width: $("paperWidth").value
      })
    });
    if (r.ok) { await loadSettings(); toast("Shop details saved."); }
    else toast(r.data.error || "Could not save.");
  }

  function addrOf(device) {
    var port = device.ports[0];
    for (var i = 0; i < device.ports.length; i++) {
      if (device.ports[i] === 9100) { port = 9100; break; }
    }
    return device.ip + ":" + port;
  }

  async function findPrinters() {
    var btn = $("find");
    btn.disabled = true;
    btn.classList.add("loading");
    btn.textContent = "Searching...";
    $("results").innerHTML = "";
    $("setupStatus").textContent = "Looking for your printer...";
    var r = await api("/api/scan");
    btn.disabled = false;
    btn.classList.remove("loading");
    btn.textContent = "Find my printer";
    if (!r.ok) {
      $("setupStatus").textContent = r.data.error || "Something went wrong. Please try again.";
      return;
    }
    renderDevices(r.data.devices || []);
  }

  function renderDevices(devices) {
    var list = $("results");
    list.innerHTML = "";
    if (devices.length === 0) {
      $("setupStatus").textContent = "We could not find a printer. Please check it is on and connected, then try again.";
      return;
    }
    $("setupStatus").textContent = devices.length === 1 ? "We found your printer." : "We found a few printers.";

    devices.forEach(function (device) {
      var addr = addrOf(device);
      var li = document.createElement("li");
      li.className = "result";
      li.setAttribute("role", "listitem");

      var label = document.createElement("span");
      label.className = "result-label";
      label.textContent = device.likely ? "Receipt printer found" : "Possible printer found";

      var actions = document.createElement("div");
      actions.className = "result-actions";

      var testBtn = document.createElement("button");
      testBtn.type = "button";
      testBtn.className = "btn-ghost";
      testBtn.textContent = "Test print";
      testBtn.addEventListener("click", function () { testPrint(addr, testBtn); });

      var useBtn = document.createElement("button");
      useBtn.type = "button";
      useBtn.className = "btn-secondary";
      useBtn.textContent = "Use this printer";
      useBtn.addEventListener("click", function () { usePrinter(addr); });

      actions.appendChild(testBtn);
      actions.appendChild(useBtn);
      li.appendChild(label);
      li.appendChild(actions);
      list.appendChild(li);
    });
  }

  async function testPrint(addr, btn) {
    btn.disabled = true;
    var original = btn.textContent;
    btn.textContent = "Printing...";
    var r = await api("/api/test-print", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ addr: addr })
    });
    btn.disabled = false;
    btn.textContent = original;
    $("setupStatus").textContent = r.ok
      ? "A test receipt was sent. Check your printer for paper."
      : (r.data.error || "Could not reach the printer.");
  }

  async function usePrinter(addr) {
    var r = await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ printer_addr: addr })
    });
    if (r.ok) { await loadSettings(); toast("Printer saved. You are ready to sell."); }
    else toast(r.data.error || "Could not save the printer.");
  }

  // ---------- Global keyboard ----------
  function onKeydown(e) {
    // Escape closes the top-most overlay or the cart sheet.
    if (e.key === "Escape") {
      if (!$("confirmOverlay").classList.contains("is-hidden")) { closeConfirm(); return; }
      if (!$("itemOverlay").classList.contains("is-hidden")) { hide($("itemOverlay")); return; }
      if (!$("successOverlay").classList.contains("is-hidden")) { hide($("successOverlay")); return; }
      if (!$("payOverlay").classList.contains("is-hidden")) { hide($("payOverlay")); return; }
      if ($("cart").classList.contains("open")) { closeCartSheet(); return; }
      return;
    }
    // Payment number entry via physical keyboard. For M-Pesa, let keys fall
    // through to the reference input; only Enter completes the sale.
    if (!$("payOverlay").classList.contains("is-hidden")) {
      if (payMethod !== "cash") {
        if (e.key === "Enter") { completeSale(); e.preventDefault(); }
        return;
      }
      if (e.key >= "0" && e.key <= "9") { padDigit(parseInt(e.key, 10)); e.preventDefault(); }
      else if (e.key === "Backspace") { padBack(); e.preventDefault(); }
      else if (e.key === "Enter") { completeSale(); e.preventDefault(); }
    }
  }

  // ---------- Hosted mode: capabilities, auth gate, account ----------

  // applyMode shows or hides the parts of the UI that only make sense on the
  // local agent (printer, scanner, device sync) or that the cloud does not serve
  // yet (search, restock, exports). The same UI build then serves both.
  function applyMode() {
    document.body.classList.toggle("hosted", hosted());
    toggleEl("printerNudge", feature("printing"));
    toggleSetupSection("printerPanel", feature("printing"));
    toggleSetupSection("receiptStylePanel", feature("printing"));
    toggleSetupSection("scannerPanel", feature("scanner"));
    toggleSetupSection("syncPanel", feature("deviceSync"));
    toggleSetupSection("recordsPanel", feature("exports"));
    ensureSetupPanel();
    // The Sales search tab and its view.
    var salesTab = document.querySelector('.tab[data-view="sales"]');
    if (salesTab) salesTab.classList.toggle("is-hidden", !feature("salesSearch"));
    // Account menu only on the hosted app.
    toggleEl("accountMenu", hosted());
    updateGetApp();
  }

  // updateGetApp shows the bottom "get the desktop app" banner only on the hosted
  // app, only when the server actually offers a download, and only until the user
  // dismisses it. CONFIG.download is a map of platform -> URL from /api/config.
  function updateGetApp() {
    var url = CONFIG.download && CONFIG.download.windows;
    var on = hosted() && !!url && !getAppDismissed;
    if (on) $("getAppLink").setAttribute("href", url);
    toggleEl("getAppBanner", on);
    document.body.classList.toggle("has-getapp", on);
  }

  function toggleEl(id, on) {
    var el = $(id);
    if (el) el.classList.toggle("is-hidden", !on);
  }

  function showAuthGate(mode) {
    gateOpen = true;
    setAuthMode(mode || "login");
    show($("authGate"));
    var first = mode === "reset" ? $("authPassword") : $("authEmail");
    if (first) first.focus();
  }

  function hideAuthGate() {
    gateOpen = false;
    hide($("authGate"));
  }

  function showAuthError(msg) {
    var e = $("authError");
    e.textContent = msg;
    e.classList.remove("is-hidden");
  }

  function postBody(obj) {
    return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(obj) };
  }

  // setAuthMode reshapes the one gate form for sign in, sign up, forgot-password
  // (email only) and reset-password (new password only).
  function setAuthMode(mode) {
    authMode = mode;
    $("authError").classList.add("is-hidden");
    $("authInfo").classList.add("is-hidden");
    var isSignup = mode === "signup", isForgot = mode === "forgot", isReset = mode === "reset", isLogin = mode === "login";

    $("authEmailField").classList.toggle("is-hidden", isReset);
    $("authPasswordField").classList.toggle("is-hidden", isForgot);
    $("authForgotRow").classList.toggle("is-hidden", !isLogin);

    var title = "Sign in to your shop", sub = "Welcome back. Sign in to start selling.",
        submit = "Sign in", pwLabel = "Password", pwAuto = "current-password";
    if (isSignup) {
      title = "Create your shop"; sub = "Set up an account to start selling in your browser.";
      submit = "Create account"; pwAuto = "new-password";
    } else if (isForgot) {
      title = "Reset your password"; sub = "Enter your email and we will send a reset link.";
      submit = "Send reset link";
    } else if (isReset) {
      title = "Choose a new password"; sub = "Enter a new password for your account.";
      submit = "Save new password"; pwLabel = "New password"; pwAuto = "new-password";
    }
    $("authTitle").textContent = title;
    $("authSub").textContent = sub;
    $("authSubmit").textContent = submit;
    $("authPasswordLabel").textContent = pwLabel;
    $("authPassword").setAttribute("autocomplete", pwAuto);

    if (isForgot || isReset) {
      $("authSwitchText").textContent = "Remembered it?";
      $("authToggle").textContent = "Back to sign in";
    } else {
      $("authSwitchText").textContent = isSignup ? "Already have an account?" : "New here?";
      $("authToggle").textContent = isSignup ? "Sign in" : "Create an account";
    }
  }

  // toggleAuth handles the switch link: login<->signup, and back-to-login from
  // the forgot/reset modes.
  function toggleAuth() {
    setAuthMode(authMode === "login" ? "signup" : "login");
  }

  async function submitAuth(e) {
    if (e) e.preventDefault();
    $("authError").classList.add("is-hidden");
    $("authInfo").classList.add("is-hidden");
    var btn = $("authSubmit");

    if (authMode === "forgot") {
      var fEmail = $("authEmail").value.trim();
      if (!fEmail) { showAuthError("Please enter your email."); return; }
      btn.disabled = true;
      var fr = await api("/api/auth/forgot", postBody({ email: fEmail }));
      btn.disabled = false;
      if (!fr.ok) { showAuthError((fr.data && fr.data.error) || "That did not work. Please try again."); return; }
      var info = $("authInfo");
      info.textContent = "If that email is registered, a reset link is on its way. Check your inbox.";
      info.classList.remove("is-hidden");
      return;
    }

    if (authMode === "reset") {
      var newPw = $("authPassword").value;
      if (!newPw) { showAuthError("Please enter a new password."); return; }
      btn.disabled = true;
      var rr = await api("/api/auth/reset", postBody({ token: resetToken, password: newPw }));
      btn.disabled = false;
      if (!rr.ok) { showAuthError((rr.data && rr.data.error) || "That did not work. Please try again."); return; }
      $("authPassword").value = "";
      resetToken = "";
      setAuthMode("login");
      var done = $("authInfo");
      done.textContent = "Password changed. Sign in with your new password.";
      done.classList.remove("is-hidden");
      return;
    }

    // login / signup
    var email = $("authEmail").value.trim();
    var password = $("authPassword").value;
    if (!email || !password) { showAuthError("Please enter your email and password."); return; }
    btn.disabled = true;
    var path = authMode === "signup" ? "/api/auth/signup" : "/api/auth/login";
    var r = await api(path, postBody({ email: email, password: password }));
    btn.disabled = false;
    if (!r.ok) { showAuthError((r.data && r.data.error) || "That did not work. Please try again."); return; }
    $("authPassword").value = "";
    setAccount(r.data);
    hideAuthGate();
    startApp();
  }

  function setAccount(me) {
    if (me && me.email) $("accountEmail").textContent = me.email;
    // Verify-email banner: hosted app, shown until the email is confirmed.
    var unverified = hosted() && me && me.emailVerified === false && !verifyDismissed;
    if (unverified && me.email) $("verifyEmail").textContent = me.email;
    $("verifyBanner").classList.toggle("is-hidden", !unverified);
  }

  async function resendVerify() {
    var r = await api("/api/auth/verify/resend", postBody({}));
    toast(r.ok ? "Sent. Check your inbox." : ((r.data && r.data.error) || "Could not send right now."));
  }

  async function signOut() {
    await api("/api/auth/logout", postBody({}));
    hide($("verifyBanner"));
    showAuthGate();
  }

  // readUrlFlags reads and clears the one-shot query flags the email links carry:
  // ?verified=1|0 after the verify link, and ?reset=TOKEN for the reset form.
  function readUrlFlags() {
    var search = window.location.search;
    var verified = paramFrom(search, "verified");
    var reset = paramFrom(search, "reset");
    if ((verified !== null || reset !== null) && window.history && window.history.replaceState) {
      window.history.replaceState({}, "", window.location.pathname);
    }
    return { verified: verified, reset: reset };
  }

  function paramFrom(search, key) {
    var m = new RegExp("[?&]" + key + "=([^&]*)").exec(search);
    return m ? decodeURIComponent(m[1]) : null;
  }

  // boot resolves the runtime mode, then (when hosted) requires a session before
  // loading any shop data. The local agent has no auth and starts immediately.
  async function boot() {
    var c = await api("/api/config");
    if (c.ok && c.data && c.data.mode) CONFIG = c.data;
    applyMode();
    var flags = readUrlFlags();
    if (flags.verified === "1") toast("Email confirmed. Thank you!");
    else if (flags.verified === "0") toast("That link has expired. Sign in and resend it.");
    if (hosted()) {
      // A reset link drops the user straight into the new-password form, signed
      // in or not.
      if (flags.reset) { resetToken = flags.reset; showAuthGate("reset"); return; }
      var me = await api("/api/auth/me");
      if (!me.ok) { showAuthGate(); return; }
      setAccount(me.data);
    }
    startApp();
  }

  // startApp loads the shop's data and starts the background pollers. Called once
  // on the local agent, and after sign-in on the hosted app.
  function startApp() {
    loadSettings();
    loadProducts();
    renderCart();
    if (feature("deviceSync")) {
      loadSyncStatus();
      setInterval(loadSyncStatus, 20000);
      document.addEventListener("visibilitychange", function () {
        if (!document.hidden) loadSyncStatus();
      });
    }
  }

  // ---------- Wire up ----------
  function init() {
    // Tabs and any element that requests a view (e.g. the nudge "Set up" link).
    var viewButtons = document.querySelectorAll("[data-view]");
    for (var i = 0; i < viewButtons.length; i++) {
      (function (b) {
        b.addEventListener("click", function () {
          switchView(b.getAttribute("data-view"));
          // A control can deep-link to a Setup section (e.g. the printer nudge,
          // the sync pill) by also carrying data-setup.
          var panel = b.getAttribute("data-setup");
          if (panel) selectSetupPanel(panel);
        });
      })(viewButtons[i]);
    }
    var setupTabs = document.querySelectorAll(".setup-tab");
    for (var s = 0; s < setupTabs.length; s++) {
      (function (b) { b.addEventListener("click", function () { selectSetupPanel(b.getAttribute("data-panel")); }); })(setupTabs[s]);
    }
    var openers = document.querySelectorAll('[data-open="addItem"]');
    for (var j = 0; j < openers.length; j++) {
      openers[j].addEventListener("click", function () { openItem(); });
    }

    $("search").addEventListener("input", onSearch);
    $("search").addEventListener("keydown", onSearchKey);
    $("addItemBtn").addEventListener("click", function () { openItem(); });

    $("clearCart").addEventListener("click", clearCart);
    $("closeCart").addEventListener("click", closeCartSheet);
    $("chargeBtn").addEventListener("click", openPayment);
    $("cartBar").addEventListener("click", openCartSheet);

    $("dismissNudge").addEventListener("click", function () {
      nudgeDismissed = true;
      hide($("printerNudge"));
    });

    $("getAppDismiss").addEventListener("click", function () {
      getAppDismissed = true;
      try { localStorage.setItem("getapp_dismissed", "1"); } catch (e) {}
      hide($("getAppBanner"));
      document.body.classList.remove("has-getapp");
    });

    // Payment controls
    $("payCancel").addEventListener("click", function () { hide($("payOverlay")); });
    $("completeSale").addEventListener("click", completeSale);
    $("payMethodSeg").addEventListener("click", function (e) {
      var m = e.target.getAttribute("data-method");
      if (m) setPayMethod(m);
    });
    $("quickCash").addEventListener("click", function (e) {
      var t = e.target;
      if (t.getAttribute("data-exact")) quickExact();
      else if (t.getAttribute("data-cash")) quickCash(parseInt(t.getAttribute("data-cash"), 10));
    });
    $("pad").addEventListener("click", function (e) {
      var t = e.target;
      if (t.hasAttribute("data-digit")) padDigit(parseInt(t.getAttribute("data-digit"), 10));
      else if (t.hasAttribute("data-zeros")) padZeros();
      else if (t.hasAttribute("data-back")) padBack();
    });

    // Success
    $("successNext").addEventListener("click", function () { hide($("successOverlay")); $("search").focus(); });
    $("successRetry").addEventListener("click", retryPrint);
    $("printSetup").addEventListener("click", function () { hide($("successOverlay")); switchView("setup"); selectSetupPanel("printerPanel"); });
    $("printReceiptBtn").addEventListener("click", function () { printBrowserReceipt(lastSale); });

    // Hosted auth gate + account
    $("authForm").addEventListener("submit", submitAuth);
    $("authToggle").addEventListener("click", toggleAuth);
    $("authForgot").addEventListener("click", function () { setAuthMode("forgot"); $("authEmail").focus(); });
    $("signOut").addEventListener("click", signOut);
    $("verifyResend").addEventListener("click", resendVerify);
    $("verifyDismiss").addEventListener("click", function () { verifyDismissed = true; hide($("verifyBanner")); });

    // Add / edit item
    $("itemCancel").addEventListener("click", function () { hide($("itemOverlay")); });
    $("saveItem").addEventListener("click", saveItem);
    $("trackStock").addEventListener("change", onTrackStockToggle);

    // Confirm dialog (delete)
    $("confirmCancel").addEventListener("click", closeConfirm);
    $("confirmCancel2").addEventListener("click", closeConfirm);
    $("confirmOk").addEventListener("click", function () {
      var fn = pendingConfirm;
      pendingConfirm = null;
      if (fn) fn();
    });

    // Setup
    $("saveShop").addEventListener("click", saveShop);
    $("logoInput").addEventListener("change", uploadLogo);
    $("logoRemove").addEventListener("click", removeLogo);
    $("find").addEventListener("click", findPrinters);
    $("printerTest").addEventListener("click", function () {
      testPrint(settings.printer_addr || "", $("printerTest"));
    });
    $("printerForget").addEventListener("click", forgetPrinter);
    $("scannerToggle").addEventListener("change", saveScanner);
    $("taxMode").addEventListener("change", onTaxModeChange);
    $("saveTax").addEventListener("click", saveTax);
    $("syncLink").addEventListener("click", linkDevice);
    $("syncNow").addEventListener("click", syncNow);
    $("syncUnlink").addEventListener("click", unlinkDevice);
    $("exportSales").addEventListener("click", function () { downloadExport("sales"); });
    $("exportAudit").addEventListener("click", function () { downloadExport("audit"); });
    $("restockSave").addEventListener("click", saveRestock);
    $("restockCancel").addEventListener("click", function () { hide($("restockOverlay")); });
    $("restockQty").addEventListener("keydown", function (e) { if (e.key === "Enter") { saveRestock(); e.preventDefault(); } });
    $("salesSearchForm").addEventListener("submit", doSalesSearch);
    $("salesSearchClear").addEventListener("click", clearSalesSearch);
    $("themePicker").addEventListener("click", function (e) {
      var theme = e.target.getAttribute("data-theme");
      if (theme) chooseTheme(theme);
    });
    $("printSample").addEventListener("click", printSample);

    // Editable header line and ending message: live preview while typing,
    // save on blur. Presets fill the message, then save.
    $("headerLine").addEventListener("input", livePreview);
    $("headerLine").addEventListener("change", saveReceiptText);
    $("footerText").addEventListener("input", livePreview);
    $("footerText").addEventListener("change", saveReceiptText);
    $("footerPresets").addEventListener("click", function (e) {
      if (!e.target.hasAttribute("data-footer")) return;
      $("footerText").value = e.target.getAttribute("data-footer");
      loadPreview();
      saveReceiptText();
      toast("Ending message saved.");
    });

    document.addEventListener("keydown", onKeydown);
    document.addEventListener("keydown", onScanKey);

    // Close an overlay when its dark backdrop (not the sheet) is clicked.
    ["payOverlay", "itemOverlay", "confirmOverlay", "restockOverlay"].forEach(function (id) {
      $(id).addEventListener("click", function (e) { if (e.target === $(id)) hide($(id)); });
    });

    // Resolve the runtime mode and (when hosted) require sign-in, then start the
    // app. Replaces the old unconditional data load so the hosted app never loads
    // shop data before there is a session.
    boot();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
