// post-images.js — thumbnail previews for the compose image picker. Pure
// progressive enhancement: without this script the plain <input type="file">
// still selects and submits the images; the only thing lost is the preview.
//
// Wiring (see profile_view.html): a container carries data-image-preview with
// the file <input> somewhere inside it and an empty [data-preview-list] element
// where thumbnails are rendered. Each thumbnail has a remove button; removing
// one rebuilds the input's FileList (via DataTransfer) so the preview and the
// files that actually POST stay in sync.

(function () {
  "use strict";

  function setupPreview(root) {
    var fileInput = root.querySelector('input[type="file"]');
    var list = root.querySelector("[data-preview-list]");
    if (!fileInput || !list) {
      return; // markup not as expected — leave the plain input in place.
    }

    // Object URLs we've created, so we can revoke them and avoid leaking memory
    // each time the selection changes.
    var objectURLs = [];

    function revokeURLs() {
      objectURLs.forEach(function (url) {
        URL.revokeObjectURL(url);
      });
      objectURLs = [];
    }

    // Rebuild fileInput.files without the file at `removeIndex`, then re-render.
    function removeAt(removeIndex) {
      var kept = new DataTransfer();
      Array.prototype.forEach.call(fileInput.files, function (file, index) {
        if (index !== removeIndex) {
          kept.items.add(file);
        }
      });
      fileInput.files = kept.files;
      render();
    }

    function render() {
      revokeURLs();
      list.textContent = "";

      var files = fileInput.files;
      if (!files || files.length === 0) {
        list.hidden = true;
        return;
      }
      list.hidden = false;

      Array.prototype.forEach.call(files, function (file, index) {
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

    fileInput.addEventListener("change", render);
  }

  document.querySelectorAll("[data-image-preview]").forEach(setupPreview);
})();
