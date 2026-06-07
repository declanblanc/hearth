// Enhances "View Likes" links into a modal. Each link keeps its href to the
// standalone /posts/{id}/likes page, so without JS it still works as a plain
// navigation; here we intercept the click, fetch the bare list fragment, and
// show it in a native <dialog>.
(function () {
  "use strict";

  var dialog = document.getElementById("likes-dialog");
  if (!dialog) return;
  var body = dialog.querySelector("[data-likes-body]");

  document.addEventListener("click", function (event) {
    var trigger = event.target.closest("[data-likes-trigger]");
    if (!trigger) return;

    // Let modified clicks (new tab, etc.) fall through to the real link.
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();

    body.innerHTML = '<p class="muted">Loading…</p>';
    if (typeof dialog.showModal === "function") {
      dialog.showModal();
    } else {
      // Very old browser without <dialog> support: just follow the link.
      window.location.href = trigger.href;
      return;
    }

    fetch(trigger.href, { headers: { "X-Fragment": "1" }, credentials: "same-origin" })
      .then(function (res) {
        if (!res.ok) throw new Error("request failed");
        return res.text();
      })
      .then(function (html) {
        body.innerHTML = html;
      })
      .catch(function () {
        body.innerHTML = '<p class="muted">Couldn’t load likes. Please try again.</p>';
      });
  });

  // Close on the close button or a click on the backdrop (the dialog element
  // itself, outside its inner content).
  dialog.addEventListener("click", function (event) {
    if (event.target === dialog || event.target.closest("[data-likes-close]")) {
      dialog.close();
    }
  });
})();
