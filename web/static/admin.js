// teenyurl admin. Every feature here is an addition to a page that already
// works without it: the forms post, the details panels open, and the times
// read as UTC if this script never runs.

(function () {
  "use strict";

  // A datetime-local input carries no time zone, so tell the server which one
  // the reader is in. The sign matches Date.getTimezoneOffset: minutes to add
  // to local time to reach UTC.
  function stampTimeZone() {
    var offset = new Date().getTimezoneOffset();
    document.querySelectorAll("form[data-tz] input[name=tz_offset]").forEach(function (input) {
      input.value = String(offset);
    });
  }

  // Times are stored, sent, and rendered as UTC, which keeps tzdata out of the
  // container image. Rewrite them here into the reader's own zone.
  function localiseTimes() {
    var fmt = new Intl.DateTimeFormat(undefined, {
      year: "numeric", month: "short", day: "numeric",
      hour: "2-digit", minute: "2-digit"
    });
    document.querySelectorAll("time[datetime]").forEach(function (el) {
      var when = new Date(el.getAttribute("datetime"));
      if (!isNaN(when.getTime())) {
        el.textContent = fmt.format(when);
      }
    });
  }

  function wireCopyButtons() {
    document.addEventListener("click", function (event) {
      var button = event.target.closest("[data-copy]");
      if (!button || !navigator.clipboard) {
        return;
      }
      navigator.clipboard.writeText(button.dataset.copy).then(function () {
        button.dataset.copied = "1";
        setTimeout(function () { delete button.dataset.copied; }, 1500);
      });
    });
  }

  function wireFilter() {
    var box = document.getElementById("filter");
    var list = document.getElementById("links");
    if (!box || !list) {
      return;
    }
    // The whole list is already in the page, so filtering is local. Reveal the
    // box only now, so it never appears without working.
    box.hidden = false;
    box.addEventListener("input", function () {
      var needle = box.value.trim().toLowerCase();
      list.querySelectorAll(".link").forEach(function (item) {
        var hay = (item.dataset.search || "").toLowerCase();
        item.hidden = needle !== "" && hay.indexOf(needle) === -1;
      });
    });
  }

  // Confirm a delete in a native dialog. Without this the delete button still
  // works, which is why it sits inside the edit panel rather than beside every
  // row: opening the panel is the first of the two deliberate clicks.
  function wireDeleteConfirm() {
    var dialog = document.getElementById("confirm");
    var text = document.getElementById("confirm-text");
    if (!dialog || !text || typeof dialog.showModal !== "function") {
      return;
    }
    var pending = null;
    document.querySelectorAll("form[data-confirm]").forEach(function (form) {
      form.addEventListener("submit", function (event) {
        if (form.dataset.confirmed === "1") {
          return;
        }
        event.preventDefault();
        pending = form;
        text.textContent = form.dataset.confirm;
        dialog.showModal();
      });
    });
    dialog.addEventListener("close", function () {
      if (dialog.returnValue === "go" && pending) {
        pending.dataset.confirmed = "1";
        pending.submit();
      }
      pending = null;
    });
  }

  stampTimeZone();
  localiseTimes();
  wireCopyButtons();
  wireFilter();
  wireDeleteConfirm();
})();
