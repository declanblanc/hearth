// confirm-dialog.js — replaces the native browser confirm() prompt before a
// destructive submit with the in-app <dialog id="confirm-dialog"> defined once
// in the base layout.
//
// Any form carrying a data-confirm="…" attribute is intercepted on submit: we
// cancel the submit, show its message in the dialog, and only let the form
// through once the user presses Delete.
//
// Confirmed-resubmit, and why it does not loop:
//   On the first submit of a data-confirm form we call event.preventDefault()
//   and open the dialog — the form does NOT submit. If the user confirms, we
//   set a per-form "confirmed" flag on that form element and call
//   form.requestSubmit(), which fires a fresh submit event. Our handler runs
//   again, sees the flag, clears it, and returns WITHOUT preventing default —
//   so the real submit proceeds exactly once. Because the flag is cleared on
//   that second pass, a later delete on the same form starts over and prompts
//   again. There is no third synthetic submit, so no loop.
//
//   For the HTMX comment-delete form this matters: htmx also listens on the
//   submit event. On the confirmed pass we do not call preventDefault() and do
//   not stop propagation, so htmx sees the event normally and issues its
//   hx-post as usual. We only ever suppress the FIRST (unconfirmed) submit.
//
// The submit listener is bound on document with event delegation so it still
// covers forms that htmx swaps into the DOM (comment threads re-render after
// posting or deleting a comment).
//
// Progressive enhancement: without JS the forms submit normally with no
// prompt, which matches the old confirm()-based behaviour (also JS-only).

(function () {
  "use strict";

  var dialog = document.getElementById("confirm-dialog");
  if (!dialog) return;

  var messageEl = dialog.querySelector("[id='confirm-dialog-message']");
  var okButton = dialog.querySelector("[data-confirm-ok]");
  var cancelButton = dialog.querySelector("[data-confirm-cancel]");
  var canModal = typeof dialog.showModal === "function";

  // The form awaiting confirmation while the dialog is open.
  var pendingForm = null;

  function openDialog(message) {
    messageEl.textContent = message;
    if (canModal) {
      if (!dialog.open) dialog.showModal();
    } else {
      dialog.setAttribute("open", "");
    }
    // Focus the confirm button so keyboard users can act immediately;
    // showModal() already traps focus and wires Esc for us.
    okButton.focus();
  }

  function closeDialog() {
    if (canModal) {
      if (dialog.open) dialog.close();
    } else {
      dialog.removeAttribute("open");
    }
    pendingForm = null;
  }

  // Intercept the submit of any data-confirm form, in the capture phase, so we
  // run before htmx's own submit handling on the same event.
  document.addEventListener(
    "submit",
    function (event) {
      var form = event.target;
      if (!form || !form.matches || !form.matches("form[data-confirm]")) return;

      // Confirmed pass: let this submit through untouched (native POST or htmx).
      if (form.dataset.confirmed === "true") {
        delete form.dataset.confirmed;
        return;
      }

      // First pass: suppress the submit and ask for confirmation.
      event.preventDefault();
      pendingForm = form;
      openDialog(form.getAttribute("data-confirm") || "Are you sure?");
    },
    true
  );

  // Confirm: flag the form and re-submit it for real.
  okButton.addEventListener("click", function () {
    var form = pendingForm;
    closeDialog();
    if (!form) return;
    form.dataset.confirmed = "true";
    if (typeof form.requestSubmit === "function") {
      form.requestSubmit();
    } else {
      // Very old browsers: submit() skips the submit event, so clear the flag.
      delete form.dataset.confirmed;
      form.submit();
    }
  });

  // Cancel, backdrop click, and Esc all close without submitting.
  cancelButton.addEventListener("click", closeDialog);
  dialog.addEventListener("click", function (event) {
    if (event.target === dialog) closeDialog();
  });
  // Native <dialog> Esc fires a cancel/close event; make sure our state resets.
  dialog.addEventListener("close", function () {
    pendingForm = null;
  });
})();
