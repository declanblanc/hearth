// pull-to-refresh.js — lets touch users reload the page by swiping down from the
// very top, the gesture native apps use. It only engages on coarse (touch)
// pointers and only when the page is already scrolled to the top, so it never
// interferes with normal scrolling or desktop use. Without JavaScript the page
// simply has no pull-to-refresh and the browser's own reload still works.
(function () {
  "use strict";

  // Bail on devices without a primary touch pointer (desktops, most laptops),
  // where a downward drag at the top is just an over-scroll, not a refresh.
  if (!window.matchMedia || !window.matchMedia("(pointer: coarse)").matches) return;

  // How far (in px) the finger must travel before a release triggers a reload,
  // and how much we damp the pull so it feels rubber-banded rather than 1:1.
  var TRIGGER_DISTANCE = 70;
  var RESISTANCE = 0.5;

  var indicator = buildIndicator();
  document.body.appendChild(indicator);

  var startY = 0;
  var pullDistance = 0;
  var tracking = false; // a finger went down while at the top of the page

  document.addEventListener("touchstart", function (event) {
    // Only begin tracking when the page is at the top and it's a single finger;
    // multi-touch is a pinch/zoom, not a pull.
    if (window.scrollY > 0 || event.touches.length !== 1) {
      tracking = false;
      return;
    }
    tracking = true;
    startY = event.touches[0].clientY;
    pullDistance = 0;
  }, { passive: true });

  document.addEventListener("touchmove", function (event) {
    if (!tracking) return;

    var delta = event.touches[0].clientY - startY;

    // An upward move (or any scroll away from the top) ends the gesture so the
    // user can scroll the page normally after starting near the top.
    if (delta <= 0 || window.scrollY > 0) {
      reset();
      return;
    }

    // We're pulling down at the top: take over the gesture and show the
    // indicator following the finger, damped for a springy feel.
    pullDistance = delta * RESISTANCE;
    if (event.cancelable) event.preventDefault();
    showAt(pullDistance);
  }, { passive: false });

  document.addEventListener("touchend", function () {
    if (!tracking) return;

    if (pullDistance >= TRIGGER_DISTANCE) {
      trigger();
    } else {
      reset();
    }
  });

  // Position and reveal the indicator as the finger drags. It eases toward full
  // opacity as the pull approaches the trigger distance, and the flame rotates
  // to hint that releasing now will refresh.
  function showAt(distance) {
    var progress = Math.min(distance / TRIGGER_DISTANCE, 1);
    indicator.style.transform = "translate(-50%, " + distance + "px)";
    indicator.style.opacity = progress;
    indicator.classList.toggle("ready", distance >= TRIGGER_DISTANCE);
  }

  // Pull released past the threshold: lock the indicator into a spinning state
  // and reload. The spin is visible for the moment before navigation begins.
  function trigger() {
    tracking = false;
    indicator.classList.add("refreshing");
    indicator.style.transform = "translate(-50%, " + TRIGGER_DISTANCE + "px)";
    indicator.style.opacity = 1;
    window.location.reload();
  }

  // Pull abandoned before the threshold: spring the indicator back out of view.
  function reset() {
    tracking = false;
    pullDistance = 0;
    indicator.classList.remove("ready");
    indicator.classList.add("settling");
    indicator.style.transform = "";
    indicator.style.opacity = "";
    // Drop the transition class once the spring-back finishes so the next pull
    // tracks the finger instantly rather than lagging through a transition.
    window.setTimeout(function () {
      indicator.classList.remove("settling");
    }, 200);
  }

  // The floating refresh indicator: the brand flame inside a paper disc. Built
  // in JS so the markup lives next to the behavior and no template needs to
  // carry an element that only matters on touch devices.
  function buildIndicator() {
    var el = document.createElement("div");
    el.className = "pull-refresh";
    el.setAttribute("aria-hidden", "true");
    el.innerHTML =
      '<svg class="flame" viewBox="0 0 24 24" fill="currentColor">' +
      '<path d="M12 2c.4 3-1.7 4.6-3.2 6.3C7.2 10 6 11.7 6 14a6 6 0 0 0 12 0c0-1.8-.8-3.2-1.7-4.4-.3 1-1 1.7-2 1.9.8-2.3.2-4.7-2.3-9.5Zm0 18a3 3 0 0 1-3-3c0-1.2.7-2.1 1.5-2.9.2 1 .8 1.7 1.8 1.9-.5-1.4 0-2.7 1.1-3.8.6.8 1.6 1.9 1.6 3.6a3 3 0 0 1-3 3.2Z"/>' +
      "</svg>";
    return el;
  }
})();
