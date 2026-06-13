// post-lightbox.js — click a post photo to view it larger in an overlay,
// without leaving the page (issue #8).
//
// Clicking any .post-image opens a full-screen overlay showing that image. When
// the post has several photos, the overlay becomes a gallery: previous/next
// controls (and the ← / → keys) move through the images of that same post.
// Closing returns the viewer exactly where they were.
//
// Pure progressive enhancement: without this script the photos still render
// inline as plain <img> elements; they simply don't open a larger view. One
// overlay element is created lazily and reused for every post on the page, and
// the gallery is scoped to the .post-media group the clicked image belongs to.

(function () {
  "use strict";

  var overlay = null; // the single reused overlay element
  var imageEl = null; // the <img> shown inside the overlay
  var prevBtn = null;
  var nextBtn = null;
  var counterEl = null; // "2 / 4" position label — a gallery aid, never a post count
  var gallery = []; // image sources for the active post
  var index = 0; // current position within `gallery`
  var lastFocused = null; // element to restore focus to on close

  function buildOverlay() {
    overlay = document.createElement("div");
    overlay.className = "lightbox";
    overlay.hidden = true;
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-label", "Photo viewer");

    var closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "lightbox-close";
    closeBtn.setAttribute("aria-label", "Close photo viewer");
    closeBtn.textContent = "×"; // ×

    prevBtn = document.createElement("button");
    prevBtn.type = "button";
    prevBtn.className = "lightbox-nav lightbox-prev";
    prevBtn.setAttribute("aria-label", "Previous photo");
    prevBtn.textContent = "‹"; // ‹

    nextBtn = document.createElement("button");
    nextBtn.type = "button";
    nextBtn.className = "lightbox-nav lightbox-next";
    nextBtn.setAttribute("aria-label", "Next photo");
    nextBtn.textContent = "›"; // ›

    imageEl = document.createElement("img");
    imageEl.className = "lightbox-image";
    imageEl.alt = "";

    counterEl = document.createElement("div");
    counterEl.className = "lightbox-counter";

    var figure = document.createElement("div");
    figure.className = "lightbox-figure";
    figure.appendChild(imageEl);

    overlay.appendChild(closeBtn);
    overlay.appendChild(prevBtn);
    overlay.appendChild(figure);
    overlay.appendChild(nextBtn);
    overlay.appendChild(counterEl);
    document.body.appendChild(overlay);

    closeBtn.addEventListener("click", close);
    prevBtn.addEventListener("click", function () {
      step(-1);
    });
    nextBtn.addEventListener("click", function () {
      step(1);
    });
    // A click on the backdrop (but not on the image or controls) closes.
    overlay.addEventListener("click", function (event) {
      if (event.target === overlay || event.target === figure) {
        close();
      }
    });

    // On touch devices, swiping left/right moves through the gallery, mirroring
    // the ‹ / › arrows. The arrows stay for tap access; this just adds the
    // gesture most people reach for on a phone.
    enableSwipe(overlay);
  }

  // A horizontal drag only counts as a swipe once it clears SWIPE_MIN_PX and is
  // clearly more horizontal than vertical — otherwise a vertical scroll or a
  // stray tap would flip the photo. A swipe left (finger moves right→left, so a
  // negative delta) advances to the next photo, like dragging the current one
  // off-screen.
  var SWIPE_MIN_PX = 40;

  function enableSwipe(element) {
    var startX = 0;
    var startY = 0;

    element.addEventListener(
      "touchstart",
      function (event) {
        var touch = event.changedTouches[0];
        startX = touch.clientX;
        startY = touch.clientY;
      },
      { passive: true },
    );

    element.addEventListener(
      "touchend",
      function (event) {
        var touch = event.changedTouches[0];
        var deltaX = touch.clientX - startX;
        var deltaY = touch.clientY - startY;
        if (
          Math.abs(deltaX) < SWIPE_MIN_PX ||
          Math.abs(deltaX) <= Math.abs(deltaY)
        ) {
          return;
        }
        step(deltaX < 0 ? 1 : -1);
      },
      { passive: true },
    );
  }

  function show() {
    imageEl.src = gallery[index];
    var multi = gallery.length > 1;
    // Only offer an arrow when there's actually a photo in that direction: none
    // for a single photo, right-only on the first, left-only on the last.
    prevBtn.hidden = index === 0;
    nextBtn.hidden = index === gallery.length - 1;
    counterEl.hidden = !multi;
    if (multi) {
      counterEl.textContent = index + 1 + " / " + gallery.length;
    }
  }

  // Move through the gallery. The ends are hard stops — no wrap-around — to match
  // the arrows, which are hidden when there's nowhere to go in that direction.
  function step(delta) {
    var next = index + delta;
    if (next < 0 || next >= gallery.length) return;
    index = next;
    show();
  }

  function open(images, startIndex) {
    if (!overlay) buildOverlay();
    gallery = images;
    index = startIndex;
    lastFocused = document.activeElement;
    show();
    overlay.hidden = false;
    document.body.classList.add("lightbox-open");
    document.addEventListener("keydown", onKeydown);
    // Move focus into the dialog so Esc/arrows work and focus is contained.
    overlay.querySelector(".lightbox-close").focus();
  }

  function close() {
    if (!overlay || overlay.hidden) return;
    overlay.hidden = true;
    document.body.classList.remove("lightbox-open");
    document.removeEventListener("keydown", onKeydown);
    imageEl.src = "";
    if (lastFocused && typeof lastFocused.focus === "function") {
      lastFocused.focus();
    }
  }

  function onKeydown(event) {
    switch (event.key) {
      case "Escape":
        close();
        break;
      case "ArrowLeft":
        step(-1);
        break;
      case "ArrowRight":
        step(1);
        break;
    }
  }

  // One delegated listener handles every post photo, including those that arrive
  // later via htmx swaps.
  document.addEventListener("click", function (event) {
    var img = event.target.closest(".post-image");
    if (!img) return;

    var group = img.closest(".post-media");
    var images = group
      ? Array.prototype.slice.call(group.querySelectorAll(".post-image"))
      : [img];

    var sources = images.map(function (el) {
      return el.currentSrc || el.src;
    });
    open(sources, images.indexOf(img));
  });
})();
