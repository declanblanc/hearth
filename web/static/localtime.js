// localtime.js — converts server-rendered UTC timestamps into the viewer's
// local timezone. It is pure progressive enhancement: timestamps are emitted
// server-side as <time datetime="<ISO-8601 UTC>">…UTC</time>, so if this script
// never runs the user still sees a correct, unambiguous time (labelled "UTC").
// When it does run, the visible text is replaced with the same instant rendered
// in the browser's local timezone.
//
// Because Hearth swaps content in with htmx (new posts, comments, notifications,
// feed pages), we localize on initial load AND after every htmx swap, and we
// guard against double-processing with a data flag.

(function () {
  "use strict";

  // Matches the human-readable fallback produced by the server's `datetime`
  // template helper, e.g. "Jan 2, 2006, 3:04 PM". We mirror that shape so a
  // localized timestamp reads identically apart from the timezone it reflects.
  var DISPLAY_OPTIONS = {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  };

  // Localize a single <time> element. Idempotent: once converted, an element is
  // flagged so repeat passes (e.g. after an htmx swap that re-includes it) skip
  // it. Elements with an unparseable or missing datetime are left untouched so
  // the server-rendered UTC fallback remains visible.
  function localizeElement(timeElement) {
    if (timeElement.hasAttribute("data-localized")) {
      return;
    }

    var iso = timeElement.getAttribute("datetime");
    if (!iso) {
      return;
    }

    var parsed = new Date(iso);
    if (isNaN(parsed.getTime())) {
      return; // leave the UTC fallback text in place.
    }

    try {
      timeElement.textContent = parsed.toLocaleString(undefined, DISPLAY_OPTIONS);
    } catch (e) {
      // Intl unavailable or options rejected — keep the server fallback.
      return;
    }

    // A full-precision local time is handy on hover for anyone curious.
    timeElement.setAttribute("title", parsed.toLocaleString());
    timeElement.setAttribute("data-localized", "");
  }

  // Localize every <time datetime> within a root node (defaults to document).
  // querySelectorAll only matches descendants, so when the root element is
  // itself a <time> (e.g. an htmx swap target that is the timestamp), we handle
  // it explicitly — otherwise such an element would never be converted.
  function localizeWithin(root) {
    var scope = root || document;
    if (scope.matches && scope.matches("time[datetime]")) {
      localizeElement(scope);
    }
    if (!scope.querySelectorAll) {
      return; // e.g. the document fragment edge cases / text nodes.
    }
    var elements = scope.querySelectorAll("time[datetime]");
    for (var i = 0; i < elements.length; i++) {
      localizeElement(elements[i]);
    }
  }

  // Initial page load.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () {
      localizeWithin(document);
    });
  } else {
    localizeWithin(document);
  }

  // htmx swaps in new markup that may contain timestamps; localize the freshly
  // inserted subtree. `event.target` is the element that received the swap.
  document.body.addEventListener("htmx:afterSwap", function (event) {
    localizeWithin(event.target);
  });
})();
