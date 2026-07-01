// comments.js — reveals the otherwise-hidden comment and reply boxes (issues
// #11, #29, #58).
//
// Two kinds of buttons open a hidden <form>:
//
//   * The top-level "Comment" button carries data-toggle-target="<form id>"
//     pointing directly at that thread's add-comment form. Clicking shows the
//     form and focuses its textarea; clicking again hides it.
//
//   * Each comment's "Reply" button (.comment-reply-toggle) shares the reply
//     form on its ROOT comment, which sits after that comment's replies — so
//     the box opens at the bottom of that sub-thread, not the whole post (#58).
//     A Reply on a flattened child climbs to the same root form. The button
//     carries data-reply-to (the parent comment id) and data-reply-to-name (its
//     author). Clicking points the form at /comments/<id>/replies, fills the
//     "replying to" hint, and reveals it. Clicking a different Reply in the same
//     sub-thread re-points that form; clicking the same Reply again hides it.
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

  // The outermost .comment <li> containing a Reply button. Replies are
  // flattened one tier under a root comment, so a Reply on a child still
  // belongs to that root's sub-thread — climb past any nesting to reach it.
  function rootCommentOf(button) {
    var li = button.closest(".comment");
    if (!li) return null;
    var ancestor = li.parentElement && li.parentElement.closest(".comment");
    while (ancestor) {
      li = ancestor;
      ancestor = li.parentElement && li.parentElement.closest(".comment");
    }
    return li;
  }

  // The shared reply form for a Reply button lives on its root comment, after
  // that comment's replies, so it opens at the bottom of that sub-thread — not
  // the bottom of the whole post (#58). Its id is reply-<rootCommentID>.
  function replyFormFor(root) {
    return root ? root.querySelector(":scope > .comment-reply-form") : null;
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
      var root = rootCommentOf(replyToggle);
      var form = replyFormFor(root);
      if (!form) return;

      var parentID = replyToggle.getAttribute("data-reply-to");
      var parentName = replyToggle.getAttribute("data-reply-to-name");

      // Clicking the same Reply that already owns an open form closes it;
      // any other click (or a fresh open) re-points the form and reveals it.
      var alreadyThis =
        !form.hasAttribute("hidden") &&
        form.getAttribute("action") === "/comments/" + parentID + "/replies";

      // Reset this sub-thread's Reply buttons, then mark the clicked one. Scoped
      // to the root so other comments' open reply boxes are left untouched.
      root.querySelectorAll(".comment-reply-toggle").forEach(function (btn) {
        btn.setAttribute("aria-expanded", "false");
      });

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
