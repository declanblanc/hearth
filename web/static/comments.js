// comments.js — reveals the otherwise-hidden comment and reply boxes (issues
// #11, #29, #58).
//
// Two kinds of buttons open a hidden <form>:
//
//   * The top-level "Comment" button carries data-toggle-target="<form id>"
//     pointing directly at that thread's add-comment form. Clicking shows the
//     form and focuses its textarea; clicking again hides it.
//
//   * Each comment's "Reply" button (.comment-reply-toggle) shares ONE reply
//     form that lives at the bottom of the thread, below every reply (#58).
//     The button carries data-reply-to (the parent comment id) and
//     data-reply-to-name (its author). Clicking points the shared form at
//     /comments/<id>/replies, fills the "replying to" hint, and reveals it.
//     Clicking a different Reply re-points the same form; clicking the same
//     Reply again hides it.
//
// A single delegated listener covers forms and buttons that arrive later via
// htmx swaps.
//
// Without this script every comment box stays hidden. That is a deliberate
// trade-off (#29): commenting requires JS, which the app already assumes for
// the composer. The toggle buttons are real <button>s, so they simply do
// nothing rather than appearing broken.

(function () {
  "use strict";

  // Show a hidden form and focus its textarea; return whether it is now shown.
  function reveal(form) {
    form.removeAttribute("hidden");
    var textarea = form.querySelector("textarea");
    if (textarea) textarea.focus();
  }

  function hide(form) {
    form.setAttribute("hidden", "");
  }

  // The shared reply form for a Reply button, found within the same thread
  // section. Its id is reply-<postID> (see the "commentthread" template).
  function replyFormFor(button) {
    var section = button.closest(".comments");
    if (!section) return null;
    return section.querySelector(".comment-reply-form");
  }

  // Point the shared reply form at a parent comment and fill its attribution
  // hint. htmx reads hx-post at request time, so re-process the form after
  // rewriting the attribute.
  function pointReplyForm(form, parentID, parentName) {
    var url = "/comments/" + parentID + "/replies";
    form.setAttribute("action", url);
    form.setAttribute("hx-post", url);
    if (window.htmx) htmx.process(form);

    var hint = form.querySelector(".comment-reply-hint");
    if (hint) {
      if (parentName) {
        hint.textContent = "replying to " + parentName;
        hint.removeAttribute("hidden");
      } else {
        hint.textContent = "";
        hint.setAttribute("hidden", "");
      }
    }
  }

  document.addEventListener("click", function (event) {
    var replyToggle = event.target.closest(".comment-reply-toggle");
    if (replyToggle) {
      var form = replyFormFor(replyToggle);
      if (!form) return;

      var parentID = replyToggle.getAttribute("data-reply-to");
      var parentName = replyToggle.getAttribute("data-reply-to-name");

      // Clicking the same Reply that already owns an open form closes it;
      // any other click (or a fresh open) re-points the form and reveals it.
      var alreadyThis =
        !form.hasAttribute("hidden") &&
        form.getAttribute("action") === "/comments/" + parentID + "/replies";

      // Reset every Reply button's expanded state, then mark this one.
      var section = replyToggle.closest(".comments");
      if (section) {
        section.querySelectorAll(".comment-reply-toggle").forEach(function (btn) {
          btn.setAttribute("aria-expanded", "false");
        });
      }

      if (alreadyThis) {
        hide(form);
      } else {
        pointReplyForm(form, parentID, parentName);
        reveal(form);
        replyToggle.setAttribute("aria-expanded", "true");
      }
      return;
    }

    // Top-level "Comment" toggle: unchanged direct target-by-id behaviour.
    var toggle = event.target.closest("[data-toggle-target]");
    if (!toggle) return;

    var target = document.getElementById(toggle.getAttribute("data-toggle-target"));
    if (!target) return;

    var nowHidden = target.hasAttribute("hidden");
    if (nowHidden) {
      reveal(target);
    } else {
      hide(target);
    }
    toggle.setAttribute("aria-expanded", String(nowHidden));
  });
})();
