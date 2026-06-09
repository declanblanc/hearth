// comments.js — reveals the otherwise-hidden comment and reply boxes (issues
// #11, #29).
//
// Both the top-level "Comment" button and each comment's "Reply" button carry
// data-toggle-target="<form id>" pointing at a hidden <form>. Clicking shows the
// form and focuses its textarea; clicking again hides it. A single delegated
// listener covers forms that arrive later via htmx swaps.
//
// Without this script every comment box stays hidden. That is a deliberate
// trade-off (#29): commenting requires JS, which the app already assumes for the
// composer and like controls. The toggle buttons are real <button>s, so they
// simply do nothing rather than appearing broken.

(function () {
  "use strict";

  document.addEventListener("click", function (event) {
    var toggle = event.target.closest("[data-toggle-target]");
    if (!toggle) return;

    var form = document.getElementById(toggle.getAttribute("data-toggle-target"));
    if (!form) return;

    var nowHidden = form.hasAttribute("hidden");
    if (nowHidden) {
      form.removeAttribute("hidden");
      var textarea = form.querySelector("textarea");
      if (textarea) textarea.focus();
    } else {
      form.setAttribute("hidden", "");
    }
    toggle.setAttribute("aria-expanded", String(nowHidden));
  });
})();
