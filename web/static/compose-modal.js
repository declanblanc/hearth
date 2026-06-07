// compose-modal.js — opens the shared "Create post" composer in a <dialog>
// (issue #19). The same dialog is used on the home feed and the profile page;
// a [data-compose-open] button opens it, the close button or a backdrop click
// closes it, and Esc closes it natively.
//
// Progressive enhancement: the composer is a normal multipart form that posts
// to /posts. When the server rejects a post it redirects back to the profile
// with ?post_error=…, which renders an error banner inside the dialog and marks
// it [data-compose-error]; we re-open the dialog on load so the message is seen.
//
// If the browser lacks <dialog>.showModal we fall back to simply unhiding the
// dialog element, so the composer is still reachable.

(function () {
  "use strict";

  var dialog = document.getElementById("compose-dialog");
  if (!dialog) return;

  var canModal = typeof dialog.showModal === "function";

  function open() {
    if (canModal) {
      if (!dialog.open) dialog.showModal();
    } else {
      dialog.setAttribute("open", "");
    }
    // Focus the textarea so the user can start typing immediately.
    var textarea = dialog.querySelector("textarea");
    if (textarea) textarea.focus();
  }

  function close() {
    if (canModal) {
      if (dialog.open) dialog.close();
    } else {
      dialog.removeAttribute("open");
    }
  }

  // Any "Create post" trigger on the page opens the dialog.
  document.addEventListener("click", function (event) {
    if (event.target.closest("[data-compose-open]")) {
      event.preventDefault();
      open();
      return;
    }
    // Close button, or a click on the backdrop (the dialog element itself).
    if (event.target === dialog || event.target.closest("[data-compose-close]")) {
      close();
    }
  });

  // A post that failed validation comes back with the error banner already
  // rendered inside the dialog — re-open so the user sees it (and can retry).
  if (dialog.hasAttribute("data-compose-error")) {
    open();
  }
})();
