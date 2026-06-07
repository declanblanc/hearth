// comments.js — reveals the per-comment reply box (issue #11).
//
// Each comment has a "Reply" button carrying data-reply-toggle="<id>" pointing
// at a hidden reply <form id="<id>">. Clicking it shows the form and focuses the
// textarea; clicking again hides it. A single delegated listener covers comments
// that arrive later via htmx swaps. Without this script the reply forms stay
// hidden, but the top-level add-comment box still works, so commenting degrades
// gracefully.

(function () {
  "use strict";

  document.addEventListener("click", function (event) {
    var toggle = event.target.closest("[data-reply-toggle]");
    if (!toggle) return;

    var form = document.getElementById(toggle.getAttribute("data-reply-toggle"));
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
