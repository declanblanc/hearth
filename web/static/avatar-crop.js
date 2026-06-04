// avatar-crop.js — a small, dependency-free square cropper for the profile photo
// field. It is pure progressive enhancement: if this script doesn't run, the
// plain <input type="file"> still submits the original image, which the server
// center-crops on its own. When it does run, the user gets to choose which
// square region of their photo becomes the avatar.
//
// Wiring (see profile_edit.html): a container element carries
// data-avatar-crop, with a child file <input>, a <canvas> viewport, and a
// zoom <input type="range">. On submit we paint the chosen square to an export
// canvas and hand the resulting JPEG back to the file input so the normal form
// POST carries the cropped bytes.

(function () {
  "use strict";

  // Edge length, in CSS pixels, of the on-screen crop viewport.
  var VIEWPORT = 240;
  // Edge length, in pixels, of the exported crop. The server clamps to its own
  // size too; we export a bit larger than strictly needed so quality holds up.
  var EXPORT_SIZE = 512;

  function setupCropper(root) {
    var fileInput = root.querySelector('input[type="file"]');
    var canvas = root.querySelector("canvas[data-crop-canvas]");
    var zoom = root.querySelector('input[data-crop-zoom]');
    var stage = root.querySelector("[data-crop-stage]");
    var form = root.closest("form");
    if (!fileInput || !canvas || !zoom || !form) {
      return; // markup not as expected — leave the plain input in place.
    }

    var ctx = canvas.getContext("2d");
    var image = null; // the loaded HTMLImageElement, or null when none chosen.

    // View state: minScale fits the image so it always covers the viewport;
    // scale is the user's current zoom; offset pans the image within the box.
    var minScale = 1;
    var scale = 1;
    var offsetX = 0;
    var offsetY = 0;

    // Size the canvas for crisp rendering on high-DPI screens.
    var dpr = window.devicePixelRatio || 1;
    canvas.width = VIEWPORT * dpr;
    canvas.height = VIEWPORT * dpr;
    canvas.style.width = VIEWPORT + "px";
    canvas.style.height = VIEWPORT + "px";

    function clampOffsets() {
      // Keep the scaled image fully covering the viewport (no empty gaps).
      var scaledW = image.naturalWidth * scale;
      var scaledH = image.naturalHeight * scale;
      var minX = VIEWPORT - scaledW;
      var minY = VIEWPORT - scaledH;
      if (offsetX > 0) offsetX = 0;
      if (offsetY > 0) offsetY = 0;
      if (offsetX < minX) offsetX = minX;
      if (offsetY < minY) offsetY = minY;
    }

    function draw() {
      ctx.save();
      ctx.scale(dpr, dpr);
      ctx.clearRect(0, 0, VIEWPORT, VIEWPORT);
      if (image) {
        ctx.drawImage(
          image,
          offsetX,
          offsetY,
          image.naturalWidth * scale,
          image.naturalHeight * scale
        );
      }
      ctx.restore();
    }

    function loadImage(file) {
      var url = URL.createObjectURL(file);
      var img = new Image();
      img.onload = function () {
        URL.revokeObjectURL(url);
        image = img;
        // Scale so the smaller dimension just fills the viewport.
        minScale = Math.max(
          VIEWPORT / img.naturalWidth,
          VIEWPORT / img.naturalHeight
        );
        scale = minScale;
        // Center the image in the viewport.
        offsetX = (VIEWPORT - img.naturalWidth * scale) / 2;
        offsetY = (VIEWPORT - img.naturalHeight * scale) / 2;
        zoom.min = "1";
        zoom.max = "3"; // allow up to 3× the fitted size
        zoom.step = "0.01";
        zoom.value = "1";
        if (stage) stage.hidden = false;
        draw();
      };
      img.onerror = function () {
        URL.revokeObjectURL(url);
        image = null;
        if (stage) stage.hidden = true;
      };
      img.src = url;
    }

    // --- input wiring ---

    fileInput.addEventListener("change", function () {
      var file = fileInput.files && fileInput.files[0];
      if (file) {
        loadImage(file);
      } else if (stage) {
        stage.hidden = true;
        image = null;
      }
    });

    zoom.addEventListener("input", function () {
      if (!image) return;
      // Zoom around the viewport center so the framed subject stays put.
      var prevScale = scale;
      scale = minScale * parseFloat(zoom.value);
      var centerX = VIEWPORT / 2;
      var centerY = VIEWPORT / 2;
      offsetX = centerX - ((centerX - offsetX) / prevScale) * scale;
      offsetY = centerY - ((centerY - offsetY) / prevScale) * scale;
      clampOffsets();
      draw();
    });

    // --- drag to pan (mouse + touch via Pointer Events) ---

    var dragging = false;
    var lastX = 0;
    var lastY = 0;

    canvas.addEventListener("pointerdown", function (e) {
      if (!image) return;
      dragging = true;
      lastX = e.clientX;
      lastY = e.clientY;
      canvas.setPointerCapture(e.pointerId);
    });
    canvas.addEventListener("pointermove", function (e) {
      if (!dragging) return;
      offsetX += e.clientX - lastX;
      offsetY += e.clientY - lastY;
      lastX = e.clientX;
      lastY = e.clientY;
      clampOffsets();
      draw();
    });
    function endDrag() {
      dragging = false;
    }
    canvas.addEventListener("pointerup", endDrag);
    canvas.addEventListener("pointercancel", endDrag);

    // --- export on submit ---

    var submitting = false;
    form.addEventListener("submit", function (e) {
      if (!image || submitting) return; // nothing cropped → submit as-is.
      e.preventDefault();

      var exportCanvas = document.createElement("canvas");
      exportCanvas.width = EXPORT_SIZE;
      exportCanvas.height = EXPORT_SIZE;
      var exportCtx = exportCanvas.getContext("2d");
      // The viewport shows the image at `scale`; map that same framing onto the
      // larger export canvas by scaling up uniformly.
      var ratio = EXPORT_SIZE / VIEWPORT;
      exportCtx.drawImage(
        image,
        offsetX * ratio,
        offsetY * ratio,
        image.naturalWidth * scale * ratio,
        image.naturalHeight * scale * ratio
      );

      exportCanvas.toBlob(
        function (blob) {
          if (blob) {
            var cropped = new File([blob], "avatar.jpg", {
              type: "image/jpeg",
            });
            var dt = new DataTransfer();
            dt.items.add(cropped);
            fileInput.files = dt.files;
          }
          submitting = true;
          form.submit();
        },
        "image/jpeg",
        0.9
      );
    });
  }

  document.querySelectorAll("[data-avatar-crop]").forEach(setupCropper);
})();
