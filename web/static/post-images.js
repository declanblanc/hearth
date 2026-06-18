// post-images.js — compose-box enhancements for creating a post:
//
//   1. Client-side image compression (Technical Plan §4.5): every chosen photo is
//      shrunk to ≤1600px / ~80% JPEG via browser-image-compression before it ever
//      enters the form, so posts upload and load fast. HEIC from iPhones is drawn
//      through a canvas and comes out JPEG.
//   2. Thumbnail previews of the chosen photos and a video's poster, each with a
//      remove button that keeps the input's FileList in sync.
//   3. Video support: pick one MP4 (≤60s) and we grab a poster frame on the
//      client; the video and poster upload together. Photos and a video can share
//      a post — up to 5 photos and one video.
//   4. An upload progress indicator shown after the user clicks "Post".
//
// All of this is progressive enhancement. Without this script the plain
// <input type="file"> still selects media and the form still submits normally —
// the only things lost are compression, previews, the poster, and the progress
// bar. (Without compression the server's per-file size cap may reject a large
// original, which is the intended guardrail.)
//
// Wiring (see composemodal in base.html): the <form> carries data-image-preview
// and contains a file <input>, an empty [data-preview-list] for thumbnails, and a
// [data-upload-progress] block (with [data-upload-bar] and [data-upload-label])
// reused for both the "preparing" status and the upload progress bar.

(function () {
  "use strict";

  // Keep these in step with the server: media.MaxImagesPerPost and
  // media.MaxVideoFileSize, and the 60s client-side duration cap from the plan.
  var MAX_IMAGES = 5;
  var MAX_VIDEO_BYTES = 50 * 1024 * 1024;
  var MAX_VIDEO_SECONDS = 60;

  // Compression targets straight from Technical Plan §4.5.
  var COMPRESSION_OPTS = {
    maxWidthOrHeight: 1600,
    initialQuality: 0.8,
    useWebWorker: true,
    fileType: "image/jpeg",
  };

  function isImage(file) {
    return file.type.indexOf("image/") === 0;
  }
  function isVideo(file) {
    return file.type.indexOf("video/") === 0;
  }

  function setup(form) {
    var fileInput = form.querySelector('input[type="file"]');
    var list = form.querySelector("[data-preview-list]");
    if (!fileInput || !list) {
      return; // markup not as expected — leave the plain input in place.
    }

    var progress = form.querySelector("[data-upload-progress]");
    var label = form.querySelector("[data-upload-label]");
    var bar = form.querySelector("[data-upload-bar]");

    // A post may hold up to MAX_IMAGES photos and one video, in any combination.
    // We hold the source of truth here and mirror it onto the file input (via
    // DataTransfer) so a normal form POST carries exactly what's shown.
    var images = []; // compressed image Files
    var video = null; // { file: File, poster: Blob|null, posterURL: string }
    var objectURLs = []; // preview URLs to revoke when the selection changes

    // Outstanding compression / poster-extraction tasks. Submission waits until
    // this reaches zero so we never upload before media is ready.
    var pending = 0;

    // ---- status + thumbnails ------------------------------------------------

    // The progress block doubles as a status line while we prepare media. Showing
    // it with the bar still at 0 reads as a quiet "working…" cue.
    function setStatus(text) {
      if (label) label.textContent = text;
      if (progress && text) progress.hidden = false;
    }
    function clearStatus() {
      if (label) label.textContent = "";
      if (progress) progress.hidden = true;
    }
    function beginWork(text) {
      pending++;
      setStatus(text);
    }
    function endWork() {
      pending--;
      if (pending <= 0) {
        pending = 0;
        clearStatus();
      }
    }

    function revokeURLs() {
      objectURLs.forEach(function (url) {
        URL.revokeObjectURL(url);
      });
      objectURLs = [];
    }

    function syncInput() {
      var dt = new DataTransfer();
      images.forEach(function (file) {
        dt.items.add(file);
      });
      if (video) {
        dt.items.add(video.file);
      }
      fileInput.files = dt.files;
    }

    function clearVideo() {
      if (video && video.posterURL) {
        URL.revokeObjectURL(video.posterURL);
      }
      video = null;
    }

    function render() {
      revokeURLs();
      list.textContent = "";

      // Each preview entry knows how to remove itself from the selection.
      var entries = [];
      images.forEach(function (file, i) {
        var url = URL.createObjectURL(file);
        objectURLs.push(url);
        entries.push({
          url: url,
          alt: file.name,
          video: false,
          remove: function () {
            images.splice(i, 1);
          },
        });
      });
      if (video) {
        entries.push({
          url: video.posterURL,
          alt: video.file.name,
          video: true,
          remove: clearVideo,
        });
      }

      if (entries.length === 0) {
        list.hidden = true;
        return;
      }
      list.hidden = false;

      entries.forEach(function (entry) {
        var item = document.createElement("div");
        item.className = "preview-thumb" + (entry.video ? " preview-thumb-video" : "");

        if (entry.url) {
          var img = document.createElement("img");
          img.src = entry.url;
          img.alt = entry.alt;
          item.appendChild(img);
        }

        var remove = document.createElement("button");
        remove.type = "button"; // never submit the form.
        remove.className = "preview-remove";
        remove.setAttribute("aria-label", "Remove " + entry.alt);
        remove.textContent = "×";
        remove.addEventListener("click", function () {
          entry.remove();
          syncInput();
          render();
        });

        item.appendChild(remove);
        list.appendChild(item);
      });
    }

    // ---- adding images (with compression) -----------------------------------

    function compress(file) {
      if (typeof window.imageCompression !== "function") {
        return Promise.resolve(file); // library missing — fall back to original.
      }
      return window
        .imageCompression(file, COMPRESSION_OPTS)
        .then(function (blob) {
          var base = file.name.replace(/\.[^.]+$/, "");
          return new File([blob], base + ".jpg", { type: "image/jpeg" });
        })
        .catch(function () {
          return file; // on failure, keep the original; the server still guards size.
        });
    }

    function addImages(picked) {
      beginWork("Compressing…");

      // Compress sequentially so the web worker isn't flooded; the chosen photos
      // are few (≤5) so latency is fine, and it keeps memory bounded.
      var chain = Promise.resolve();
      picked.forEach(function (file) {
        chain = chain.then(function () {
          if (images.length >= MAX_IMAGES) return;
          return compress(file).then(function (out) {
            if (images.length < MAX_IMAGES) images.push(out);
          });
        });
      });
      chain.then(function () {
        endWork();
        syncInput();
        render();
      });
    }

    // ---- adding a video (with a client-extracted poster) --------------------

    function addVideo(file) {
      if (file.type !== "video/mp4") {
        setStatus("Only MP4 video is supported.");
        return;
      }
      if (file.size > MAX_VIDEO_BYTES) {
        setStatus("That video is too large (50 MB max).");
        return;
      }

      beginWork("Preparing video…");
      extractPoster(file).then(function (result) {
        endWork();
        if (result.error) {
          setStatus(result.error);
          return;
        }
        clearVideo(); // a new pick replaces any existing video.
        video = { file: file, poster: result.poster, posterURL: result.posterURL };
        syncInput();
        render();
      });
    }

    // extractPoster loads the video to read its duration and grab a still frame.
    // Resolves with { poster, posterURL } (poster may be null if the frame can't
    // be captured — the video still uploads), or { error } for a rejection the
    // user should see.
    function extractPoster(file) {
      return new Promise(function (resolve) {
        var url = URL.createObjectURL(file);
        var probe = document.createElement("video");
        probe.preload = "metadata";
        probe.muted = true;
        probe.src = url;

        var settled = false;
        function fail(message) {
          if (settled) return;
          settled = true;
          URL.revokeObjectURL(url);
          resolve({ error: message });
        }
        function succeed(poster, posterURL) {
          if (settled) return;
          settled = true;
          URL.revokeObjectURL(url);
          resolve({ poster: poster, posterURL: posterURL });
        }

        probe.addEventListener("loadedmetadata", function () {
          if (probe.duration > MAX_VIDEO_SECONDS + 0.5) {
            fail("Videos must be " + MAX_VIDEO_SECONDS + " seconds or shorter.");
            return;
          }
          // Seek slightly in so the poster isn't a black opening frame.
          try {
            probe.currentTime = Math.min(1, probe.duration / 2);
          } catch (e) {
            /* some browsers fire seeked from the metadata position instead */
          }
        });

        probe.addEventListener("seeked", function () {
          var canvas = document.createElement("canvas");
          canvas.width = probe.videoWidth || 1280;
          canvas.height = probe.videoHeight || 720;
          try {
            canvas.getContext("2d").drawImage(probe, 0, 0, canvas.width, canvas.height);
          } catch (e) {
            succeed(null, ""); // tainted/undrawable — upload without a poster.
            return;
          }
          canvas.toBlob(
            function (blob) {
              if (!blob) {
                succeed(null, "");
                return;
              }
              succeed(blob, URL.createObjectURL(blob));
            },
            "image/jpeg",
            0.8
          );
        });

        probe.addEventListener("error", function () {
          fail("That video couldn't be read.");
        });
      });
    }

    fileInput.addEventListener("change", function () {
      var picked = Array.prototype.slice.call(fileInput.files);
      if (picked.length === 0) return;

      // Photos and a video can be chosen together; handle each kind. Only the
      // first video in a multi-select pick is taken (a post holds one).
      var pickedImages = picked.filter(isImage);
      var pickedVideo = null;
      picked.forEach(function (file) {
        if (isVideo(file) && !pickedVideo) pickedVideo = file;
      });

      if (pickedImages.length) addImages(pickedImages);
      if (pickedVideo) addVideo(pickedVideo);
    });

    // Let users paste an image straight from the clipboard — e.g. a screenshot
    // or a photo copied from another app — anywhere in the composer; it attaches
    // exactly like a picked photo (compressed, previewed, synced to the input).
    // Videos aren't pasteable, so we only look for image items.
    form.addEventListener("paste", function (event) {
      var data = event.clipboardData;
      if (!data || !data.items) return;
      var pasted = [];
      Array.prototype.forEach.call(data.items, function (item) {
        if (item.kind === "file" && item.type.indexOf("image/") === 0) {
          var file = item.getAsFile();
          if (file) pasted.push(namedPaste(file));
        }
      });
      if (pasted.length) addImages(pasted);
    });

    // Clipboard images often arrive without a usable filename; give them one so
    // previews and the eventual upload read sensibly.
    function namedPaste(file) {
      if (file.name) return file;
      var ext = (file.type.split("/")[1] || "png").replace("jpeg", "jpg");
      return new File([file], "pasted-" + Date.now() + "." + ext, { type: file.type });
    }

    // ---- upload progress on submit ------------------------------------------

    // If the browser can't do an XHR upload with progress events, leave the
    // native submit alone — the post still works, just without a progress bar.
    if (!window.FormData || !window.XMLHttpRequest || !new XMLHttpRequest().upload) {
      return;
    }

    var button = form.querySelector('button[type="submit"]') || form.querySelector("button");
    var buttonLabel = button ? button.textContent : "";
    var submitting = false;

    function setPercent(percent) {
      if (bar) bar.style.width = percent + "%";
      if (progress) progress.setAttribute("aria-valuenow", String(percent));
    }

    function resetSubmit() {
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
      // Don't submit while compression or poster extraction is still running.
      if (pending > 0) {
        event.preventDefault();
        setStatus("Still preparing your media…");
        return;
      }
      event.preventDefault();
      submitting = true;

      // Where to land once the server responds: it replies with a 303 redirect
      // (to the profile, or back with ?post_error=…); XHR follows it and
      // xhr.responseURL is the final landing page. If responseURL isn't exposed,
      // fall back to the current path — compose only appears on the author's own
      // pages.
      var fallbackURL = window.location.pathname;

      if (button) {
        button.disabled = true;
        button.textContent = "Posting…";
      }
      if (progress) {
        progress.hidden = false;
        progress.setAttribute("role", "progressbar");
        progress.setAttribute("aria-valuemin", "0");
        progress.setAttribute("aria-valuemax", "100");
      }
      setPercent(0);
      setStatus("Uploading…");

      // FormData(form) already carries the file input (images and/or the video)
      // and the text. A video's poster is a separate Blob, appended here.
      var data = new FormData(form);
      if (video && video.poster) {
        data.append("video_poster", video.poster, "poster.jpg");
      }

      var xhr = new XMLHttpRequest();
      xhr.open(form.method || "post", form.action, true);

      xhr.upload.addEventListener("progress", function (e) {
        if (!e.lengthComputable) return; // size unknown — leave the bar at its start.
        var percent = Math.round((e.loaded / e.total) * 100);
        setPercent(percent);
        // Once the bytes are sent, the server still stores them, so reflect that.
        setStatus(percent >= 100 ? "Processing…" : "Uploading… " + percent + "%");
      });

      xhr.addEventListener("load", function () {
        window.location.href = xhr.responseURL || fallbackURL;
      });

      xhr.addEventListener("error", function () {
        setStatus("Upload failed — please try again.");
        resetSubmit();
      });

      xhr.addEventListener("abort", resetSubmit);

      xhr.send(data);
    });
  }

  document.querySelectorAll("[data-image-preview]").forEach(function (form) {
    setup(form);
  });
})();
