// post-images.js — compose-box enhancements for creating a post:
//
//   1. Thumbnail previews of the images chosen in the file picker, each with a
//      remove button that keeps the input's FileList in sync.
//   2. An upload progress indicator shown after the user clicks "Post", so a
//      large/slow upload doesn't make the page look frozen.
//
// Both are pure progressive enhancement. Without this script the plain
// <input type="file"> still selects images and the form still submits normally;
// the only things lost are the preview thumbnails and the progress bar.
//
// Wiring (see profile_view.html): the <form> carries data-image-preview and
// contains a file <input>, an empty [data-preview-list] for thumbnails, and a
// hidden [data-upload-progress] block (with [data-upload-bar] and
// [data-upload-label]) for the progress UI.

(function () {
  "use strict";

  // ---- thumbnail previews -------------------------------------------------

  // Keep in step with media.MaxImagesPerPost on the server.
  var MAX_IMAGES = 5;

  function setupPreview(form) {
    var fileInput = form.querySelector('input[type="file"]');
    var list = form.querySelector("[data-preview-list]");
    if (!fileInput || !list) {
      return; // markup not as expected — leave the plain input in place.
    }

    // The accumulated selection is our source of truth. A native file input
    // *replaces* its FileList every time the picker is used, so without this we
    // would lose earlier photos each time the user adds another. We keep the
    // list here and push it back into the input (via DataTransfer) so the form
    // submits exactly the photos shown as thumbnails.
    var selected = [];

    // Object URLs we've created, so we can revoke them and avoid leaking memory
    // each time the selection changes.
    var objectURLs = [];

    function revokeURLs() {
      objectURLs.forEach(function (url) {
        URL.revokeObjectURL(url);
      });
      objectURLs = [];
    }

    // Mirror `selected` onto the file input so a normal form POST carries it.
    function syncInput() {
      var dt = new DataTransfer();
      selected.forEach(function (file) {
        dt.items.add(file);
      });
      fileInput.files = dt.files;
    }

    function isAlreadySelected(file) {
      return selected.some(function (existing) {
        return (
          existing.name === file.name &&
          existing.size === file.size &&
          existing.lastModified === file.lastModified
        );
      });
    }

    // Merge a freshly picked FileList into the accumulated selection, skipping
    // duplicates and respecting the per-post image cap.
    function addFiles(picked) {
      Array.prototype.forEach.call(picked, function (file) {
        if (selected.length >= MAX_IMAGES) return;
        if (isAlreadySelected(file)) return;
        selected.push(file);
      });
      syncInput();
      render();
    }

    function removeAt(removeIndex) {
      selected.splice(removeIndex, 1);
      syncInput();
      render();
    }

    function render() {
      revokeURLs();
      list.textContent = "";

      if (selected.length === 0) {
        list.hidden = true;
        return;
      }
      list.hidden = false;

      selected.forEach(function (file, index) {
        if (file.type.indexOf("image/") !== 0) {
          return; // skip anything that isn't an image we can render.
        }
        var url = URL.createObjectURL(file);
        objectURLs.push(url);

        var item = document.createElement("div");
        item.className = "preview-thumb";

        var img = document.createElement("img");
        img.src = url;
        img.alt = file.name;

        var remove = document.createElement("button");
        remove.type = "button"; // never submit the form.
        remove.className = "preview-remove";
        remove.setAttribute("aria-label", "Remove " + file.name);
        remove.textContent = "×";
        remove.addEventListener("click", function () {
          removeAt(index);
        });

        item.appendChild(img);
        item.appendChild(remove);
        list.appendChild(item);
      });
    }

    fileInput.addEventListener("change", function () {
      addFiles(fileInput.files);
    });
  }

  // ---- upload progress on submit ------------------------------------------

  function setupSubmit(form) {
    // If the browser can't do an XHR upload with progress events, leave the
    // native submit alone — the post still works, just without a progress bar.
    if (!window.FormData || !window.XMLHttpRequest || !new XMLHttpRequest().upload) {
      return;
    }

    var button = form.querySelector('button[type="submit"]') || form.querySelector("button");
    var progress = form.querySelector("[data-upload-progress]");
    var bar = form.querySelector("[data-upload-bar]");
    var label = form.querySelector("[data-upload-label]");
    var buttonLabel = button ? button.textContent : "";
    var submitting = false;

    function setLabel(text) {
      if (label) label.textContent = text;
    }

    function setPercent(percent) {
      if (bar) bar.style.width = percent + "%";
      if (progress) progress.setAttribute("aria-valuenow", String(percent));
    }

    function showProgress() {
      if (progress) {
        progress.hidden = false;
        progress.setAttribute("role", "progressbar");
        progress.setAttribute("aria-valuemin", "0");
        progress.setAttribute("aria-valuemax", "100");
      }
      setPercent(0);
      setLabel("Uploading…");
    }

    // Restore the form to its idle state so the user can retry after a failure.
    function reset() {
      submitting = false;
      if (button) {
        button.disabled = false;
        button.textContent = buttonLabel;
      }
      if (progress) progress.hidden = true;
    }

    form.addEventListener("submit", function (event) {
      if (submitting) {
        event.preventDefault();
        return;
      }
      event.preventDefault();
      submitting = true;

      // Where to land once the server responds. The server replies with a 303
      // redirect (to the profile, or back to it with ?post_error=…); XHR follows
      // it transparently, and xhr.responseURL is the final landing page. If the
      // browser doesn't expose responseURL, fall back to the current path — the
      // compose box only ever appears on the author's own profile.
      var fallbackURL = window.location.pathname;

      if (button) {
        button.disabled = true;
        button.textContent = "Posting…";
      }
      showProgress();

      var xhr = new XMLHttpRequest();
      xhr.open(form.method || "post", form.action, true);

      xhr.upload.addEventListener("progress", function (e) {
        if (!e.lengthComputable) {
          return; // size unknown — leave the bar at its indeterminate start.
        }
        var percent = Math.round((e.loaded / e.total) * 100);
        setPercent(percent);
        // Once the bytes are all sent, the server still has to store and process
        // the images, so reflect that the work isn't quite done.
        setLabel(percent >= 100 ? "Processing…" : "Uploading… " + percent + "%");
      });

      xhr.addEventListener("load", function () {
        window.location.href = xhr.responseURL || fallbackURL;
      });

      xhr.addEventListener("error", function () {
        setLabel("Upload failed — please try again.");
        reset();
      });

      xhr.addEventListener("abort", reset);

      xhr.send(new FormData(form));
    });
  }

  document.querySelectorAll("[data-image-preview]").forEach(function (form) {
    setupPreview(form);
    setupSubmit(form);
  });
})();
