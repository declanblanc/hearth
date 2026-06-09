// nav-toggle.js — collapses the header navigation behind a hamburger button on
// narrow screens (issue #33). Below the header's mobile breakpoint the nav links
// are hidden by CSS until .site-header gains the .nav-open class; this wires the
// button to toggle that class and keeps aria-expanded in sync for screen readers.
// Without JavaScript the links are simply always visible (the CSS only hides them
// in the collapsed state), so navigation still works.
(function () {
  "use strict";

  var header = document.querySelector(".site-header");
  var toggle = header && header.querySelector(".nav-toggle");
  var nav = header && header.querySelector("nav");
  if (!header || !toggle || !nav) return;

  function setOpen(open) {
    header.classList.toggle("nav-open", open);
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
  }

  toggle.addEventListener("click", function () {
    setOpen(!header.classList.contains("nav-open"));
  });

  // Following a link or tapping outside the header closes the menu, so it never
  // lingers open over the page after navigating.
  nav.addEventListener("click", function (event) {
    if (event.target.closest("a")) setOpen(false);
  });
  document.addEventListener("click", function (event) {
    if (!header.contains(event.target)) setOpen(false);
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") setOpen(false);
  });
})();
