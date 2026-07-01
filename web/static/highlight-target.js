// highlight-target.js — when the page is opened from a notification, the link
// carries a fragment and the browser scrolls to that element. Nothing otherwise
// marks which element was meant, so on load we add the .highlight-target class
// to the anchored element; app.css fades a brief accent glow out over ~2s, then
// we remove the class so the element returns to normal.
//
// Notification links target a post (#post-N) for comment-on-post or the reply
// comment (#comment-N) for a reply — see web/templates/notifications.html. Posts
// render as <article id="post-N"> and comments as <li id="comment-N">, so
// document.getElementById(location.hash) resolves either kind. The logic is
// anchor-agnostic: it highlights whatever element the hash names.
//
// A one-shot class added on load (rather than the :target CSS pseudo-class) is
// used deliberately: :target cannot auto-fade after a delay and would re-fire
// on every same-page anchor click. This only reacts to the hash present when
// the page loads, which is exactly the "arrived from a notification" case. The
// browser may have already scrolled before this runs, so we read location.hash
// at load time rather than listening for a navigation event.

(function () {
  "use strict";

  var HIGHLIGHT_CLASS = "highlight-target";
  // Slightly longer than the 2s CSS animation so removal never clips the fade.
  var REMOVE_AFTER_MS = 2200;

  function highlightHashTarget() {
    var hash = window.location.hash;
    if (!hash || hash.length < 2) {
      return;
    }

    // hash includes the leading "#"; getElementById wants the bare id.
    var target = document.getElementById(hash.slice(1));
    if (!target) {
      return;
    }

    target.classList.add(HIGHLIGHT_CLASS);
    window.setTimeout(function () {
      target.classList.remove(HIGHLIGHT_CLASS);
    }, REMOVE_AFTER_MS);
  }

  // Flash an element with the same fade the on-load path uses. Re-adding the
  // class only restarts the animation after a reflow, so a rapid second click on
  // the same target still glows.
  function flash(target) {
    target.classList.remove(HIGHLIGHT_CLASS);
    void target.offsetWidth;
    target.classList.add(HIGHLIGHT_CLASS);
    window.setTimeout(function () {
      target.classList.remove(HIGHLIGHT_CLASS);
    }, REMOVE_AFTER_MS);
  }

  // A reply's reference line links to the comment it answers (#comment-N). The
  // browser scrolls there via the anchor; we also flash it so it's clear which
  // comment you landed on.
  document.addEventListener("click", function (event) {
    var link = event.target.closest && event.target.closest("a.comment-reply-ref");
    if (!link) {
      return;
    }
    var target = document.getElementById(link.getAttribute("href").slice(1));
    if (target) {
      flash(target);
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", highlightHashTarget);
  } else {
    highlightHashTarget();
  }
})();
