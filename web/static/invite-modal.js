// Enhances the "Generate invite link" form on the connections page into a
// modal. Without JS the form POSTs to /invites and renders the link on its own
// page (invite_created.html); here we intercept the submit, post for the bare
// fragment, and show it in a native <dialog> with a copy button.
(function () {
  "use strict";

  var dialog = document.getElementById("invite-dialog");
  if (!dialog) return;
  var body = dialog.querySelector("[data-invite-body]");

  document.addEventListener("submit", function (event) {
    var form = event.target.closest("[data-invite-form]");
    if (!form) return;

    // No <dialog> support: let the form submit and fall back to the full page.
    if (typeof dialog.showModal !== "function") return;
    event.preventDefault();

    body.innerHTML = '<p class="muted">Generating…</p>';
    dialog.showModal();

    fetch(form.action, {
      method: "POST",
      headers: { "X-Fragment": "1" },
      credentials: "same-origin",
    })
      .then(function (res) {
        if (!res.ok && res.status !== 429) throw new Error("request failed");
        return res.text();
      })
      .then(function (html) {
        body.innerHTML = html;
      })
      .catch(function () {
        body.innerHTML = '<p class="muted">Couldn’t generate a link. Please try again.</p>';
      });
  });

  dialog.addEventListener("click", function (event) {
    // Close on the close button or a click on the backdrop (the dialog element
    // itself, outside its inner content).
    if (event.target === dialog || event.target.closest("[data-invite-close]")) {
      dialog.close();
      return;
    }

    var copyButton = event.target.closest("[data-invite-copy]");
    if (!copyButton) return;
    var field = dialog.querySelector("[data-invite-url]");
    if (!field) return;

    var flash = function () {
      copyButton.textContent = "Copied";
      setTimeout(function () { copyButton.textContent = "Copy"; }, 1500);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(field.value).then(flash, function () {
        field.select();
        document.execCommand("copy");
        flash();
      });
    } else {
      field.select();
      document.execCommand("copy");
      flash();
    }
  });
})();
