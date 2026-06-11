// Rotates the mission statement's middle line through the things Hearth
// deliberately doesn't have ("No algorithms…", "No advertisements…", …), with a
// gentle cross-fade between items.
//
// Progressive enhancement: the template renders the full list as one readable
// sentence. This script runs only when motion is welcome; it swaps that sentence
// for a single rotating word. When prefers-reduced-motion is set — or when
// JavaScript is unavailable — the full sentence simply stays put.
(function () {
  var line = document.querySelector("[data-rotate]");
  if (!line) return;

  var words = (line.getAttribute("data-words") || "").split("|").filter(Boolean);
  if (words.length === 0) return;

  var prefersReducedMotion =
    window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (prefersReducedMotion) return; // honour the preference; keep the full sentence

  // Preserve the full list for screen readers, then hide the now-decorative
  // animated line from assistive tech so the rotation isn't announced over and
  // over.
  var fullSentence = line.textContent.trim();
  var screenReaderCopy = document.createElement("p");
  screenReaderCopy.className = "visually-hidden";
  screenReaderCopy.textContent = fullSentence;
  line.parentNode.insertBefore(screenReaderCopy, line);
  line.setAttribute("aria-hidden", "true");

  // Replace "No algorithms, no advertisements, …" with "No <word>…".
  line.textContent = "";
  line.appendChild(document.createTextNode("No "));
  var word = document.createElement("span");
  word.className = "mission-rotate-word";
  word.textContent = words[0];
  line.appendChild(word);
  line.appendChild(document.createTextNode("…")); // …

  var currentIndex = 0;
  var HOLD_MS = 2200; // how long each word rests fully visible
  var FADE_MS = 450; // cross-fade duration; keep in sync with the CSS transition

  setInterval(function () {
    word.classList.add("is-fading"); // fade the current word out
    setTimeout(function () {
      currentIndex = (currentIndex + 1) % words.length;
      word.textContent = words[currentIndex];
      word.classList.remove("is-fading"); // fade the next word in
    }, FADE_MS);
  }, HOLD_MS + FADE_MS);
})();
