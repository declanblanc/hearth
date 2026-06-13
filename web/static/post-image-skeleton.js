// post-image-skeleton.js — turns off a post image's loading skeleton once the
// photo has actually painted.
//
// Each .post-image starts with a shimmer skeleton (see app.css). The image's box
// is already reserved by its width/height attributes, so the skeleton fills the
// exact space the photo will occupy and the page never jumps. This script's only
// job is to mark each image .is-loaded when it finishes, which stops the shimmer
// animation and fades the photo in.
//
// Pure progressive enhancement: without it the photos still load and display —
// the skeleton just stays painted underneath the (opaque) photo instead of being
// switched off.

(function () {
  "use strict";

  function markLoaded(image) {
    image.classList.add("is-loaded");
  }

  // A `load` event doesn't bubble, so we listen in the capture phase to catch it
  // for every .post-image — including ones htmx swaps in later (older feed pages,
  // profile pages) without needing to re-run any setup.
  document.addEventListener(
    "load",
    function (event) {
      var target = event.target;
      if (target && target.classList && target.classList.contains("post-image")) {
        markLoaded(target);
      }
    },
    true
  );

  // Images already complete by the time this script runs (e.g. served from cache)
  // won't fire a fresh load event, so reconcile them on startup.
  document.querySelectorAll(".post-image").forEach(function (image) {
    if (image.complete && image.naturalWidth > 0) {
      markLoaded(image);
    }
  });
})();
